package microbatch

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
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

func TestSchedulerDefaultWaitAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler, err := NewScheduler(time.Hour, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
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

func TestRuntimePebbleCrashBoundaryReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store")
	newRuntime := func(store *storage.Store) *Runtime {
		choose, _ := partition.New(1)
		state := storage.StatePartition{Module: "module", State: "count", Partition: 0}
		topology := Topology{
			Name:    "count",
			Sources: []Source{{Depot: "events", PartitionKey: func([]byte) ([]byte, error) { return []byte("key"), nil }}},
			Handle: func(_ context.Context, event *Event, items []Item) error {
				value, _, err := event.Get(ctx, state, []byte("count"))
				if err != nil {
					return err
				}
				event.Set(state, []byte("count"), binary.BigEndian.AppendUint64(nil, decode(value)+uint64(len(items))))
				return nil
			},
		}
		runtime, err := New("module", 1, choose, store, topology)
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}

	store, err := storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntime(store)
	for i := range 3 {
		record, err := runtime.Append(ctx, "events", fmt.Sprintf("r%d", i), []byte("event"))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if record.Position != uint64(i+1) {
			t.Fatalf("append %d position = %d", i, record.Position)
		}
	}
	count, err := runtime.Advance(ctx, "count")
	if err != nil || count != 3 {
		t.Fatalf("advance before crash: count=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime = newRuntime(store)

	// Replaying an acknowledged id is a duplicate.
	dup, err := runtime.Append(ctx, "events", "r0", []byte("event"))
	if err != nil {
		t.Fatalf("replayed append: %v", err)
	}
	if dup.Position != 1 {
		t.Fatalf("replayed append position = %d", dup.Position)
	}

	for i := 3; i < 5; i++ {
		if _, err := runtime.Append(ctx, "events", fmt.Sprintf("r%d", i), []byte("event")); err != nil {
			t.Fatalf("append after reopen %d: %v", i, err)
		}
	}
	count, err = runtime.Advance(ctx, "count")
	if err != nil || count != 2 {
		t.Fatalf("advance after reopen: count=%d err=%v", count, err)
	}
	count, err = runtime.Advance(ctx, "count")
	if err != nil || count != 0 {
		t.Fatalf("second advance after reopen: count=%d err=%v", count, err)
	}

	value, found, err := store.GetState(ctx, storage.StatePartition{Module: "module", State: "count", Partition: 0}, []byte("count"))
	if err != nil || !found || decode(value) != 5 {
		t.Fatalf("count state = %d, found=%v err=%v", decode(value), found, err)
	}
}

func TestRuntimePebbleSnapshotReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "source")
	destination := filepath.Join(root, "snapshot")

	newRuntime := func(store *storage.Store) *Runtime {
		choose, _ := partition.New(1)
		state := storage.StatePartition{Module: "module", State: "count", Partition: 0}
		topology := Topology{
			Name:    "count",
			Sources: []Source{{Depot: "events", PartitionKey: func([]byte) ([]byte, error) { return []byte("key"), nil }}},
			Handle: func(_ context.Context, event *Event, items []Item) error {
				value, _, err := event.Get(ctx, state, []byte("count"))
				if err != nil {
					return err
				}
				event.Set(state, []byte("count"), binary.BigEndian.AppendUint64(nil, decode(value)+uint64(len(items))))
				return nil
			},
		}
		runtime, err := New("module", 1, choose, store, topology)
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}

	store, err := storage.NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntime(store)
	for i := range 3 {
		if _, err := runtime.Append(ctx, "events", fmt.Sprintf("r%d", i), []byte("event")); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	count, err := runtime.Advance(ctx, "count")
	if err != nil || count != 3 {
		t.Fatalf("advance before snapshot: count=%d err=%v", count, err)
	}
	if err := store.Snapshot(ctx, destination); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for i := 3; i < 5; i++ {
		if _, err := runtime.Append(ctx, "events", fmt.Sprintf("r%d", i), []byte("event")); err != nil {
			t.Fatalf("append after snapshot %d: %v", i, err)
		}
	}
	if _, err := runtime.Advance(ctx, "count"); err != nil {
		t.Fatalf("advance after snapshot: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := storage.NewPebble(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	runtime = newRuntime(snapshot)

	// Snapshot must contain the first committed batch and not the later records.
	value, found, err := snapshot.GetState(ctx, storage.StatePartition{Module: "module", State: "count", Partition: 0}, []byte("count"))
	if err != nil || !found || decode(value) != 3 {
		t.Fatalf("snapshot state = %d, found=%v err=%v", decode(value), found, err)
	}
	records, err := snapshot.Read(ctx, storage.DepotPartition{Module: "module", Depot: "events", Partition: 0}, 1, 10)
	if err != nil || len(records) != 3 {
		t.Fatalf("snapshot records = %d, %v", len(records), err)
	}

	// The snapshot can continue processing independently.
	for i := 5; i < 7; i++ {
		if _, err := runtime.Append(ctx, "events", fmt.Sprintf("r%d", i), []byte("event")); err != nil {
			t.Fatalf("append after reopen %d: %v", i, err)
		}
	}
	count, err = runtime.Advance(ctx, "count")
	if err != nil || count != 2 {
		t.Fatalf("advance snapshot after reopen: count=%d err=%v", count, err)
	}
	value, found, err = snapshot.GetState(ctx, storage.StatePartition{Module: "module", State: "count", Partition: 0}, []byte("count"))
	if err != nil || !found || decode(value) != 5 {
		t.Fatalf("snapshot state after continue = %d, found=%v err=%v", decode(value), found, err)
	}
}
