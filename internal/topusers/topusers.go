package topusers

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
)

const (
	ModuleName       storage.ModuleID   = "top-users"
	PurchaseDepot    storage.DepotID    = "purchase"
	UserTotalSpend   storage.StateID    = "user-total-spend"
	TopSpendingUsers storage.StateID    = "top-spending-users"
	TopUsersTopology storage.TopologyID = "topusers"
)

var ErrInvalidPurchase = errors.New("user id and positive purchase amount are required")

type Purchase struct {
	UserID        uint64 `json:"userId"`
	PurchaseCents int    `json:"purchaseCents"`
}

type SpendingUser struct {
	UserID uint64 `json:"userId"`
	Total  int64  `json:"total"`
}

type Module struct {
	Append              func(context.Context, Purchase) error
	Advance             func(context.Context) (int, error)
	AdvanceAll          func(context.Context) (int, error)
	Rebuild             func(context.Context) error
	GetTopSpendingUsers func(context.Context) ([]SpendingUser, error)
	GetTopUserIDs       func(context.Context) ([]uint64, error)
}

func New(store *storage.Store, taskCount uint32, topAmount int) (*Module, error) {
	if topAmount <= 0 {
		return nil, errors.New("top amount must be positive")
	}
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	totalState := func(task uint32) storage.StatePartition {
		return storage.StatePartition{Module: ModuleName, State: UserTotalSpend, Partition: task}
	}
	topState := storage.StatePartition{Module: ModuleName, State: TopSpendingUsers, Partition: 0}

	runtime, err := microbatch.New(ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: TopUsersTopology,
			Sources: []microbatch.Source{
				{
					Depot: PurchaseDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var p Purchase
						if err := json.Unmarshal(payload, &p); err != nil {
							return nil, err
						}
						return partition.Uint64Key(p.UserID), nil
					},
				},
			},
			Handle: func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
				totals := make(map[uint64]int64)
				for task := uint32(0); task < taskCount; task++ {
					state := totalState(task)
					entries, err := event.Scan(ctx, state, nil, nil, 1<<20, false)
					if err != nil {
						return err
					}
					for _, entry := range entries {
						if len(entry.Key) != 8 {
							continue
						}
						userID := binary.BigEndian.Uint64(entry.Key)
						totals[userID] = int64(binary.BigEndian.Uint64(entry.Value))
					}
				}
				updated := make(map[uint64]bool)
				for _, item := range items {
					var p Purchase
					if err := json.Unmarshal(item.Record.Payload, &p); err != nil {
						return err
					}
					if p.PurchaseCents <= 0 {
						return ErrInvalidPurchase
					}
					totals[p.UserID] += int64(p.PurchaseCents)
					updated[p.UserID] = true
				}
				for userID := range updated {
					task := choosePartition(partition.Uint64Key(userID))
					event.Set(totalState(task), partition.Uint64Key(userID), binary.BigEndian.AppendUint64(nil, uint64(totals[userID])))
				}
				ranked := make([]SpendingUser, 0, len(totals))
				for userID, total := range totals {
					ranked = append(ranked, SpendingUser{UserID: userID, Total: total})
				}
				sort.SliceStable(ranked, func(i, j int) bool {
					if ranked[i].Total != ranked[j].Total {
						return ranked[i].Total > ranked[j].Total
					}
					return ranked[i].UserID < ranked[j].UserID
				})
				if len(ranked) > topAmount {
					ranked = ranked[:topAmount]
				}
				encoded, err := json.Marshal(ranked)
				if err != nil {
					return err
				}
				event.Set(topState, []byte("list"), encoded)
				return nil
			},
		})
	if err != nil {
		return nil, err
	}

	module := &Module{}
	module.Append = func(ctx context.Context, p Purchase) error {
		if p.PurchaseCents <= 0 {
			return ErrInvalidPurchase
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, PurchaseDepot, "", payload)
		return err
	}
	module.Advance = func(ctx context.Context) (int, error) { return runtime.Advance(ctx, TopUsersTopology) }
	module.AdvanceAll = func(ctx context.Context) (int, error) { return runtime.AdvanceAll(ctx) }
	module.Rebuild = runtime.Rebuild
	module.GetTopSpendingUsers = func(ctx context.Context) ([]SpendingUser, error) {
		value, found, err := store.GetState(ctx, topState, []byte("list"))
		if err != nil {
			return nil, err
		}
		if !found {
			return []SpendingUser{}, nil
		}
		var list []SpendingUser
		if err := json.Unmarshal(value, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	module.GetTopUserIDs = func(ctx context.Context) ([]uint64, error) {
		list, err := module.GetTopSpendingUsers(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]uint64, len(list))
		for i, u := range list {
			ids[i] = u.UserID
		}
		return ids, nil
	}
	return module, nil
}
