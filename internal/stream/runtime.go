package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"app/internal/storage"
)

var (
	ErrSourceNotFound = errors.New("stream source not found")
	ErrSourceExists   = errors.New("stream source already registered")
	ErrRecordGap      = errors.New("depot partition has a record gap")
	ErrCommitResult   = errors.New("committed stream result not found")
	ErrTaskCount      = errors.New("task count must be positive")
	ErrPartition      = errors.New("partitioner returned an out-of-range task")
)

type Ack map[storage.TopologyID][]byte

type Event struct {
	Get    func(context.Context, storage.StatePartition, []byte) ([]byte, bool, error)
	Set    func(storage.StatePartition, []byte, []byte)
	Delete func(storage.StatePartition, []byte)
}

type Source struct {
	Depot        storage.DepotID
	Topology     storage.TopologyID
	PartitionKey func([]byte) ([]byte, error)
	Handle       func(context.Context, *Event, storage.Record, uint32) ([]byte, error)
}

type Runtime struct {
	Append  func(context.Context, storage.DepotID, string, []byte) (Ack, error)
	Enqueue func(context.Context, storage.DepotID, string, []byte) (uint64, error)
	Replay  func(context.Context) error
	Rebuild func(context.Context) error
}

func New(module storage.ModuleID, taskCount uint32, choosePartition func([]byte) uint32, store *storage.Store, sources ...Source) (*Runtime, error) {
	if taskCount == 0 {
		return nil, ErrTaskCount
	}
	byDepot := make(map[storage.DepotID]Source, len(sources))
	for _, source := range sources {
		if _, ok := byDepot[source.Depot]; ok {
			return nil, fmt.Errorf("%w: %s", ErrSourceExists, source.Depot)
		}
		byDepot[source.Depot] = source
	}
	var maintenance sync.RWMutex
	taskLocks := make([]sync.Mutex, taskCount)
	batchID := func(source Source, partition uint32, position uint64) storage.BatchID {
		return storage.BatchID(fmt.Sprintf("stream/%s/%s/%d/%d", source.Topology, source.Depot, partition, position))
	}
	process := func(ctx context.Context, source Source, partition uint32, record storage.Record) ([]byte, error) {
		var lastErr error
		for range 3 {
			mutations := make([]storage.Mutation, 0, 4)
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
			event.Set = func(state storage.StatePartition, key, value []byte) {
				mutations = append(mutations, storage.Mutation{State: state, Key: append([]byte(nil), key...), Value: append([]byte(nil), value...)})
			}
			event.Delete = func(state storage.StatePartition, key []byte) {
				mutations = append(mutations, storage.Mutation{State: state, Key: append([]byte(nil), key...), Delete: true})
			}
			ack, err := source.Handle(ctx, event, record, partition)
			if err != nil {
				return nil, err
			}
			cursor := storage.Cursor{Module: module, Topology: source.Topology, Depot: source.Depot, Partition: partition, Position: record.Position}
			applied, err := store.Commit(ctx, storage.CommitRequest{
				Module:     module,
				BatchID:    batchID(source, partition, record.Position),
				Mutations:  mutations,
				Checkpoint: cursor,
				Result:     ack,
			})
			if err == nil && applied {
				return ack, nil
			}
			if err == nil {
				result, found, resultErr := store.CommitResult(ctx, module, batchID(source, partition, record.Position))
				if resultErr != nil {
					return nil, resultErr
				}
				if !found {
					return nil, ErrCommitResult
				}
				return result, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	drain := func(ctx context.Context, source Source, partition uint32, target uint64) ([]byte, error) {
		cursor := storage.Cursor{Module: module, Topology: source.Topology, Depot: source.Depot, Partition: partition}
		checkpoint, err := store.Checkpoint(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if checkpoint >= target {
			result, found, err := store.CommitResult(ctx, module, batchID(source, partition, target))
			if err != nil || found {
				return result, err
			}
			return nil, ErrCommitResult
		}
		depot := storage.DepotPartition{Module: module, Depot: source.Depot, Partition: partition}
		var targetResult []byte
		for checkpoint < target {
			records, err := store.Read(ctx, depot, checkpoint+1, 256)
			if err != nil {
				return nil, err
			}
			if len(records) == 0 || records[0].Position != checkpoint+1 {
				return nil, fmt.Errorf("%w after position %d", ErrRecordGap, checkpoint)
			}
			for _, record := range records {
				if record.Position > target {
					break
				}
				result, err := process(ctx, source, partition, record)
				if err != nil {
					return nil, err
				}
				checkpoint = record.Position
				if checkpoint == target {
					targetResult = result
					break
				}
			}
		}
		return targetResult, nil
	}
	replayLocked := func(ctx context.Context) error {
		for _, source := range sources {
			for partition := range taskCount {
				depot := storage.DepotPartition{Module: module, Depot: source.Depot, Partition: partition}
				high, err := store.HighWater(ctx, depot)
				if err != nil {
					return err
				}
				if high == 0 {
					continue
				}
				if _, err := drain(ctx, source, partition, high); err != nil {
					return err
				}
			}
		}
		return nil
	}

	runtime := &Runtime{}
	runtime.Append = func(ctx context.Context, depot storage.DepotID, id string, payload []byte) (Ack, error) {
		source, ok := byDepot[depot]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrSourceNotFound, depot)
		}
		partitionKey, err := source.PartitionKey(payload)
		if err != nil {
			return nil, err
		}
		partition := choosePartition(partitionKey)
		if partition >= taskCount {
			return nil, ErrPartition
		}
		maintenance.RLock()
		defer maintenance.RUnlock()
		taskLocks[partition].Lock()
		defer taskLocks[partition].Unlock()
		record, duplicate, err := store.Append(ctx, storage.DepotPartition{Module: module, Depot: depot, Partition: partition}, id, payload)
		if err != nil {
			return nil, err
		}
		var ack []byte
		if duplicate {
			ack, duplicate, err = store.CommitResult(ctx, module, batchID(source, partition, record.Position))
			if err != nil {
				return nil, err
			}
		}
		if !duplicate {
			ack, err = drain(ctx, source, partition, record.Position)
			if err != nil {
				return nil, err
			}
		}
		result := Ack{}
		if ack != nil {
			result[source.Topology] = ack
		}
		return result, nil
	}
	runtime.Enqueue = func(ctx context.Context, depot storage.DepotID, id string, payload []byte) (uint64, error) {
		source, ok := byDepot[depot]
		if !ok {
			return 0, fmt.Errorf("%w: %s", ErrSourceNotFound, depot)
		}
		partitionKey, err := source.PartitionKey(payload)
		if err != nil {
			return 0, err
		}
		partition := choosePartition(partitionKey)
		if partition >= taskCount {
			return 0, ErrPartition
		}
		maintenance.RLock()
		defer maintenance.RUnlock()
		taskLocks[partition].Lock()
		defer taskLocks[partition].Unlock()
		record, _, err := store.Append(ctx, storage.DepotPartition{Module: module, Depot: depot, Partition: partition}, id, payload)
		return record.Position, err
	}
	runtime.Replay = func(ctx context.Context) error {
		maintenance.Lock()
		defer maintenance.Unlock()
		return replayLocked(ctx)
	}
	runtime.Rebuild = func(ctx context.Context) error {
		maintenance.Lock()
		defer maintenance.Unlock()
		if err := store.ResetDerived(ctx, module); err != nil {
			return err
		}
		return replayLocked(ctx)
	}
	return runtime, nil
}
