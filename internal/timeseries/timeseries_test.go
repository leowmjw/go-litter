package timeseries

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"testing"

	"app/internal/storage"
)

func intPtr(v int) *int { return &v }

func minute(bucket int) int64 {
	return int64(bucket * 60 * 1000)
}

func mustAppend(t *testing.T, ctx context.Context, m *Module, url string, ms int, bucket int) {
	t.Helper()
	if err := m.Append(ctx, RenderLatency{URL: url, RenderMillis: ms, TimestampMillis: minute(bucket)}); err != nil {
		t.Fatalf("append %s %d: %v", url, ms, err)
	}
}

func mustAdvanceAll(t *testing.T, ctx context.Context, m *Module) {
	t.Helper()
	if _, err := m.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance all: %v", err)
	}
}

func TestTimeSeriesModule(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 4)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}

	mustAppend(t, ctx, m, "foo.com", 10, 3)
	mustAppend(t, ctx, m, "foo.com", 20, 3)
	mustAppend(t, ctx, m, "foo.com", 15, 10)
	mustAppend(t, ctx, m, "foo.com", 18, 10)
	mustAppend(t, ctx, m, "foo.com", 33, 10)
	mustAppend(t, ctx, m, "foo.com", 20, 65)
	mustAppend(t, ctx, m, "foo.com", 30, 65)
	mustAppend(t, ctx, m, "foo.com", 100, 60*24)
	mustAppend(t, ctx, m, "foo.com", 100, 60*24+8)
	mustAppend(t, ctx, m, "foo.com", 50, 60*48+122)

	mustAdvanceAll(t, ctx, m)

	bucket3, found, err := m.GetWindowStats(ctx, "foo.com", "m", 3)
	if err != nil || !found {
		t.Fatalf("get m bucket 3: found=%v err=%v", found, err)
	}
	expected3 := WindowStats{Cardinality: 2, Total: 30, LastMillis: intPtr(20), MinLatencyMillis: intPtr(10), MaxLatencyMillis: intPtr(20)}
	if !reflect.DeepEqual(bucket3, expected3) {
		t.Fatalf("m bucket 3 = %+v, want %+v", bucket3, expected3)
	}

	range3to11, err := m.RangeWindowStats(ctx, "foo.com", "m", 3, 11)
	if err != nil {
		t.Fatalf("range m: %v", err)
	}
	expected10 := WindowStats{Cardinality: 3, Total: 66, LastMillis: intPtr(33), MinLatencyMillis: intPtr(15), MaxLatencyMillis: intPtr(33)}
	if !reflect.DeepEqual(range3to11, map[int]WindowStats{3: expected3, 10: expected10}) {
		t.Fatalf("range m = %+v", range3to11)
	}

	count, err := m.CountBuckets(ctx, "foo.com", "m", 0, 60*72)
	if err != nil || count != 6 {
		t.Fatalf("count = %d, %v", count, err)
	}

	large, err := m.StatsForMinuteRange(ctx, "foo.com", 0, 60*72)
	if err != nil {
		t.Fatalf("stats for range: %v", err)
	}
	expectedLarge := WindowStats{Cardinality: 10, Total: 396, LastMillis: intPtr(50), MinLatencyMillis: intPtr(10), MaxLatencyMillis: intPtr(100)}
	if !reflect.DeepEqual(large, expectedLarge) {
		t.Fatalf("large range = %+v, want %+v", large, expectedLarge)
	}

	day0, found, err := m.GetWindowStats(ctx, "foo.com", "d", 0)
	if err != nil || !found {
		t.Fatalf("get d bucket 0: found=%v err=%v", found, err)
	}
	expectedDay0 := WindowStats{Cardinality: 7, Total: 146, LastMillis: intPtr(30), MinLatencyMillis: intPtr(10), MaxLatencyMillis: intPtr(33)}
	if !reflect.DeepEqual(day0, expectedDay0) {
		t.Fatalf("d bucket 0 = %+v, want %+v", day0, expectedDay0)
	}
}

func TestTimeSeriesQueryValidation(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := module.GetWindowStats(ctx, "url", "week", 0); !errors.Is(err, ErrUnknownGranularity) {
		t.Fatalf("unknown granularity error = %v", err)
	}
	if _, _, err := module.GetWindowStats(ctx, "url", "m", -1); !errors.Is(err, ErrInvalidBucket) {
		t.Fatalf("negative bucket error = %v", err)
	}
	if strconv.IntSize == 64 {
		if _, _, err := module.GetWindowStats(ctx, "url", "m", int(uint64(math.MaxUint32)+1)); !errors.Is(err, ErrInvalidBucket) {
			t.Fatalf("overflow bucket error = %v", err)
		}
	}
	if _, err := module.RangeWindowStats(ctx, "url", "m", 2, 2); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("invalid range error = %v", err)
	}
}

func TestEmitQueryGranularities(t *testing.T) {
	cases := []struct {
		granularity string
		start, end  int
		expected    map[string]struct{}
	}{
		{
			"m", 63, 10033,
			map[string]struct{}{
				"m:63:120":      {},
				"h:2:24":        {},
				"d:1:6":         {},
				"h:144:167":     {},
				"m:10020:10033": {},
			},
		},
		{
			"d", 3, 122,
			map[string]struct{}{
				"d:3:30":    {},
				"td:1:4":    {},
				"d:120:122": {},
			},
		},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%s-%d-%d", c.granularity, c.start, c.end), func(t *testing.T) {
			got := map[string]struct{}{}
			err := EmitQueryGranularities(c.granularity, c.start, c.end, func(g string, s, e int) {
				got[fmt.Sprintf("%s:%d:%d", g, s, e)] = struct{}{}
			})
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if !reflect.DeepEqual(got, c.expected) {
				t.Fatalf("emits = %+v, want %+v", got, c.expected)
			}
		})
	}
}
