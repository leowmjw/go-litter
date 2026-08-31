package tutorial

import (
	"context"
	"encoding/json"
	"errors"

	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

// Stage 3 - Distributed Programming.
//
// A module is split into a fixed power-of-two number of logical tasks
// (internal/partition.New). Every task owns one partition of every depot and
// PState. Records on one depot partition preserve append order; there is no
// global ordering across partitions, so causally related updates for the
// same logical entity must share a partition key. A handler may still write
// to a *different* task's PState within the same atomic commit
// ("repartitioning"/"co-location") to route a cross-entity effect to the
// task that owns it, exactly as internal/ramaspace does for bidirectional
// friend requests.

const (
	Stage3ModuleName storage.ModuleID   = "tutorial-stage3"
	MailDepot        storage.DepotID    = "mail"
	MailTopic        storage.TopologyID = "mail"
	MailboxState     storage.StateID    = "mailbox" // map[recipient]list[sender messages], repartitioned by recipient
	SentState        storage.StateID    = "sent"    // map[sender]count, local to the sender's own task
)

var ErrInvalidMail = errors.New("sender, recipient, and message are required")

// Mail is the depot event; the depot is partitioned by Sender, so a given
// sender's mail is always processed in the order it was appended, but two
// different senders' mail may interleave arbitrarily across tasks.
type Mail struct {
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type Stage3Module struct {
	Send      func(context.Context, Mail) error
	SentCount func(context.Context, string) (uint64, error)
	Inbox     func(context.Context, string) ([]string, error)
	TaskOf    func(string) uint32
	Replay    func(context.Context) error
}

func NewStage3(store *storage.Store, taskCount uint32) (*Stage3Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	state := func(name storage.StateID, task uint32) storage.StatePartition {
		return storage.StatePartition{Module: Stage3ModuleName, State: name, Partition: task}
	}
	runtime, err := stream.New(Stage3ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: MailDepot, Topology: MailTopic,
			// Partitioning by Sender means the depot only guarantees causal
			// order for a single sender's mail; it does NOT co-locate mail
			// with its recipient's inbox.
			PartitionKey: func(payload []byte) ([]byte, error) {
				var m Mail
				if err := json.Unmarshal(payload, &m); err != nil {
					return nil, err
				}
				return []byte(m.Sender), nil
			},
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, senderTask uint32) ([]byte, error) {
				var m Mail
				if err := json.Unmarshal(record.Payload, &m); err != nil {
					return nil, err
				}
				if m.Sender == "" || m.Recipient == "" || m.Message == "" {
					return nil, ErrInvalidMail
				}
				// Local write: stays on the sender's own task.
				countRaw, _, err := event.Get(ctx, state(SentState, senderTask), []byte(m.Sender))
				if err != nil {
					return nil, err
				}
				event.Set(state(SentState, senderTask), []byte(m.Sender), partition.Uint64Key(decodeUint64(countRaw)+1))

				// Repartitioning: this handler runs on the sender's task, but the
				// mailbox effect belongs to the recipient's task. The mutation
				// targets the recipient's partition directly; the storage layer
				// commits the whole batch (both partitions) atomically.
				recipientTask := choosePartition([]byte(m.Recipient))
				seqRaw, _, err := event.Get(ctx, state(MailboxState, recipientTask), []byte(m.Recipient+"\x00seq"))
				if err != nil {
					return nil, err
				}
				seq := decodeUint64(seqRaw) + 1
				event.Set(state(MailboxState, recipientTask), []byte(m.Recipient+"\x00seq"), partition.Uint64Key(seq))
				msgKey := append([]byte(m.Recipient+"\x00msg"), partition.Uint64Key(seq)...)
				event.Set(state(MailboxState, recipientTask), msgKey, []byte(m.Sender+": "+m.Message))
				return nil, nil
			},
		},
	)
	if err != nil {
		return nil, err
	}
	module := &Stage3Module{Replay: runtime.Replay, TaskOf: func(key string) uint32 { return choosePartition([]byte(key)) }}
	module.Send = func(ctx context.Context, m Mail) error {
		if m.Sender == "" || m.Recipient == "" || m.Message == "" {
			return ErrInvalidMail
		}
		payload, err := json.Marshal(m)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, MailDepot, "", payload)
		return err
	}
	module.SentCount = func(ctx context.Context, sender string) (uint64, error) {
		raw, _, err := store.GetState(ctx, state(SentState, choosePartition([]byte(sender))), []byte(sender))
		return decodeUint64(raw), err
	}
	module.Inbox = func(ctx context.Context, recipient string) ([]string, error) {
		prefix := []byte(recipient + "\x00msg")
		end := append([]byte(nil), prefix...)
		end = append(end, 0xff)
		entries, err := store.ScanState(ctx, state(MailboxState, choosePartition([]byte(recipient))), prefix, end, 1<<10, false)
		if err != nil {
			return nil, err
		}
		messages := make([]string, len(entries))
		for i, entry := range entries {
			messages[i] = string(entry.Value)
		}
		return messages, nil
	}
	if err := module.Replay(context.Background()); err != nil {
		return nil, err
	}
	return module, nil
}
