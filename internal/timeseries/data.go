package timeseries

import (
	"encoding/binary"
	"fmt"
)

const millisPerMinute = 60 * 1000
const millisPerHour = 60 * millisPerMinute
const millisPerDay = 24 * millisPerHour
const millisPerThirtyDays = 30 * millisPerDay

var granularityDivisors = map[string]int{
	"m":  60,
	"h":  24,
	"d":  30,
	"td": 0,
}

var nextGranularity = map[string]string{
	"m": "h",
	"h": "d",
	"d": "td",
}

type RenderLatency struct {
	URL             string `json:"url"`
	RenderMillis    int    `json:"renderMillis"`
	TimestampMillis int64  `json:"timestampMillis"`
}

type WindowStats struct {
	Cardinality      int64 `json:"cardinality"`
	Total            int64 `json:"total"`
	LastMillis       *int  `json:"lastMillis,omitempty"`
	MinLatencyMillis *int  `json:"minLatencyMillis,omitempty"`
	MaxLatencyMillis *int  `json:"maxLatencyMillis,omitempty"`
}

func MakeSingle(latency int) WindowStats {
	stats := WindowStats{Cardinality: 1, Total: int64(latency)}
	stats.LastMillis = &latency
	stats.MinLatencyMillis = &latency
	stats.MaxLatencyMillis = &latency
	return stats
}

func Combine(curr, other WindowStats) WindowStats {
	ret := WindowStats{Cardinality: curr.Cardinality + other.Cardinality, Total: curr.Total + other.Total}
	if other.LastMillis != nil {
		ret.LastMillis = other.LastMillis
	} else {
		ret.LastMillis = curr.LastMillis
	}
	ret.MinLatencyMillis = minPtr(curr.MinLatencyMillis, other.MinLatencyMillis)
	ret.MaxLatencyMillis = maxPtr(curr.MaxLatencyMillis, other.MaxLatencyMillis)
	return ret
}

func minPtr(a, b *int) *int {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a < *b {
		return a
	}
	return b
}

func maxPtr(a, b *int) *int {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a > *b {
		return a
	}
	return b
}

func bucketForTimestamp(timestampMillis int64, granularity string) (int, error) {
	switch granularity {
	case "m":
		return int(timestampMillis / millisPerMinute), nil
	case "h":
		return int(timestampMillis / millisPerHour), nil
	case "d":
		return int(timestampMillis / millisPerDay), nil
	case "td":
		return int(timestampMillis / millisPerThirtyDays), nil
	}
	return 0, fmt.Errorf("unknown granularity %q", granularity)
}

func allBuckets(timestampMillis int64) (map[string]int, error) {
	buckets := map[string]int{}
	for _, gran := range []string{"m", "h", "d", "td"} {
		bucket, err := bucketForTimestamp(timestampMillis, gran)
		if err != nil {
			return nil, err
		}
		buckets[gran] = bucket
	}
	return buckets, nil
}

func EmitQueryGranularities(granularity string, startBucket, endBucket int, emit func(string, int, int)) error {
	if startBucket >= endBucket {
		return nil
	}
	next, ok := nextGranularity[granularity]
	if !ok {
		emit(granularity, startBucket, endBucket)
		return nil
	}
	divisor := granularityDivisors[granularity]
	nextStart := startBucket / divisor
	if startBucket%divisor != 0 {
		nextStart++
	}
	nextEnd := endBucket / divisor
	nextAlignedStart := nextStart * divisor
	nextAlignedEnd := nextEnd * divisor
	if nextEnd > nextStart {
		if err := EmitQueryGranularities(next, nextStart, nextEnd, emit); err != nil {
			return err
		}
	}
	if nextAlignedStart >= nextAlignedEnd {
		emit(granularity, startBucket, endBucket)
		return nil
	}
	if nextAlignedStart > startBucket {
		emit(granularity, startBucket, nextAlignedStart)
	}
	if endBucket > nextAlignedEnd {
		emit(granularity, nextAlignedEnd, endBucket)
	}
	return nil
}

func windowStatsPrefix(url, granularity string) []byte {
	key := append([]byte(url), 0)
	key = append(key, []byte(granularity)...)
	key = append(key, 0)
	return key
}

func windowStatsKey(url, granularity string, bucket int) []byte {
	return binary.BigEndian.AppendUint32(windowStatsPrefix(url, granularity), uint32(bucket))
}

func decodeWindowStatsBucket(key []byte) (int, bool) {
	if len(key) < 4 {
		return 0, false
	}
	return int(binary.BigEndian.Uint32(key[len(key)-4:])), true
}
