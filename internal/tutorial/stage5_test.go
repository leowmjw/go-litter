package tutorial

import (
	"context"
	"errors"
	"testing"
	"time"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

func TestStage5StreamAppendVisibleImmediatelyAfterAck(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage5Stream(storage.NewMemory(), 2)
	if err != nil {
		t.Fatalf("NewStage5Stream: %v", err)
	}
	if err := module.Append(ctx, "a"); err != nil {
		t.Fatalf("append: %v", err)
	}
	// No Advance call exists for the stream module: the acknowledged Append
	// call above already guarantees the PState effect is committed.
	count, err := module.Count(ctx, "a")
	if err != nil || count != 1 {
		t.Fatalf("Count = %d, %v", count, err)
	}
}

func TestStage5MicrobatchAppendNotVisibleUntilAdvance(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage5Microbatch(storage.NewMemory(), 2)
	if err != nil {
		t.Fatalf("NewStage5Microbatch: %v", err)
	}
	if err := module.Append(ctx, "a"); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Appending does not imply processing completion for a microbatch ETL.
	count, err := module.Count(ctx, "a")
	if err != nil || count != 0 {
		t.Fatalf("Count before advance = %d, %v (want 0)", count, err)
	}
	n, err := module.Advance(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Advance = %d, %v", n, err)
	}
	count, err = module.Count(ctx, "a")
	if err != nil || count != 1 {
		t.Fatalf("Count after advance = %d, %v", count, err)
	}
	// A second Advance with no new records has no additional effect.
	n, err = module.Advance(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second Advance = %d, %v", n, err)
	}
	count, err = module.Count(ctx, "a")
	if err != nil || count != 1 {
		t.Fatalf("Count after second advance = %d, %v (must stay 1: exactly-once)", count, err)
	}
}

func TestStage5MicrobatchVirtualTimeSchedulerAvoidsRealSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var advances int
	var seenIntervals []time.Duration
	advance := func(context.Context) error {
		advances++
		if advances >= 3 {
			cancel()
		}
		return nil
	}
	wait := func(ctx context.Context, d time.Duration) error {
		seenIntervals = append(seenIntervals, d)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	scheduler, err := microbatch.NewScheduler(30*time.Second, advance, wait)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	start := time.Now()
	runErr := scheduler.Run(ctx)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", runErr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("scheduler took %v; virtual time injection should avoid real 30s waits", elapsed)
	}
	if advances != 3 {
		t.Fatalf("advances = %d, want 3", advances)
	}
	for _, interval := range seenIntervals {
		if interval != 30*time.Second {
			t.Fatalf("interval = %v, want 30s (the configured microbatch window)", interval)
		}
	}
}

// TestStage5StreamRecordLevelRetryViaReplay builds a raw stream runtime with
// a handler that fails on its first attempt. The failed Append surfaces the
// error immediately (the record is not silently dropped or retried in-line),
// but because the depot append itself already durably succeeded before
// processing failed, an explicit Replay re-drives the same unacknowledged
// record through the handler and completes it - the configured retry policy
// this project uses for stream ETLs.
func TestStage5StreamRecordLevelRetryViaReplay(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	choosePartition, err := partition.New(2)
	if err != nil {
		t.Fatalf("partition.New: %v", err)
	}
	const module storage.ModuleID = "stage5-retry"
	const depot storage.DepotID = "flaky"
	const topic storage.TopologyID = "flaky"
	const stateID storage.StateID = "count"
	attempts := 0
	runtime, err := stream.New(module, 2, choosePartition, store,
		stream.Source{
			Depot: depot, Topology: topic,
			PartitionKey: func(payload []byte) ([]byte, error) { return payload, nil },
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				attempts++
				if attempts == 1 {
					return nil, errors.New("transient failure")
				}
				event.Set(storage.StatePartition{Module: module, State: stateID, Partition: task}, []byte("k"), []byte{1})
				return nil, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("stream.New: %v", err)
	}
	if _, err := runtime.Append(ctx, depot, "", []byte("k")); err == nil {
		t.Fatal("expected the first, failing attempt to surface its error")
	}
	if attempts != 1 {
		t.Fatalf("attempts after failed append = %d, want 1", attempts)
	}
	statePartition := storage.StatePartition{Module: module, State: stateID, Partition: choosePartition([]byte("k"))}
	if _, found, err := store.GetState(ctx, statePartition, []byte("k")); err != nil || found {
		t.Fatalf("state must not be committed after a failed attempt: found=%v, err=%v", found, err)
	}

	// The record was durably appended despite the processing failure; Replay
	// re-drives it through the handler without requiring a re-append.
	if err := runtime.Replay(ctx); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts after replay = %d, want 2", attempts)
	}
	value, found, err := store.GetState(ctx, statePartition, []byte("k"))
	if err != nil || !found || len(value) != 1 {
		t.Fatalf("state = %v, %v, %v", value, found, err)
	}
}

// TestStage5StreamAppendFailsWhenRetryBudgetExhausted proves an
// unacknowledged record is never silently dropped: the Append call surfaces
// the failure to the caller instead of committing a partial effect.
func TestStage5StreamAppendFailsWhenRetryBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	choosePartition, err := partition.New(1)
	if err != nil {
		t.Fatalf("partition.New: %v", err)
	}
	const module storage.ModuleID = "stage5-always-fails"
	const depot storage.DepotID = "flaky"
	runtime, err := stream.New(module, 1, choosePartition, store,
		stream.Source{
			Depot: depot, Topology: "flaky",
			PartitionKey: func(payload []byte) ([]byte, error) { return payload, nil },
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				return nil, errors.New("permanent failure")
			},
		},
	)
	if err != nil {
		t.Fatalf("stream.New: %v", err)
	}
	if _, err := runtime.Append(ctx, depot, "", []byte("k")); err == nil {
		t.Fatal("expected Append to surface the exhausted retry error")
	}
}
