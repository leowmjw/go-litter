package tutorial

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"

	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

// Stage 2 - Depots, ETLs, and PStates.
//
// Depots are append-only sources of truth (event sourcing); PStates are
// typed, partitioned materialized views derived from depots by ETLs, and
// ETLs are the only writers to their PStates. This stage's single ETL
// derives five differently-shaped PStates from one depot to demonstrate
// scalar, map, set, list, and fixed-key-record schemas, plus point/range
// queries, a server-side transform (sum), and PState rebuild by replay.

const (
	Stage2ModuleName storage.ModuleID   = "tutorial-stage2"
	ActivityDepot    storage.DepotID    = "activity"
	ActivityTopic    storage.TopologyID = "activity"

	TotalCountState storage.StateID = "total-count" // scalar: single counter
	UserScoreState  storage.StateID = "user-score"  // map[user]score
	UserTagsState   storage.StateID = "user-tags"   // map[user]sorted-set[tag]
	UserEventsState storage.StateID = "user-events" // map[user]ordered-list[event]
	UserRecordState storage.StateID = "user-record" // map[user]fixed-key-record
)

var ErrInvalidActivity = errors.New("user, tag, and positive score are required")

// Activity is the single depot event driving all five PStates.
type Activity struct {
	User  string `json:"user"`
	Tag   string `json:"tag"`
	Score int64  `json:"score"`
}

// UserRecord is a fixed-key record: named fields with a stable schema,
// stored as one value per key rather than as separate scalar entries.
type UserRecord struct {
	User       string `json:"user"`
	EventCount int    `json:"eventCount"`
	LastTag    string `json:"lastTag"`
	HighScore  int64  `json:"highScore"`
}

type Stage2Module struct {
	Record          func(context.Context, Activity) error
	TotalCount      func(context.Context) (uint64, error)
	UserScore       func(context.Context, string) (int64, bool, error)
	UserTags        func(context.Context, string) ([]string, error)
	UserEvents      func(context.Context, string) ([]string, error)
	UserRecord      func(context.Context, string) (UserRecord, bool, error)
	SumScoresForTag func(context.Context, string, []string) (int64, error)
	Replay          func(context.Context) error
	Rebuild         func(context.Context) error
}

func NewStage2(store *storage.Store, taskCount uint32) (*Stage2Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	state := func(name storage.StateID, task uint32) storage.StatePartition {
		return storage.StatePartition{Module: Stage2ModuleName, State: name, Partition: task}
	}
	tagKey := func(user, tag string) []byte {
		buf := binary.BigEndian.AppendUint16(nil, uint16(len(user)))
		buf = append(buf, user...)
		buf = append(buf, tag...)
		return buf
	}
	runtime, err := stream.New(Stage2ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: ActivityDepot, Topology: ActivityTopic,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var a Activity
				if err := json.Unmarshal(payload, &a); err != nil {
					return nil, err
				}
				return []byte(a.User), nil
			},
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				var a Activity
				if err := json.Unmarshal(record.Payload, &a); err != nil {
					return nil, err
				}
				if a.User == "" || a.Tag == "" || a.Score <= 0 {
					return nil, ErrInvalidActivity
				}
				// Scalar: single global counter.
				totalRaw, _, err := event.Get(ctx, state(TotalCountState, 0), []byte("total"))
				if err != nil {
					return nil, err
				}
				event.Set(state(TotalCountState, 0), []byte("total"), partition.Uint64Key(decodeUint64(totalRaw)+1))

				// Map: last score per user.
				event.Set(state(UserScoreState, task), []byte(a.User), partition.Uint64Key(uint64(a.Score)))

				// Sorted set (subindexed map[user]set[tag]): presence-keyed.
				event.Set(state(UserTagsState, task), tagKey(a.User, a.Tag), []byte{1})

				// Ordered list (subindexed map[user]list[event]): append-only via a
				// task-local monotonic sequence per user, encoded so scan order
				// matches append order.
				seqRaw, _, err := event.Get(ctx, state(UserEventsState, task), []byte(a.User+"\x00seq"))
				if err != nil {
					return nil, err
				}
				seq := decodeUint64(seqRaw) + 1
				event.Set(state(UserEventsState, task), []byte(a.User+"\x00seq"), partition.Uint64Key(seq))
				eventKey := append(tagKey(a.User, ""), partition.Uint64Key(seq)...)
				event.Set(state(UserEventsState, task), eventKey, []byte(a.Tag))

				// Fixed-key record: several named fields under one key.
				recordRaw, found, err := event.Get(ctx, state(UserRecordState, task), []byte(a.User))
				if err != nil {
					return nil, err
				}
				var rec UserRecord
				if found {
					if err := json.Unmarshal(recordRaw, &rec); err != nil {
						return nil, err
					}
				} else {
					rec.User = a.User
				}
				rec.EventCount++
				rec.LastTag = a.Tag
				if a.Score > rec.HighScore {
					rec.HighScore = a.Score
				}
				encodedRec, err := json.Marshal(rec)
				if err != nil {
					return nil, err
				}
				event.Set(state(UserRecordState, task), []byte(a.User), encodedRec)
				return nil, nil
			},
		},
	)
	if err != nil {
		return nil, err
	}

	module := &Stage2Module{Replay: runtime.Replay, Rebuild: runtime.Rebuild}
	module.Record = func(ctx context.Context, a Activity) error {
		if a.User == "" || a.Tag == "" || a.Score <= 0 {
			return ErrInvalidActivity
		}
		payload, err := json.Marshal(a)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, ActivityDepot, "", payload)
		return err
	}
	module.TotalCount = func(ctx context.Context) (uint64, error) {
		raw, _, err := store.GetState(ctx, state(TotalCountState, 0), []byte("total"))
		return decodeUint64(raw), err
	}
	module.UserScore = func(ctx context.Context, user string) (int64, bool, error) {
		raw, found, err := store.GetState(ctx, state(UserScoreState, choosePartition([]byte(user))), []byte(user))
		return int64(decodeUint64(raw)), found, err
	}
	module.UserTags = func(ctx context.Context, user string) ([]string, error) {
		prefix := tagKey(user, "")
		end := append([]byte(nil), prefix...)
		end = append(end, 0xff)
		entries, err := store.ScanState(ctx, state(UserTagsState, choosePartition([]byte(user))), prefix, end, 1<<10, false)
		if err != nil {
			return nil, err
		}
		tags := make([]string, 0, len(entries))
		for _, entry := range entries {
			tags = append(tags, string(entry.Key[len(prefix):]))
		}
		return tags, nil
	}
	module.UserEvents = func(ctx context.Context, user string) ([]string, error) {
		prefix := tagKey(user, "")
		end := append([]byte(nil), prefix...)
		end = append(end, 0xff)
		entries, err := store.ScanState(ctx, state(UserEventsState, choosePartition([]byte(user))), prefix, end, 1<<10, false)
		if err != nil {
			return nil, err
		}
		events := make([]string, 0, len(entries))
		for _, entry := range entries {
			events = append(events, string(entry.Value))
		}
		return events, nil
	}
	module.UserRecord = func(ctx context.Context, user string) (UserRecord, bool, error) {
		raw, found, err := store.GetState(ctx, state(UserRecordState, choosePartition([]byte(user))), []byte(user))
		if err != nil || !found {
			return UserRecord{}, found, err
		}
		var rec UserRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return UserRecord{}, false, err
		}
		return rec, true, nil
	}
	// SumScoresForTag is a server-side transform/reduction: it scans only the
	// bounded set of requested users rather than the whole PState.
	module.SumScoresForTag = func(ctx context.Context, tag string, users []string) (int64, error) {
		var sum int64
		for _, user := range users {
			tags, err := module.UserTags(ctx, user)
			if err != nil {
				return 0, err
			}
			hasTag := false
			for _, tagged := range tags {
				if tagged == tag {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
			score, found, err := module.UserScore(ctx, user)
			if err != nil {
				return 0, err
			}
			if found {
				sum += score
			}
		}
		return sum, nil
	}
	if err := module.Replay(context.Background()); err != nil {
		return nil, err
	}
	return module, nil
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
