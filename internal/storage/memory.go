package storage

import (
	"bytes"
	"context"
	"sort"
	"sync"
)

func NewMemory() *Store {
	var mu sync.RWMutex
	data := make(map[string][]byte)
	closed := false

	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if closed {
			return ErrClosed
		}
		return nil
	}

	store := &Store{}
	store.Append = func(ctx context.Context, depot DepotPartition, id string, payload []byte) (Record, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return Record{}, false, err
		}
		if id != "" {
			if encoded, ok := data[string(appendIDKey(depot, id))]; ok {
				position := decodeUint64(encoded)
				existing := data[string(recordKey(depot, position))]
				if !bytes.Equal(existing, payload) {
					return Record{}, true, ErrIdempotencyConflict
				}
				return Record{Position: position, Payload: cloneBytes(existing)}, true, nil
			}
		}
		position := decodeUint64(data[string(highWaterKey(depot))]) + 1
		data[string(recordKey(depot, position))] = cloneBytes(payload)
		data[string(highWaterKey(depot))] = uint64Bytes(position)
		if id != "" {
			data[string(appendIDKey(depot, id))] = uint64Bytes(position)
		}
		return Record{Position: position, Payload: cloneBytes(payload)}, false, nil
	}
	store.Read = func(ctx context.Context, depot DepotPartition, from uint64, limit int) ([]Record, error) {
		if limit <= 0 {
			return nil, ErrInvalidLimit
		}
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, err
		}
		if from == 0 {
			from = 1
		}
		high := decodeUint64(data[string(highWaterKey(depot))])
		capacity := 0
		if high >= from {
			capacity = min(limit, int(high-from+1))
		}
		records := make([]Record, 0, capacity)
		for position := from; position <= high && len(records) < limit; position++ {
			payload, ok := data[string(recordKey(depot, position))]
			if ok {
				records = append(records, Record{Position: position, Payload: cloneBytes(payload)})
			}
		}
		return records, nil
	}
	store.HighWater = func(ctx context.Context, depot DepotPartition) (uint64, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return 0, err
		}
		return decodeUint64(data[string(highWaterKey(depot))]), nil
	}
	store.GetState = func(ctx context.Context, state StatePartition, key []byte) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		value, ok := data[string(stateKey(state, key))]
		return cloneBytes(value), ok, nil
	}
	store.ScanState = func(ctx context.Context, state StatePartition, start, end []byte, limit int, reverse bool) ([]Entry, error) {
		if limit <= 0 {
			return nil, ErrInvalidLimit
		}
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, err
		}
		prefix := statePrefix(state)
		keys := make([][]byte, 0)
		for encoded := range data {
			key := []byte(encoded)
			if !bytes.HasPrefix(key, prefix) {
				continue
			}
			logical := key[len(prefix):]
			if len(start) > 0 && bytes.Compare(logical, start) < 0 {
				continue
			}
			if len(end) > 0 && bytes.Compare(logical, end) >= 0 {
				continue
			}
			keys = append(keys, cloneBytes(logical))
		}
		sort.Slice(keys, func(i, j int) bool {
			if reverse {
				return bytes.Compare(keys[i], keys[j]) > 0
			}
			return bytes.Compare(keys[i], keys[j]) < 0
		})
		if len(keys) > limit {
			keys = keys[:limit]
		}
		entries := make([]Entry, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, Entry{Key: key, Value: cloneBytes(data[string(stateKey(state, key))])})
		}
		return entries, nil
	}
	store.Commit = func(ctx context.Context, request CommitRequest) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return false, err
		}
		marker := string(batchKey(request.Module, request.BatchID))
		if _, ok := data[marker]; ok {
			return false, nil
		}
		for _, mutation := range request.Mutations {
			key := string(stateKey(mutation.State, mutation.Key))
			if mutation.Delete {
				delete(data, key)
			} else {
				data[key] = cloneBytes(mutation.Value)
			}
		}
		for _, checkpoint := range commitCheckpoints(request) {
			data[string(checkpointKey(checkpoint))] = uint64Bytes(checkpoint.Position)
		}
		data[marker] = append([]byte{1}, request.Result...)
		return true, nil
	}
	store.CommitResult = func(ctx context.Context, module ModuleID, batchID BatchID) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		value, ok := data[string(batchKey(module, batchID))]
		if !ok {
			return nil, false, nil
		}
		if len(value) == 0 {
			return nil, false, ErrCorruptStore
		}
		return cloneBytes(value[1:]), true, nil
	}
	store.Checkpoint = func(ctx context.Context, cursor Cursor) (uint64, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return 0, err
		}
		return decodeUint64(data[string(checkpointKey(cursor))]), nil
	}
	store.GetMetadata = func(ctx context.Context, module ModuleID, key []byte) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		value, ok := data[string(metadataKey(module, key))]
		return cloneBytes(value), ok, nil
	}
	store.SetMetadata = func(ctx context.Context, module ModuleID, key, value []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return err
		}
		data[string(metadataKey(module, key))] = cloneBytes(value)
		return nil
	}
	store.ResetDerived = func(ctx context.Context, module ModuleID) error {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return err
		}
		prefixes := [][]byte{stateModulePrefix(module), checkpointModulePrefix(module), batchModulePrefix(module)}
		for encoded := range data {
			key := []byte(encoded)
			for _, prefix := range prefixes {
				if bytes.HasPrefix(key, prefix) {
					delete(data, encoded)
					break
				}
			}
		}
		return nil
	}
	store.Snapshot = func(ctx context.Context, _ string) error {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return err
		}
		return ErrSnapshotUnsupported
	}
	store.Close = func() error {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		return nil
	}
	return store
}
