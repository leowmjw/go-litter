package stream

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"

	"app/internal/partition"
	"app/internal/storage"
)

func TestRuntimeAppendRetryReplayAndRebuild(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, err := partition.New(4)
	if err != nil {
		t.Fatal(err)
	}
	state := storage.StatePartition{Module: "module", State: "counts", Partition: choose([]byte("key"))}
	handlerCalls := 0
	handler := func(ctx context.Context, event *Event, _ storage.Record, task uint32) ([]byte, error) {
		handlerCalls++
		value, _, err := event.Get(ctx, storage.StatePartition{Module: "module", State: "counts", Partition: task}, []byte("key"))
		if err != nil {
			return nil, err
		}
		count := decodeTestUint64(value) + 1
		encoded := binary.BigEndian.AppendUint64(nil, count)
		event.Set(storage.StatePartition{Module: "module", State: "counts", Partition: task}, []byte("key"), encoded)
		return encoded, nil
	}
	runtime, err := New("module", 4, choose, store, Source{Depot: "events", Topology: "count", PartitionKey: func([]byte) ([]byte, error) { return []byte("key"), nil }, Handle: handler})
	if err != nil {
		t.Fatal(err)
	}
	originalCommit := store.Commit
	attempts := 0
	store.Commit = func(ctx context.Context, request storage.CommitRequest) (bool, error) {
		attempts++
		if attempts < 3 {
			return false, errors.New("injected commit failure")
		}
		return originalCommit(ctx, request)
	}
	ack, err := runtime.Append(ctx, "events", "request-1", []byte("event"))
	if err != nil || decodeTestUint64(ack["count"]) != 1 || attempts != 3 || handlerCalls != 3 {
		t.Fatalf("append ack=%v attempts=%d handlerCalls=%d err=%v", ack, attempts, handlerCalls, err)
	}
	store.Commit = originalCommit
	ack, err = runtime.Append(ctx, "events", "request-1", []byte("event"))
	if err != nil || decodeTestUint64(ack["count"]) != 1 || handlerCalls != 3 {
		t.Fatalf("duplicate ack=%v handlerCalls=%d err=%v", ack, handlerCalls, err)
	}
	if _, _, err := store.Append(ctx, storage.DepotPartition{Module: "module", Depot: "events", Partition: choose([]byte("key"))}, "request-2", []byte("event")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.GetState(ctx, state, []byte("key"))
	if err != nil || !found || decodeTestUint64(value) != 2 {
		t.Fatalf("replayed value=%d found=%v err=%v", decodeTestUint64(value), found, err)
	}
	if err := runtime.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	value, found, err = store.GetState(ctx, state, []byte("key"))
	if err != nil || !found || decodeTestUint64(value) != 2 {
		t.Fatalf("rebuilt value=%d found=%v err=%v", decodeTestUint64(value), found, err)
	}
}

func TestRuntimeProcessesPartitionsConcurrently(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(2)
	firstKey := []byte("a")
	secondKey := []byte("b")
	for choose(secondKey) == choose(firstKey) {
		secondKey = append(secondKey, 'b')
	}
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	handler := func(_ context.Context, _ *Event, record storage.Record, _ uint32) ([]byte, error) {
		if string(record.Payload) == string(firstKey) {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return nil, nil
	}
	runtime, err := New("module", 2, choose, store, Source{Depot: "events", Topology: "topology", PartitionKey: func(payload []byte) ([]byte, error) { return payload, nil }, Handle: handler})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := runtime.Append(ctx, "events", "first", firstKey)
		firstDone <- err
	}()
	<-firstEntered
	go func() {
		_, err := runtime.Append(ctx, "events", "second", secondKey)
		secondDone <- err
	}()
	<-secondEntered
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePreservesPartitionOrder(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(4)
	positions := make([]uint64, 0, 64)
	runtime, err := New("module", 4, choose, store, Source{
		Depot: "events", Topology: "topology",
		PartitionKey: func([]byte) ([]byte, error) { return []byte("same-key"), nil },
		Handle: func(_ context.Context, _ *Event, record storage.Record, _ uint32) ([]byte, error) {
			positions = append(positions, record.Position)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runtime.Append(ctx, "events", fmt.Sprintf("request-%d", i), []byte("event")); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(positions) != 64 {
		t.Fatalf("positions = %v", positions)
	}
	for i, position := range positions {
		if position != uint64(i+1) {
			t.Fatalf("positions = %v", positions)
		}
	}
}

func TestRuntimeDoesNotSkipFailedRecord(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(1)
	allowBad := false
	state := storage.StatePartition{Module: "module", State: "counts", Partition: 0}
	handler := func(ctx context.Context, event *Event, record storage.Record, _ uint32) ([]byte, error) {
		if string(record.Payload) == "bad" && !allowBad {
			return nil, errors.New("bad record")
		}
		value, _, err := event.Get(ctx, state, []byte("count"))
		if err != nil {
			return nil, err
		}
		event.Set(state, []byte("count"), binary.BigEndian.AppendUint64(nil, decodeTestUint64(value)+1))
		return nil, nil
	}
	runtime, err := New("module", 1, choose, store, Source{Depot: "events", Topology: "count", PartitionKey: func([]byte) ([]byte, error) { return []byte("key"), nil }, Handle: handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Append(ctx, "events", "bad", []byte("bad")); err == nil {
		t.Fatal("expected first record failure")
	}
	if _, err := runtime.Append(ctx, "events", "good", []byte("good")); err == nil {
		t.Fatal("expected later record to remain blocked")
	}
	cursor := storage.Cursor{Module: "module", Topology: "count", Depot: "events", Partition: 0}
	if checkpoint, err := store.Checkpoint(ctx, cursor); err != nil || checkpoint != 0 {
		t.Fatalf("checkpoint = %d, %v", checkpoint, err)
	}
	allowBad = true
	if err := runtime.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.GetState(ctx, state, []byte("count"))
	if err != nil || !found || decodeTestUint64(value) != 2 {
		t.Fatalf("count = %d, %v, %v", decodeTestUint64(value), found, err)
	}
	if checkpoint, err := store.Checkpoint(ctx, cursor); err != nil || checkpoint != 2 {
		t.Fatalf("checkpoint = %d, %v", checkpoint, err)
	}
}

func TestRuntimeEnqueueDefersProcessingToReplay(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(2)
	handlerCalls := 0
	handler := func(ctx context.Context, event *Event, _ storage.Record, task uint32) ([]byte, error) {
		handlerCalls++
		value, _, err := event.Get(ctx, storage.StatePartition{Module: "module", State: "counts", Partition: task}, []byte("key"))
		if err != nil {
			return nil, err
		}
		encoded := binary.BigEndian.AppendUint64(nil, decodeTestUint64(value)+1)
		event.Set(storage.StatePartition{Module: "module", State: "counts", Partition: task}, []byte("key"), encoded)
		return encoded, nil
	}
	runtime, err := New("module", 2, choose, store, Source{Depot: "events", Topology: "count", PartitionKey: func([]byte) ([]byte, error) { return []byte("key"), nil }, Handle: handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Enqueue(ctx, "events", "request-1", []byte("event")); err != nil {
		t.Fatal(err)
	}
	if handlerCalls != 0 {
		t.Fatalf("enqueue must not process: handlerCalls = %d", handlerCalls)
	}
	if err := runtime.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	if handlerCalls != 1 {
		t.Fatalf("replay must process enqueued record: handlerCalls = %d", handlerCalls)
	}
	value, found, err := store.GetState(ctx, storage.StatePartition{Module: "module", State: "counts", Partition: choose([]byte("key"))}, []byte("key"))
	if err != nil || !found || decodeTestUint64(value) != 1 {
		t.Fatalf("value=%d found=%v err=%v", decodeTestUint64(value), found, err)
	}
	// Replay again is idempotent: already committed, handler must not rerun.
	if err := runtime.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	if handlerCalls != 1 {
		t.Fatalf("replay must be idempotent: handlerCalls = %d", handlerCalls)
	}
}

func TestRuntimeValidation(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	choose, _ := partition.New(1)
	source := Source{Depot: "events", Topology: "topology", PartitionKey: func([]byte) ([]byte, error) { return nil, nil }, Handle: func(context.Context, *Event, storage.Record, uint32) ([]byte, error) { return nil, nil }}
	if _, err := New("module", 0, choose, store, source); !errors.Is(err, ErrTaskCount) {
		t.Fatalf("task count error = %v", err)
	}
	if _, err := New("module", 1, choose, store, source, source); !errors.Is(err, ErrSourceExists) {
		t.Fatalf("duplicate source error = %v", err)
	}
	runtime, err := New("module", 1, choose, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Append(context.Background(), "missing", "", nil); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	outOfRange, err := New("module", 1, func([]byte) uint32 { return 1 }, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outOfRange.Append(context.Background(), "events", "", nil); !errors.Is(err, ErrPartition) {
		t.Fatalf("partition error = %v", err)
	}
}

func TestRuntimeInjectedErrors(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("injected")
	choose, _ := partition.New(1)
	newRuntime := func(store *storage.Store, partitionKey func([]byte) ([]byte, error), handler func(context.Context, *Event, storage.Record, uint32) ([]byte, error)) *Runtime {
		runtime, err := New("module", 1, choose, store, Source{Depot: "events", Topology: "topology", PartitionKey: partitionKey, Handle: handler})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	store := storage.NewMemory()
	runtime := newRuntime(store, func([]byte) ([]byte, error) { return nil, injected }, func(context.Context, *Event, storage.Record, uint32) ([]byte, error) { return nil, nil })
	if _, err := runtime.Append(ctx, "events", "key", nil); !errors.Is(err, injected) {
		t.Fatalf("partition-key error = %v", err)
	}
	_ = store.Close()

	store = storage.NewMemory()
	runtime = newRuntime(store, func([]byte) ([]byte, error) { return nil, nil }, func(context.Context, *Event, storage.Record, uint32) ([]byte, error) { return nil, injected })
	if _, err := runtime.Append(ctx, "events", "key", nil); !errors.Is(err, injected) {
		t.Fatalf("handler error = %v", err)
	}
	_ = store.Close()

	store = storage.NewMemory()
	runtime = newRuntime(store, func([]byte) ([]byte, error) { return nil, nil }, func(context.Context, *Event, storage.Record, uint32) ([]byte, error) { return nil, nil })
	store.Commit = func(context.Context, storage.CommitRequest) (bool, error) { return false, injected }
	if _, err := runtime.Append(ctx, "events", "key", nil); !errors.Is(err, injected) {
		t.Fatalf("commit error = %v", err)
	}
	store.ResetDerived = func(context.Context, storage.ModuleID) error { return injected }
	if err := runtime.Rebuild(ctx); !errors.Is(err, injected) {
		t.Fatalf("rebuild error = %v", err)
	}
	_ = store.Close()
}

func decodeTestUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
