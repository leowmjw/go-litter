package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreConformance(t *testing.T) {
	factories := []struct {
		name string
		new  func(*testing.T) *Store
	}{
		{name: "memory", new: func(t *testing.T) *Store { return NewMemory() }},
		{name: "pebble", new: func(t *testing.T) *Store {
			store, err := NewPebble(filepath.Join(t.TempDir(), "store"))
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.new(t)
			t.Cleanup(func() { _ = store.Close() })
			testStoreConformance(t, store)
		})
	}
}

func testStoreConformance(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	depot := DepotPartition{Module: "module", Depot: "events", Partition: 2}
	first, duplicate, err := store.Append(ctx, depot, "one", []byte("first"))
	if err != nil || duplicate || first.Position != 1 {
		t.Fatalf("first append = %#v, %v, %v", first, duplicate, err)
	}
	second, duplicate, err := store.Append(ctx, depot, "two", []byte("second"))
	if err != nil || duplicate || second.Position != 2 {
		t.Fatalf("second append = %#v, %v, %v", second, duplicate, err)
	}
	again, duplicate, err := store.Append(ctx, depot, "one", []byte("first"))
	if err != nil || !duplicate || again.Position != first.Position {
		t.Fatalf("duplicate append = %#v, %v, %v", again, duplicate, err)
	}
	if _, _, err := store.Append(ctx, depot, "one", []byte("changed")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	high, err := store.HighWater(ctx, depot)
	if err != nil || high != 2 {
		t.Fatalf("high water = %d, %v", high, err)
	}
	records, err := store.Read(ctx, depot, 1, 10)
	if err != nil || !reflect.DeepEqual(records, []Record{{Position: 1, Payload: []byte("first")}, {Position: 2, Payload: []byte("second")}}) {
		t.Fatalf("records = %#v, %v", records, err)
	}
	if _, err := store.Read(ctx, depot, 1, 0); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("invalid read limit error = %v", err)
	}
	state := StatePartition{Module: "module", State: "view", Partition: 2}
	cursor := Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 2, Position: 2}
	applied, err := store.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "batch-1", Checkpoint: cursor,
		Result: []byte("ack"),
		Mutations: []Mutation{
			{State: state, Key: []byte("a"), Value: []byte("1")},
			{State: state, Key: []byte("b"), Value: []byte("2")},
			{State: state, Key: []byte("c"), Value: []byte("3")},
		},
	})
	if err != nil || !applied {
		t.Fatalf("commit = %v, %v", applied, err)
	}
	result, found, err := store.CommitResult(ctx, "module", "batch-1")
	if err != nil || !found || string(result) != "ack" {
		t.Fatalf("commit result = %q, %v, %v", result, found, err)
	}
	value, found, err := store.GetState(ctx, state, []byte("b"))
	if err != nil || !found || string(value) != "2" {
		t.Fatalf("state value = %q, %v, %v", value, found, err)
	}
	forward, err := store.ScanState(ctx, state, []byte("a"), []byte("d"), 2, false)
	if err != nil || !reflect.DeepEqual(forward, []Entry{{Key: []byte("a"), Value: []byte("1")}, {Key: []byte("b"), Value: []byte("2")}}) {
		t.Fatalf("forward scan = %#v, %v", forward, err)
	}
	reverse, err := store.ScanState(ctx, state, nil, nil, 2, true)
	if err != nil || !reflect.DeepEqual(reverse, []Entry{{Key: []byte("c"), Value: []byte("3")}, {Key: []byte("b"), Value: []byte("2")}}) {
		t.Fatalf("reverse scan = %#v, %v", reverse, err)
	}
	if _, err := store.ScanState(ctx, state, nil, nil, 0, false); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("invalid scan limit error = %v", err)
	}
	checkpoint, err := store.Checkpoint(ctx, cursor)
	if err != nil || checkpoint != 2 {
		t.Fatalf("checkpoint = %d, %v", checkpoint, err)
	}
	applied, err = store.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "batch-1", Checkpoint: Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 2, Position: 99},
		Mutations: []Mutation{{State: state, Key: []byte("b"), Value: []byte("changed")}},
	})
	if err != nil || applied {
		t.Fatalf("duplicate commit = %v, %v", applied, err)
	}
	value, _, _ = store.GetState(ctx, state, []byte("b"))
	checkpoint, _ = store.Checkpoint(ctx, cursor)
	if string(value) != "2" || checkpoint != 2 {
		t.Fatalf("duplicate changed state: value=%q checkpoint=%d", value, checkpoint)
	}
	applied, err = store.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "batch-2", Checkpoint: cursor,
		Mutations: []Mutation{{State: state, Key: []byte("b"), Delete: true}},
	})
	if err != nil || !applied {
		t.Fatalf("delete commit = %v, %v", applied, err)
	}
	if _, found, err := store.GetState(ctx, state, []byte("b")); err != nil || found {
		t.Fatalf("deleted state found=%v err=%v", found, err)
	}
	if err := store.SetMetadata(ctx, "module", []byte("config"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	metadata, found, err := store.GetMetadata(ctx, "module", []byte("config"))
	if err != nil || !found || string(metadata) != "v1" {
		t.Fatalf("metadata = %q, %v, %v", metadata, found, err)
	}
	if err := store.ResetDerived(ctx, "module"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetState(ctx, state, []byte("a")); err != nil || found {
		t.Fatalf("reset state found=%v err=%v", found, err)
	}
	records, err = store.Read(ctx, depot, 1, 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("depot after reset = %#v, %v", records, err)
	}
	metadata, found, err = store.GetMetadata(ctx, "module", []byte("config"))
	if err != nil || !found || string(metadata) != "v1" {
		t.Fatalf("metadata after reset = %q, %v, %v", metadata, found, err)
	}
}

func TestPebbleCrashBoundariesAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store")
	depot := DepotPartition{Module: "module", Depot: "events", Partition: 0}
	state := StatePartition{Module: "module", State: "view", Partition: 0}
	cursor := Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 0, Position: 1}
	store, err := NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	format, found, err := store.GetMetadata(ctx, "__storage", []byte("pebble-format"))
	if err != nil || !found || decodeUint64(format) == 0 {
		t.Fatalf("pebble format metadata = %v, %v, %v", format, found, err)
	}
	createdBy, found, err := store.GetMetadata(ctx, "__storage", []byte("pebble-created-by"))
	if err != nil || !found || string(createdBy) != "github.com/cockroachdb/pebble/v2@v2.1.6" {
		t.Fatalf("pebble library metadata = %q, %v, %v", createdBy, found, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := store.Append(canceled, depot, "canceled", []byte("not-acknowledged")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append error = %v", err)
	}
	if _, err := store.Commit(canceled, CommitRequest{
		Module: "module", BatchID: "canceled", Checkpoint: Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 0, Position: 99},
		Mutations: []Mutation{{State: state, Key: []byte("ghost"), Value: []byte("not-committed")}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit error = %v", err)
	}
	appended, duplicate, err := store.Append(ctx, depot, "request", []byte("event"))
	if err != nil || duplicate || appended.Position != 1 {
		t.Fatalf("acknowledged append = %#v, %v, %v", appended, duplicate, err)
	}
	applied, err := store.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "batch", Checkpoint: cursor, Result: []byte("result"),
		Mutations: []Mutation{{State: state, Key: []byte("key"), Value: []byte("value")}},
	})
	if err != nil || !applied {
		t.Fatalf("acknowledged commit = %v, %v", applied, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewPebble(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.Read(ctx, depot, 1, 1)
	if err != nil || len(records) != 1 || string(records[0].Payload) != "event" {
		t.Fatalf("reopened records = %#v, %v", records, err)
	}
	value, found, err := store.GetState(ctx, state, []byte("key"))
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("reopened value = %q, %v, %v", value, found, err)
	}
	checkpoint, err := store.Checkpoint(ctx, cursor)
	if err != nil || checkpoint != 1 {
		t.Fatalf("reopened checkpoint = %d, %v", checkpoint, err)
	}
	result, found, err := store.CommitResult(ctx, "module", "batch")
	if err != nil || !found || string(result) != "result" {
		t.Fatalf("reopened commit result = %q, %v, %v", result, found, err)
	}
	if _, found, err := store.GetState(ctx, state, []byte("ghost")); err != nil || found {
		t.Fatalf("canceled state found=%v err=%v", found, err)
	}
	if _, found, err := store.CommitResult(ctx, "module", "canceled"); err != nil || found {
		t.Fatalf("canceled result found=%v err=%v", found, err)
	}
	again, duplicate, err := store.Append(ctx, depot, "request", []byte("event"))
	if err != nil || !duplicate || again.Position != 1 {
		t.Fatalf("replayed append = %#v, %v, %v", again, duplicate, err)
	}
	applied, err = store.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "batch", Checkpoint: Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 0, Position: 99},
		Mutations: []Mutation{{State: state, Key: []byte("key"), Value: []byte("duplicated")}},
	})
	if err != nil || applied {
		t.Fatalf("replayed commit = %v, %v", applied, err)
	}
	value, found, err = store.GetState(ctx, state, []byte("key"))
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("state after replay = %q, %v, %v", value, found, err)
	}
	checkpoint, err = store.Checkpoint(ctx, cursor)
	if err != nil || checkpoint != 1 {
		t.Fatalf("checkpoint after replay = %d, %v", checkpoint, err)
	}
	next, duplicate, err := store.Append(ctx, depot, "next", []byte("next-event"))
	if err != nil || duplicate || next.Position != 2 {
		t.Fatalf("append after reopen = %#v, %v, %v", next, duplicate, err)
	}
}

func TestPebbleSnapshotReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source")
	destination := filepath.Join(root, "snapshot")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	depot := DepotPartition{Module: "module", Depot: "events", Partition: 3}
	state := StatePartition{Module: "module", State: "view", Partition: 3}
	cursor := Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 3, Position: 1}
	source, err := NewPebble(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Append(ctx, depot, "before", []byte("before-snapshot")); err != nil {
		t.Fatal(err)
	}
	applied, err := source.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "before", Checkpoint: cursor, Result: []byte("before-result"),
		Mutations: []Mutation{{State: state, Key: []byte("key"), Value: []byte("before-value")}},
	})
	if err != nil || !applied {
		t.Fatalf("commit before snapshot = %v, %v", applied, err)
	}
	if err := source.SetMetadata(ctx, "module", []byte("config"), []byte("before-config")); err != nil {
		t.Fatal(err)
	}
	if err := source.Snapshot(ctx, destination); err != nil {
		t.Fatal(err)
	}

	if _, _, err := source.Append(ctx, depot, "after", []byte("after-snapshot")); err != nil {
		t.Fatal(err)
	}
	applied, err = source.Commit(ctx, CommitRequest{
		Module: "module", BatchID: "after", Checkpoint: Cursor{Module: "module", Topology: "topology", Depot: "events", Partition: 3, Position: 2},
		Mutations: []Mutation{{State: state, Key: []byte("key"), Value: []byte("after-value")}},
	})
	if err != nil || !applied {
		t.Fatalf("commit after snapshot = %v, %v", applied, err)
	}
	if err := source.SetMetadata(ctx, "module", []byte("config"), []byte("after-config")); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := NewPebble(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	records, err := snapshot.Read(ctx, depot, 1, 10)
	if err != nil || !reflect.DeepEqual(records, []Record{{Position: 1, Payload: []byte("before-snapshot")}}) {
		t.Fatalf("snapshot records = %#v, %v", records, err)
	}
	value, found, err := snapshot.GetState(ctx, state, []byte("key"))
	if err != nil || !found || string(value) != "before-value" {
		t.Fatalf("snapshot state = %q, %v, %v", value, found, err)
	}
	checkpoint, err := snapshot.Checkpoint(ctx, cursor)
	if err != nil || checkpoint != 1 {
		t.Fatalf("snapshot checkpoint = %d, %v", checkpoint, err)
	}
	result, found, err := snapshot.CommitResult(ctx, "module", "before")
	if err != nil || !found || string(result) != "before-result" {
		t.Fatalf("snapshot commit result = %q, %v, %v", result, found, err)
	}
	if _, found, err := snapshot.CommitResult(ctx, "module", "after"); err != nil || found {
		t.Fatalf("post-snapshot result found=%v err=%v", found, err)
	}
	metadata, found, err := snapshot.GetMetadata(ctx, "module", []byte("config"))
	if err != nil || !found || string(metadata) != "before-config" {
		t.Fatalf("snapshot metadata = %q, %v, %v", metadata, found, err)
	}
	again, duplicate, err := snapshot.Append(ctx, depot, "before", []byte("before-snapshot"))
	if err != nil || !duplicate || again.Position != 1 {
		t.Fatalf("snapshot append replay = %#v, %v, %v", again, duplicate, err)
	}
	next, duplicate, err := snapshot.Append(ctx, depot, "snapshot-next", []byte("next"))
	if err != nil || duplicate || next.Position != 2 {
		t.Fatalf("snapshot next append = %#v, %v, %v", next, duplicate, err)
	}
}

func TestSnapshotDestinationAndMemoryBehavior(t *testing.T) {
	ctx := context.Background()
	memory := NewMemory()
	defer memory.Close()
	empty := t.TempDir()
	if err := memory.Snapshot(ctx, empty); !errors.Is(err, ErrSnapshotUnsupported) {
		t.Fatalf("memory snapshot error = %v", err)
	}
	entries, err := os.ReadDir(empty)
	if err != nil || len(entries) != 0 {
		t.Fatalf("memory changed snapshot destination: entries=%v err=%v", entries, err)
	}

	pebbleStore, err := NewPebble(filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer pebbleStore.Close()
	nonempty := t.TempDir()
	marker := filepath.Join(nonempty, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pebbleStore.Snapshot(ctx, nonempty); !errors.Is(err, os.ErrExist) {
		t.Fatalf("nonempty snapshot destination error = %v", err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("snapshot changed nonempty destination: contents=%q err=%v", contents, err)
	}
}

func TestStoreCancellationAndClose(t *testing.T) {
	store := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.HighWater(ctx, DepotPartition{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	if err := store.Snapshot(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.HighWater(context.Background(), DepotPartition{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed error = %v", err)
	}
	if err := store.Snapshot(context.Background(), "unused"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed snapshot error = %v", err)
	}
}
