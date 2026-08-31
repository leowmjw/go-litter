package tutorial

import (
	"context"
	"encoding/json"
	"errors"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

// Stage 5 - Stream and Microbatch ETLs.
//
// This stage counts the same kind of event (a "Ping" for a key) through two
// parallel ETLs over the same depot-partition scheme to make the documented
// differences directly comparable:
//
//   - Stream: a record is processed as soon as it is appended; the
//     acknowledged Append call does not return until that record's PState
//     effect is committed and queryable (at-most-once/at-least-once by
//     configured retry, ordered within a depot partition).
//   - Microbatch: Append only durably logs the record; Advance/AdvanceAll
//     must be called (here, driven by a Scheduler using injectable virtual
//     time rather than a real 30s sleep) before its effect is visible, and
//     repeated Advance calls after a commit have no additional effect
//     (exactly-once).

const (
	Stage5ModuleName storage.ModuleID = "tutorial-stage5"
	PingDepot        storage.DepotID  = "ping"

	StreamTopic      storage.TopologyID = "stream-pings"
	StreamCountState storage.StateID    = "stream-count"

	MicrobatchPingDepot  storage.DepotID    = "microbatch-ping"
	MicrobatchTopic      storage.TopologyID = "microbatch-pings"
	MicrobatchCountState storage.StateID    = "microbatch-count"
)

var ErrInvalidPing = errors.New("key is required")

type Ping struct {
	Key string `json:"key"`
}

// Stage5StreamModule demonstrates stream ETL semantics.
type Stage5StreamModule struct {
	Append func(context.Context, string) error
	Count  func(context.Context, string) (uint64, error)
}

func NewStage5Stream(store *storage.Store, taskCount uint32) (*Stage5StreamModule, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	state := func(task uint32) storage.StatePartition {
		return storage.StatePartition{Module: Stage5ModuleName, State: StreamCountState, Partition: task}
	}
	runtime, err := stream.New(Stage5ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: PingDepot, Topology: StreamTopic,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var p Ping
				if err := json.Unmarshal(payload, &p); err != nil {
					return nil, err
				}
				return []byte(p.Key), nil
			},
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				var p Ping
				if err := json.Unmarshal(record.Payload, &p); err != nil {
					return nil, err
				}
				if p.Key == "" {
					return nil, ErrInvalidPing
				}
				raw, _, err := event.Get(ctx, state(task), []byte(p.Key))
				if err != nil {
					return nil, err
				}
				event.Set(state(task), []byte(p.Key), partition.Uint64Key(decodeUint64(raw)+1))
				return nil, nil
			},
		},
	)
	if err != nil {
		return nil, err
	}
	module := &Stage5StreamModule{}
	module.Append = func(ctx context.Context, key string) error {
		if key == "" {
			return ErrInvalidPing
		}
		payload, err := json.Marshal(Ping{Key: key})
		if err != nil {
			return err
		}
		// Append does not return until this record's stream effect is
		// committed: the count below is guaranteed visible immediately after.
		_, err = runtime.Append(ctx, PingDepot, "", payload)
		return err
	}
	module.Count = func(ctx context.Context, key string) (uint64, error) {
		raw, _, err := store.GetState(ctx, state(choosePartition([]byte(key))), []byte(key))
		return decodeUint64(raw), err
	}
	if err := runtime.Replay(context.Background()); err != nil {
		return nil, err
	}
	return module, nil
}

// Stage5MicrobatchModule demonstrates microbatch ETL semantics over the same
// kind of event.
type Stage5MicrobatchModule struct {
	Append  func(context.Context, string) error
	Advance func(context.Context) (int, error)
	Count   func(context.Context, string) (uint64, error)
}

func NewStage5Microbatch(store *storage.Store, taskCount uint32) (*Stage5MicrobatchModule, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	state := func(task uint32) storage.StatePartition {
		return storage.StatePartition{Module: Stage5ModuleName, State: MicrobatchCountState, Partition: task}
	}
	runtime, err := microbatch.New(Stage5ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: MicrobatchTopic,
			Sources: []microbatch.Source{
				{
					Depot: MicrobatchPingDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var p Ping
						if err := json.Unmarshal(payload, &p); err != nil {
							return nil, err
						}
						return []byte(p.Key), nil
					},
				},
			},
			Handle: func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
				deltas := make(map[string]uint64)
				order := make([]string, 0)
				for _, item := range items {
					var p Ping
					if err := json.Unmarshal(item.Record.Payload, &p); err != nil {
						return err
					}
					if p.Key == "" {
						return ErrInvalidPing
					}
					if _, ok := deltas[p.Key]; !ok {
						order = append(order, p.Key)
					}
					deltas[p.Key]++
				}
				for _, key := range order {
					task := choosePartition([]byte(key))
					raw, _, err := event.Get(ctx, state(task), []byte(key))
					if err != nil {
						return err
					}
					event.Set(state(task), []byte(key), partition.Uint64Key(decodeUint64(raw)+deltas[key]))
				}
				return nil
			},
		},
	)
	if err != nil {
		return nil, err
	}
	module := &Stage5MicrobatchModule{}
	module.Append = func(ctx context.Context, key string) error {
		if key == "" {
			return ErrInvalidPing
		}
		payload, err := json.Marshal(Ping{Key: key})
		if err != nil {
			return err
		}
		// Append only durably logs the record; it does NOT wait for batch
		// processing, unlike the stream module above.
		_, err = runtime.Append(ctx, MicrobatchPingDepot, "", payload)
		return err
	}
	module.Advance = func(ctx context.Context) (int, error) { return runtime.Advance(ctx, MicrobatchTopic) }
	module.Count = func(ctx context.Context, key string) (uint64, error) {
		raw, _, err := store.GetState(ctx, state(choosePartition([]byte(key))), []byte(key))
		return decodeUint64(raw), err
	}
	return module, nil
}
