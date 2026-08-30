package banktransfer

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
	ModuleName      storage.ModuleID   = "bank-transfer"
	TransferDepot   storage.DepotID    = "transfer"
	DepositDepot    storage.DepotID    = "deposit"
	FundsState      storage.StateID    = "funds"
	OutgoingState   storage.StateID    = "outgoing-transfers"
	IncomingState   storage.StateID    = "incoming-transfers"
	BankingTopology storage.TopologyID = "banking"
)

var (
	ErrInvalidDeposit  = errors.New("positive deposit amount is required")
	ErrInvalidTransfer = errors.New("transfer id, from/to users, and positive amount are required")
)

type Deposit struct {
	UserID uint64 `json:"userId"`
	Amt    int    `json:"amt"`
}

type Transfer struct {
	TransferID string `json:"transferId"`
	FromUserID uint64 `json:"fromUserId"`
	ToUserID   uint64 `json:"toUserId"`
	Amt        int    `json:"amt"`
}

type OutgoingRecord struct {
	ToUserID  uint64 `json:"toUserId"`
	Amt       int    `json:"amt"`
	IsSuccess bool   `json:"isSuccess"`
}

type IncomingRecord struct {
	FromUserID uint64 `json:"fromUserId"`
	Amt        int    `json:"amt"`
	IsSuccess  bool   `json:"isSuccess"`
}

type Module struct {
	AppendDeposit        func(context.Context, Deposit) error
	AppendTransfer       func(context.Context, Transfer) error
	Advance              func(context.Context) (int, error)
	AdvanceAll           func(context.Context) (int, error)
	Rebuild              func(context.Context) error
	GetFunds             func(context.Context, uint64) (int, error)
	GetOutgoingTransfers func(context.Context, uint64) (map[string]OutgoingRecord, error)
	GetIncomingTransfers func(context.Context, uint64) (map[string]IncomingRecord, error)
}

func New(store *storage.Store, taskCount uint32) (*Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	fundsState := func(userID uint64) storage.StatePartition {
		return storage.StatePartition{Module: ModuleName, State: FundsState, Partition: choosePartition(partition.Uint64Key(userID))}
	}
	outgoingState := func(userID uint64) storage.StatePartition {
		return storage.StatePartition{Module: ModuleName, State: OutgoingState, Partition: choosePartition(partition.Uint64Key(userID))}
	}
	incomingState := func(userID uint64) storage.StatePartition {
		return storage.StatePartition{Module: ModuleName, State: IncomingState, Partition: choosePartition(partition.Uint64Key(userID))}
	}

	runtime, err := microbatch.New(ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: BankingTopology,
			Sources: []microbatch.Source{
				{
					Depot: DepositDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var d Deposit
						if err := json.Unmarshal(payload, &d); err != nil {
							return nil, err
						}
						return partition.Uint64Key(d.UserID), nil
					},
				},
				{
					Depot: TransferDepot,
					PartitionKey: func(payload []byte) ([]byte, error) {
						var tr Transfer
						if err := json.Unmarshal(payload, &tr); err != nil {
							return nil, err
						}
						return partition.Uint64Key(tr.FromUserID), nil
					},
				},
			},
			Handle: func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
				depotOrder := map[storage.DepotID]int{DepositDepot: 0, TransferDepot: 1}
				sort.SliceStable(items, func(i, j int) bool {
					return depotOrder[items[i].Depot] < depotOrder[items[j].Depot]
				})
				for _, item := range items {
					switch item.Depot {
					case DepositDepot:
						var d Deposit
						if err := json.Unmarshal(item.Record.Payload, &d); err != nil {
							return err
						}
						if d.Amt <= 0 {
							return ErrInvalidDeposit
						}
						key := partition.Uint64Key(d.UserID)
						current, _, err := event.Get(ctx, fundsState(d.UserID), key)
						if err != nil {
							return err
						}
						funds := decodeInt64(current) + int64(d.Amt)
						event.Set(fundsState(d.UserID), key, binary.BigEndian.AppendUint64(nil, uint64(funds)))

					case TransferDepot:
						var tr Transfer
						if err := json.Unmarshal(item.Record.Payload, &tr); err != nil {
							return err
						}
						if tr.Amt <= 0 || tr.TransferID == "" || tr.FromUserID == tr.ToUserID {
							return ErrInvalidTransfer
						}
						fromKey := partition.Uint64Key(tr.FromUserID)
						fromRaw, _, err := event.Get(ctx, fundsState(tr.FromUserID), fromKey)
						if err != nil {
							return err
						}
						fromFunds := decodeInt64(fromRaw)
						isSuccess := fromFunds >= int64(tr.Amt)
						if isSuccess {
							event.Set(fundsState(tr.FromUserID), fromKey, binary.BigEndian.AppendUint64(nil, uint64(fromFunds-int64(tr.Amt))))
						}
						toKey := partition.Uint64Key(tr.ToUserID)
						toRaw, _, err := event.Get(ctx, fundsState(tr.ToUserID), toKey)
						if err != nil {
							return err
						}
						toFunds := decodeInt64(toRaw)
						if isSuccess {
							event.Set(fundsState(tr.ToUserID), toKey, binary.BigEndian.AppendUint64(nil, uint64(toFunds+int64(tr.Amt))))
						}
						outRecord := OutgoingRecord{ToUserID: tr.ToUserID, Amt: tr.Amt, IsSuccess: isSuccess}
						outEncoded, err := json.Marshal(outRecord)
						if err != nil {
							return err
						}
						outKey := make([]byte, 0, len(fromKey)+1+len(tr.TransferID))
						outKey = append(outKey, fromKey...)
						outKey = append(outKey, 0)
						outKey = append(outKey, []byte(tr.TransferID)...)
						event.Set(outgoingState(tr.FromUserID), outKey, outEncoded)
						inRecord := IncomingRecord{FromUserID: tr.FromUserID, Amt: tr.Amt, IsSuccess: isSuccess}
						inEncoded, err := json.Marshal(inRecord)
						if err != nil {
							return err
						}
						inKey := make([]byte, 0, len(toKey)+1+len(tr.TransferID))
						inKey = append(inKey, toKey...)
						inKey = append(inKey, 0)
						inKey = append(inKey, []byte(tr.TransferID)...)
						event.Set(incomingState(tr.ToUserID), inKey, inEncoded)
					}
				}
				return nil
			},
		})
	if err != nil {
		return nil, err
	}

	module := &Module{}
	module.AppendDeposit = func(ctx context.Context, d Deposit) error {
		if d.Amt <= 0 {
			return ErrInvalidDeposit
		}
		payload, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, DepositDepot, "", payload)
		return err
	}
	module.AppendTransfer = func(ctx context.Context, tr Transfer) error {
		if tr.Amt <= 0 || tr.TransferID == "" || tr.FromUserID == tr.ToUserID {
			return ErrInvalidTransfer
		}
		payload, err := json.Marshal(tr)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, TransferDepot, "", payload)
		return err
	}
	module.Advance = func(ctx context.Context) (int, error) { return runtime.Advance(ctx, BankingTopology) }
	module.AdvanceAll = func(ctx context.Context) (int, error) { return runtime.AdvanceAll(ctx) }
	module.Rebuild = runtime.Rebuild
	module.GetFunds = func(ctx context.Context, userID uint64) (int, error) {
		value, found, err := store.GetState(ctx, fundsState(userID), partition.Uint64Key(userID))
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, nil
		}
		return int(decodeInt64(value)), nil
	}
	module.GetOutgoingTransfers = func(ctx context.Context, userID uint64) (map[string]OutgoingRecord, error) {
		return scanUserRecords(ctx, store, userID, outgoingState(userID), func(encoded []byte) (OutgoingRecord, error) {
			var r OutgoingRecord
			err := json.Unmarshal(encoded, &r)
			return r, err
		})
	}
	module.GetIncomingTransfers = func(ctx context.Context, userID uint64) (map[string]IncomingRecord, error) {
		return scanUserRecords(ctx, store, userID, incomingState(userID), func(encoded []byte) (IncomingRecord, error) {
			var r IncomingRecord
			err := json.Unmarshal(encoded, &r)
			return r, err
		})
	}
	return module, nil
}

func decodeInt64(value []byte) int64 {
	if len(value) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(value))
}

func scanUserRecords[Record any](ctx context.Context, store *storage.Store, userID uint64, state storage.StatePartition, decode func([]byte) (Record, error)) (map[string]Record, error) {
	prefix := partition.Uint64Key(userID)
	start := append(append([]byte(nil), prefix...), 0)
	end := append(append([]byte(nil), prefix...), 1)
	entries, err := store.ScanState(ctx, state, start, end, 1<<20, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string]Record, len(entries))
	for _, entry := range entries {
		if len(entry.Key) <= len(start) {
			continue
		}
		transferID := string(entry.Key[len(start):])
		record, err := decode(entry.Value)
		if err != nil {
			return nil, err
		}
		result[transferID] = record
	}
	return result, nil
}
