package tutorial

import (
	"context"
	"sort"
	"testing"

	"app/internal/storage"
)

func TestStage2AllPStateShapesAndQueries(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module, err := NewStage2(store, 2)
	if err != nil {
		t.Fatalf("NewStage2: %v", err)
	}

	if err := module.Record(ctx, Activity{User: "alice", Tag: "gold", Score: 10}); err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if err := module.Record(ctx, Activity{User: "alice", Tag: "silver", Score: 5}); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	if err := module.Record(ctx, Activity{User: "bob", Tag: "gold", Score: 20}); err != nil {
		t.Fatalf("record 3: %v", err)
	}

	// Scalar.
	total, err := module.TotalCount(ctx)
	if err != nil || total != 3 {
		t.Fatalf("TotalCount = %d, %v", total, err)
	}

	// Map (point query): last score wins.
	score, found, err := module.UserScore(ctx, "alice")
	if err != nil || !found || score != 5 {
		t.Fatalf("UserScore(alice) = %d, %v, %v", score, found, err)
	}

	// Sorted set (range query): both tags present, deduplicated by presence key.
	if err := module.Record(ctx, Activity{User: "alice", Tag: "gold", Score: 11}); err != nil {
		t.Fatalf("record 4: %v", err)
	}
	tags, err := module.UserTags(ctx, "alice")
	if err != nil {
		t.Fatalf("UserTags: %v", err)
	}
	sort.Strings(tags)
	if len(tags) != 2 || tags[0] != "gold" || tags[1] != "silver" {
		t.Fatalf("tags = %v", tags)
	}
	bobTags, err := module.UserTags(ctx, "bob")
	if err != nil || len(bobTags) != 1 || bobTags[0] != "gold" {
		t.Fatalf("bob tags = %v, %v", bobTags, err)
	}

	// Ordered list: append order preserved.
	events, err := module.UserEvents(ctx, "alice")
	if err != nil {
		t.Fatalf("UserEvents: %v", err)
	}
	want := []string{"gold", "silver", "gold"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events[%d] = %q, want %q (events=%v)", i, events[i], want[i], events)
		}
	}

	// Fixed-key record.
	rec, found, err := module.UserRecord(ctx, "alice")
	if err != nil || !found {
		t.Fatalf("UserRecord: %v, %v", found, err)
	}
	if rec.EventCount != 3 || rec.LastTag != "gold" || rec.HighScore != 11 {
		t.Fatalf("record = %+v", rec)
	}

	// Server-side transform: bounded reduction over a requested user set.
	sum, err := module.SumScoresForTag(ctx, "gold", []string{"alice", "bob"})
	if err != nil {
		t.Fatalf("SumScoresForTag: %v", err)
	}
	if sum != 31 { // alice's last score (11) + bob's (20)
		t.Fatalf("sum = %d, want 31", sum)
	}

	if err := module.Record(ctx, Activity{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestStage2RebuildFromDepotMatchesOriginal(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	module, err := NewStage2(store, 2)
	if err != nil {
		t.Fatalf("NewStage2: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := module.Record(ctx, Activity{User: "carol", Tag: "gold", Score: int64(i + 1)}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	before, _, err := module.UserScore(ctx, "carol")
	if err != nil {
		t.Fatalf("score before: %v", err)
	}
	beforeEvents, err := module.UserEvents(ctx, "carol")
	if err != nil {
		t.Fatalf("events before: %v", err)
	}
	beforeTotal, err := module.TotalCount(ctx)
	if err != nil {
		t.Fatalf("total before: %v", err)
	}

	if err := module.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	after, _, err := module.UserScore(ctx, "carol")
	if err != nil {
		t.Fatalf("score after: %v", err)
	}
	afterEvents, err := module.UserEvents(ctx, "carol")
	if err != nil {
		t.Fatalf("events after: %v", err)
	}
	afterTotal, err := module.TotalCount(ctx)
	if err != nil {
		t.Fatalf("total after: %v", err)
	}
	if before != after {
		t.Fatalf("score changed: before=%d after=%d", before, after)
	}
	if len(beforeEvents) != len(afterEvents) {
		t.Fatalf("event count changed: before=%v after=%v", beforeEvents, afterEvents)
	}
	if beforeTotal != afterTotal {
		t.Fatalf("total changed: before=%d after=%d", beforeTotal, afterTotal)
	}
}
