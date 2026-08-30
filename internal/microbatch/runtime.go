package microbatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"app/internal/storage"
)

var (
	ErrSourceExists    = errors.New("microbatch source already registered")
	ErrSourceNotFound  = errors.New("microbatch source not found")
	ErrTopologyExists  = errors.New("microbatch topology already registered")
	ErrTopologyMissing = errors.New("microbatch topology not found")
	ErrTaskCount       = errors.New("task count must be positive")
	ErrPartition       = errors.New("partitioner returned an out-of-range task")
)

type Source struct {
	Depot        storage.DepotID
	PartitionKey func([]byte) ([]byte, error)
}

type Item struct {
	Depot     storage.DepotID
	Partition uint32
	Record    storage.Record
}

type Event struct {
	Get    func(context.Context, storage.StatePartition, []byte) ([]byte, bool, error)
	Scan   func(context.Context, storage.StatePartition, []byte, []byte, int, bool) ([]storage.Entry, error)
	Set    func(storage.StatePartition, []byte, []byte)
	Delete func(storage.StatePartition, []byte)
}

type Topology struct {
	Name    storage.TopologyID
	Sources []Source
	Handle  func(context.Context, *Event, []Item) error
}

type Runtime struct {
	Append     func(context.Context, storage.DepotID, string, []byte) (storage.Record, error)
	Advance    func(context.Context, storage.TopologyID) (int, error)
	AdvanceAll func(context.Context) (int, error)
	Rebuild    func(context.Context) error
}

func New(module storage.ModuleID, taskCount uint32, choosePartition func([]byte) uint32, store *storage.Store, topologies ...Topology) (*Runtime, error) {
	if taskCount == 0 {
		return nil, ErrTaskCount
	}
	byName := make(map[storage.TopologyID]Topology, len(topologies))
	byDepot := make(map[storage.DepotID]Source)
	for _, topology := range topologies {
		if _, ok := byName[topology.Name]; ok {
			return nil, fmt.Errorf("%w: %s", ErrTopologyExists, topology.Name)
		}
		byName[topology.Name] = topology
		for _, source := range topology.Sources {
			if _, ok := byDepot[source.Depot]; ok {
				return nil, fmt.Errorf("%w: %s", ErrSourceExists, source.Depot)
			}
			byDepot[source.Depot] = source
		}
	}
	var maintenance sync.RWMutex

	advanceLocked := func(ctx context.Context, topology Topology) (int, error) {
		items := make([]Item, 0)
		checkpoints := make([]storage.Cursor, 0)
		for _, source := range topology.Sources {
			for partition := range taskCount {
				depot := storage.DepotPartition{Module: module, Depot: source.Depot, Partition: partition}
				cursor := storage.Cursor{Module: module, Topology: topology.Name, Depot: source.Depot, Partition: partition}
				checkpoint, err := store.Checkpoint(ctx, cursor)
				if err != nil {
					return 0, err
				}
				high, err := store.HighWater(ctx, depot)
				if err != nil {
					return 0, err
				}
				if high <= checkpoint {
					continue
				}
				for position := checkpoint + 1; position <= high; {
					records, err := store.Read(ctx, depot, position, 256)
					if err != nil {
						return 0, err
					}
					if len(records) == 0 || records[0].Position != position {
						return 0, fmt.Errorf("record gap in %s/%d at %d", source.Depot, partition, position)
					}
					for _, record := range records {
						if record.Position > high {
							break
						}
						items = append(items, Item{Depot: source.Depot, Partition: partition, Record: record})
						position = record.Position + 1
					}
				}
				cursor.Position = high
				checkpoints = append(checkpoints, cursor)
			}
		}
		if len(items) == 0 {
			return 0, nil
		}
		mutations := make([]storage.Mutation, 0, len(items))
		event := &Event{}
		event.Get = func(ctx context.Context, state storage.StatePartition, key []byte) ([]byte, bool, error) {
			for i := len(mutations) - 1; i >= 0; i-- {
				mutation := mutations[i]
				if mutation.State == state && bytes.Equal(mutation.Key, key) {
					return append([]byte(nil), mutation.Value...), !mutation.Delete, nil
				}
			}
			return store.GetState(ctx, state, key)
		}
		event.Scan = store.ScanState
		event.Set = func(state storage.StatePartition, key, value []byte) {
			mutations = append(mutations, storage.Mutation{State: state, Key: append([]byte(nil), key...), Value: append([]byte(nil), value...)})
		}
		event.Delete = func(state storage.StatePartition, key []byte) {
			mutations = append(mutations, storage.Mutation{State: state, Key: append([]byte(nil), key...), Delete: true})
		}
		if err := topology.Handle(ctx, event, items); err != nil {
			return 0, err
		}
		hash := sha256.New()
		for _, checkpoint := range checkpoints {
			_, _ = fmt.Fprintf(hash, "%s/%d/%d;", checkpoint.Depot, checkpoint.Partition, checkpoint.Position)
		}
		batchID := storage.BatchID(fmt.Sprintf("microbatch/%s/%x", topology.Name, hash.Sum(nil)))
		_, err := store.Commit(ctx, storage.CommitRequest{Module: module, BatchID: batchID, Mutations: mutations, Checkpoints: checkpoints})
		if err != nil {
			return 0, err
		}
		return len(items), nil
	}

	runtime := &Runtime{}
	runtime.Append = func(ctx context.Context, depot storage.DepotID, id string, payload []byte) (storage.Record, error) {
		source, ok := byDepot[depot]
		if !ok {
			return storage.Record{}, fmt.Errorf("%w: %s", ErrSourceNotFound, depot)
		}
		key, err := source.PartitionKey(payload)
		if err != nil {
			return storage.Record{}, err
		}
		partition := choosePartition(key)
		if partition >= taskCount {
			return storage.Record{}, ErrPartition
		}
		maintenance.RLock()
		defer maintenance.RUnlock()
		record, _, err := store.Append(ctx, storage.DepotPartition{Module: module, Depot: depot, Partition: partition}, id, payload)
		return record, err
	}
	runtime.Advance = func(ctx context.Context, topology storage.TopologyID) (int, error) {
		definition, ok := byName[topology]
		if !ok {
			return 0, fmt.Errorf("%w: %s", ErrTopologyMissing, topology)
		}
		maintenance.Lock()
		defer maintenance.Unlock()
		return advanceLocked(ctx, definition)
	}
	runtime.AdvanceAll = func(ctx context.Context) (int, error) {
		maintenance.Lock()
		defer maintenance.Unlock()
		total := 0
		for _, topology := range topologies {
			count, err := advanceLocked(ctx, topology)
			if err != nil {
				return total, err
			}
			total += count
		}
		return total, nil
	}
	runtime.Rebuild = func(ctx context.Context) error {
		maintenance.Lock()
		defer maintenance.Unlock()
		if err := store.ResetDerived(ctx, module); err != nil {
			return err
		}
		for _, topology := range topologies {
			if _, err := advanceLocked(ctx, topology); err != nil {
				return err
			}
		}
		return nil
	}
	return runtime, nil
}

type Scheduler struct {
	Run func(context.Context) error
}

func NewScheduler(interval time.Duration, advance func(context.Context) error, wait func(context.Context, time.Duration) error) (*Scheduler, error) {
	if interval <= 0 {
		return nil, errors.New("microbatch interval must be positive")
	}
	if wait == nil {
		wait = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return &Scheduler{Run: func(ctx context.Context) error {
		for {
			if err := wait(ctx, interval); err != nil {
				return err
			}
			if err := advance(ctx); err != nil {
				return err
			}
		}
	}}, nil
}
