package microbatch

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"app/internal/partition"
	"app/internal/storage"
)

func TestRuntimeExactlyOnceAndRebuild(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(4)
	state := func(task uint32) storage.StatePartition {
		return storage.StatePartition{Module: "module", State: "counts", Partition: task}
	}
	topology := Topology{Name: "count", Sources: []Source{{Depot: "events", PartitionKey: func(payload []byte) ([]byte, error) { return payload, nil }}}}
	topology.Handle = func(ctx context.Context, event *Event, items []Item) error {
		for _, item := range items {
			value, _, err := event.Get(ctx, state(item.Partition), []byte("count"))
			if err != nil {
				return err
			}
			event.Set(state(item.Partition), []byte("count"), binary.BigEndian.AppendUint64(nil, decode(value)+1))
		}
		return nil
	}
	runtime, err := New("module", 4, choose, store, topology)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range [][]byte{[]byte("a"), []byte("b"), []byte("a")} {
		if _, err := runtime.Append(ctx, "events", string(rune('a'+i)), key); err != nil {
			t.Fatal(err)
		}
	}
	for task := range uint32(4) {
		if _, found, err := store.GetState(ctx, state(task), []byte("count")); err != nil || found {
			t.Fatalf("state visible before advance task=%d found=%v err=%v", task, found, err)
		}
	}
	count, err := runtime.Advance(ctx, "count")
	if err != nil || count != 3 {
		t.Fatalf("advance count=%d err=%v", count, err)
	}
	count, err = runtime.Advance(ctx, "count")
	if err != nil || count != 0 {
		t.Fatalf("second advance count=%d err=%v", count, err)
	}
	if got := stateTotal(t, store, state, 4); got != 3 {
		t.Fatalf("state total=%d", got)
	}
	if err := runtime.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if got := stateTotal(t, store, state, 4); got != 3 {
		t.Fatalf("rebuilt total=%d", got)
	}
}

func TestRuntimeCommitFailures(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(1)
	state := storage.StatePartition{Module: "module", State: "count", Partition: 0}
	topology := Topology{Name: "count", Sources: []Source{{Depot: "events", PartitionKey: func([]byte) ([]byte, error) { return nil, nil }}}}
	topology.Handle = func(ctx context.Context, event *Event, items []Item) error {
		value, _, err := event.Get(ctx, state, []byte("count"))
		if err != nil {
			return err
		}
		event.Set(state, []byte("count"), binary.BigEndian.AppendUint64(nil, decode(value)+uint64(len(items))))
		return nil
	}
	runtime, err := New("module", 1, choose, store, topology)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Append(ctx, "events", "one", []byte("event")); err != nil {
		t.Fatal(err)
	}
	originalCommit := store.Commit
	injected := errors.New("injected")
	store.Commit = func(context.Context, storage.CommitRequest) (bool, error) { return false, injected }
	if _, err := runtime.Advance(ctx, "count"); !errors.Is(err, injected) {
		t.Fatalf("commit error=%v", err)
	}
	if _, found, _ := store.GetState(ctx, state, []byte("count")); found {
		t.Fatal("failed commit changed state")
	}
	store.Commit = func(ctx context.Context, request storage.CommitRequest) (bool, error) {
		_, err := originalCommit(ctx, request)
		if err != nil {
			return false, err
		}
		return false, injected
	}
	if _, err := runtime.Advance(ctx, "count"); !errors.Is(err, injected) {
		t.Fatalf("post-commit error=%v", err)
	}
	store.Commit = originalCommit
	count, err := runtime.Advance(ctx, "count")
	if err != nil || count != 0 {
		t.Fatalf("retry count=%d err=%v", count, err)
	}
	value, found, err := store.GetState(ctx, state, []byte("count"))
	if err != nil || !found || decode(value) != 1 {
		t.Fatalf("state=%d found=%v err=%v", decode(value), found, err)
	}
}

func TestRuntimeValidationAndScheduler(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(1)
	source := Source{Depot: "events", PartitionKey: func([]byte) ([]byte, error) { return nil, nil }}
	topology := Topology{Name: "topology", Sources: []Source{source}, Handle: func(context.Context, *Event, []Item) error { return nil }}
	if _, err := New("module", 0, choose, store, topology); !errors.Is(err, ErrTaskCount) {
		t.Fatalf("task count error=%v", err)
	}
	if _, err := New("module", 1, choose, store, topology, topology); !errors.Is(err, ErrTopologyExists) {
		t.Fatalf("topology error=%v", err)
	}
	second := Topology{Name: "other", Sources: []Source{source}, Handle: topology.Handle}
	if _, err := New("module", 1, choose, store, topology, second); !errors.Is(err, ErrSourceExists) {
		t.Fatalf("source error=%v", err)
	}
	runtime, err := New("module", 1, choose, store, topology)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Append(context.Background(), "missing", "", nil); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing source error=%v", err)
	}
	if _, err := runtime.Advance(context.Background(), "missing"); !errors.Is(err, ErrTopologyMissing) {
		t.Fatalf("missing topology error=%v", err)
	}
	if _, err := NewScheduler(0, func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("expected interval error")
	}
	stop := errors.New("stop")
	waits := 0
	advances := 0
	scheduler, err := NewScheduler(time.Second, func(context.Context) error { advances++; return nil }, func(context.Context, time.Duration) error {
		waits++
		if waits == 3 {
			return stop
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(context.Background()); !errors.Is(err, stop) || advances != 2 {
		t.Fatalf("scheduler err=%v advances=%d", err, advances)
	}
}

func stateTotal(t *testing.T, store *storage.Store, state func(uint32) storage.StatePartition, tasks uint32) uint64 {
	t.Helper()
	var total uint64
	for task := range tasks {
		value, found, err := store.GetState(context.Background(), state(task), []byte("count"))
		if err != nil {
			t.Fatal(err)
		}
		if found {
			total += decode(value)
		}
	}
	return total
}

func decode(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
