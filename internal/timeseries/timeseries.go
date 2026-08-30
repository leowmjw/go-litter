package timeseries

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"

	"app/internal/microbatch"
	"app/internal/partition"
	"app/internal/storage"
)

const (
	ModuleName         storage.ModuleID   = "time-series"
	RenderLatencyDepot storage.DepotID    = "render-latency"
	WindowStatsState   storage.StateID    = "window-stats"
	TimeSeriesTopology storage.TopologyID = "timeseries"
)

var (
	ErrRenderLatencyURL   = errors.New("url, render latency, and timestamp are required")
	ErrInvalidBucket      = errors.New("bucket must be between zero and the maximum uint32 value")
	ErrInvalidRange       = errors.New("range must be within uint32 bounds and start must be less than end")
	ErrUnknownGranularity = errors.New("unknown granularity")
)

type Module struct {
	Append              func(context.Context, RenderLatency) error
	Advance             func(context.Context) (int, error)
	AdvanceAll          func(context.Context) (int, error)
	Rebuild             func(context.Context) error
	GetWindowStats      func(context.Context, string, string, int) (WindowStats, bool, error)
	RangeWindowStats    func(context.Context, string, string, int, int) (map[int]WindowStats, error)
	CountBuckets        func(context.Context, string, string, int, int) (int, error)
	StatsForMinuteRange func(context.Context, string, int, int) (WindowStats, error)
}

func New(store *storage.Store, taskCount uint32) (*Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	runtime, err := microbatch.New(ModuleName, taskCount, choosePartition, store,
		microbatch.Topology{
			Name: TimeSeriesTopology,
			Sources: []microbatch.Source{
				{Depot: RenderLatencyDepot, PartitionKey: func(payload []byte) ([]byte, error) {
					var r RenderLatency
					if err := json.Unmarshal(payload, &r); err != nil {
						return nil, err
					}
					return []byte(r.URL), nil
				}},
			},
			Handle: func(ctx context.Context, event *microbatch.Event, items []microbatch.Item) error {
				for _, item := range items {
					var r RenderLatency
					if err := json.Unmarshal(item.Record.Payload, &r); err != nil {
						return err
					}
					if err := combineRecord(ctx, choosePartition, event, &r); err != nil {
						return err
					}
				}
				return nil
			},
		})
	if err != nil {
		return nil, err
	}

	module := &Module{}
	module.Append = func(ctx context.Context, r RenderLatency) error {
		if r.URL == "" || r.RenderMillis < 0 || r.TimestampMillis < 0 {
			return ErrRenderLatencyURL
		}
		payload, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, RenderLatencyDepot, "", payload)
		return err
	}
	module.Advance = func(ctx context.Context) (int, error) { return runtime.Advance(ctx, TimeSeriesTopology) }
	module.AdvanceAll = func(ctx context.Context) (int, error) { return runtime.AdvanceAll(ctx) }
	module.Rebuild = runtime.Rebuild
	module.GetWindowStats = func(ctx context.Context, url, granularity string, bucket int) (WindowStats, bool, error) {
		if !validGranularity(granularity) {
			return WindowStats{}, false, ErrUnknownGranularity
		}
		if !validBucket(bucket) {
			return WindowStats{}, false, ErrInvalidBucket
		}
		state := statePartition(choosePartition, url)
		value, found, err := store.GetState(ctx, state, windowStatsKey(url, granularity, bucket))
		if err != nil || !found {
			return WindowStats{}, found, err
		}
		var stats WindowStats
		if err := json.Unmarshal(value, &stats); err != nil {
			return WindowStats{}, false, err
		}
		return stats, true, nil
	}
	module.RangeWindowStats = func(ctx context.Context, url, granularity string, start, end int) (map[int]WindowStats, error) {
		if !validGranularity(granularity) {
			return nil, ErrUnknownGranularity
		}
		if !validRange(start, end) {
			return nil, ErrInvalidRange
		}
		state := statePartition(choosePartition, url)
		prefix := windowStatsPrefix(url, granularity)
		startKey := binary.BigEndian.AppendUint32(append([]byte(nil), prefix...), uint32(start))
		endKey := binary.BigEndian.AppendUint32(append([]byte(nil), prefix...), uint32(end))
		entries, err := store.ScanState(ctx, state, startKey, endKey, 1048576, false)
		if err != nil {
			return nil, err
		}
		result := make(map[int]WindowStats, len(entries))
		for _, entry := range entries {
			bucket, ok := decodeWindowStatsBucket(entry.Key)
			if !ok {
				continue
			}
			var stats WindowStats
			if err := json.Unmarshal(entry.Value, &stats); err != nil {
				return nil, err
			}
			result[bucket] = stats
		}
		return result, nil
	}
	module.CountBuckets = func(ctx context.Context, url, granularity string, start, end int) (int, error) {
		if !validGranularity(granularity) {
			return 0, ErrUnknownGranularity
		}
		if !validRange(start, end) {
			return 0, ErrInvalidRange
		}
		state := statePartition(choosePartition, url)
		prefix := windowStatsPrefix(url, granularity)
		startKey := binary.BigEndian.AppendUint32(append([]byte(nil), prefix...), uint32(start))
		endKey := binary.BigEndian.AppendUint32(append([]byte(nil), prefix...), uint32(end))
		entries, err := store.ScanState(ctx, state, startKey, endKey, 1048576, false)
		if err != nil {
			return 0, err
		}
		return len(entries), nil
	}
	module.StatsForMinuteRange = func(ctx context.Context, url string, startBucket, endBucket int) (WindowStats, error) {
		if !validRange(startBucket, endBucket) {
			return WindowStats{}, ErrInvalidRange
		}
		var combined WindowStats
		var queryErr error
		if err := EmitQueryGranularities("m", startBucket, endBucket, func(granularity string, gStart, gEnd int) {
			if queryErr != nil || gStart >= gEnd {
				return
			}
			buckets, err := module.RangeWindowStats(ctx, url, granularity, gStart, gEnd)
			if err != nil {
				queryErr = err
				return
			}
			for bucket := gStart; bucket < gEnd; bucket++ {
				if stats, ok := buckets[bucket]; ok {
					combined = Combine(combined, stats)
				}
			}
		}); err != nil {
			return WindowStats{}, err
		}
		if queryErr != nil {
			return WindowStats{}, queryErr
		}
		return combined, nil
	}
	return module, nil
}

func validGranularity(granularity string) bool {
	switch granularity {
	case "m", "h", "d", "td":
		return true
	default:
		return false
	}
}

func validBucket(bucket int) bool {
	return bucket >= 0 && uint64(bucket) <= math.MaxUint32
}

func validRange(start, end int) bool {
	return validBucket(start) && validBucket(end) && start < end
}

func statePartition(choosePartition func([]byte) uint32, url string) storage.StatePartition {
	return storage.StatePartition{Module: ModuleName, State: WindowStatsState, Partition: choosePartition([]byte(url))}
}

func combineRecord(ctx context.Context, choosePartition func([]byte) uint32, event *microbatch.Event, r *RenderLatency) error {
	single := MakeSingle(r.RenderMillis)
	buckets, err := allBuckets(r.TimestampMillis)
	if err != nil {
		return err
	}
	state := statePartition(choosePartition, r.URL)
	for _, granularity := range []string{"m", "h", "d", "td"} {
		bucket := buckets[granularity]
		key := windowStatsKey(r.URL, granularity, bucket)
		existing, found, err := event.Get(ctx, state, key)
		if err != nil {
			return err
		}
		var current WindowStats
		if found {
			if err := json.Unmarshal(existing, &current); err != nil {
				return err
			}
		}
		combined := Combine(current, single)
		encoded, err := json.Marshal(combined)
		if err != nil {
			return err
		}
		event.Set(state, key, encoded)
	}
	return nil
}
