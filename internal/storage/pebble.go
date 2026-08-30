package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"

	pebble "github.com/cockroachdb/pebble/v2"
)

func prepareSnapshotDestination(destination string) error {
	entries, err := os.ReadDir(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return &os.PathError{Op: "snapshot", Path: destination, Err: os.ErrExist}
	}
	return os.Remove(destination)
}

func NewPebble(path string) (*Store, error) {
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatFlushableIngest})
	if err != nil {
		return nil, err
	}
	var mu sync.RWMutex
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
	get := func(key []byte) ([]byte, bool, error) {
		value, closer, err := db.Get(key)
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		copied := cloneBytes(value)
		if err := closer.Close(); err != nil {
			return nil, false, err
		}
		return copied, true, nil
	}
	formatKey := metadataKey("__storage", []byte("pebble-format"))
	createdByKey := metadataKey("__storage", []byte("pebble-created-by"))
	expectedFormat := uint64Bytes(uint64(pebble.FormatFlushableIngest))
	storedFormat, found, err := get(formatKey)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if found && !bytes.Equal(storedFormat, expectedFormat) {
		_ = db.Close()
		return nil, fmt.Errorf("%w: pebble format %d", ErrCorruptStore, decodeUint64(storedFormat))
	}
	if !found {
		batch := db.NewBatch()
		if err := batch.Set(formatKey, expectedFormat, nil); err == nil {
			err = batch.Set(createdByKey, []byte("github.com/cockroachdb/pebble/v2@v2.1.6"), nil)
		}
		if err == nil {
			err = batch.Commit(pebble.Sync)
		}
		closeErr := batch.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	store := &Store{}
	store.Append = func(ctx context.Context, depot DepotPartition, id string, payload []byte) (Record, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return Record{}, false, err
		}
		if id != "" {
			encoded, ok, err := get(appendIDKey(depot, id))
			if err != nil {
				return Record{}, false, err
			}
			if ok {
				position := decodeUint64(encoded)
				existing, found, err := get(recordKey(depot, position))
				if err != nil {
					return Record{}, true, err
				}
				if !found || !bytes.Equal(existing, payload) {
					return Record{}, true, ErrIdempotencyConflict
				}
				return Record{Position: position, Payload: existing}, true, nil
			}
		}
		high, _, err := get(highWaterKey(depot))
		if err != nil {
			return Record{}, false, err
		}
		position := decodeUint64(high) + 1
		batch := db.NewBatch()
		defer batch.Close()
		if err := batch.Set(recordKey(depot, position), payload, nil); err != nil {
			return Record{}, false, err
		}
		if err := batch.Set(highWaterKey(depot), uint64Bytes(position), nil); err != nil {
			return Record{}, false, err
		}
		if id != "" {
			if err := batch.Set(appendIDKey(depot, id), uint64Bytes(position), nil); err != nil {
				return Record{}, false, err
			}
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return Record{}, false, err
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
		prefix := recordPrefix(depot)
		iter, err := db.NewIter(&pebble.IterOptions{LowerBound: recordKey(depot, from), UpperBound: prefixEnd(prefix)})
		if err != nil {
			return nil, err
		}
		records := make([]Record, 0, limit)
		for valid := iter.First(); valid && len(records) < limit; valid = iter.Next() {
			key := iter.Key()
			if len(key) < len(prefix)+8 {
				continue
			}
			position := binary.BigEndian.Uint64(key[len(key)-8:])
			records = append(records, Record{Position: position, Payload: cloneBytes(iter.Value())})
		}
		if err := iter.Close(); err != nil {
			return nil, err
		}
		return records, nil
	}
	store.HighWater = func(ctx context.Context, depot DepotPartition) (uint64, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return 0, err
		}
		value, _, err := get(highWaterKey(depot))
		return decodeUint64(value), err
	}
	store.GetState = func(ctx context.Context, state StatePartition, key []byte) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		return get(stateKey(state, key))
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
		lower := prefix
		if len(start) > 0 {
			lower = stateKey(state, start)
		}
		upper := prefixEnd(prefix)
		if len(end) > 0 {
			upper = stateKey(state, end)
		}
		iter, err := db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, limit)
		if reverse {
			for valid := iter.Last(); valid && len(entries) < limit; valid = iter.Prev() {
				entries = append(entries, Entry{Key: cloneBytes(iter.Key()[len(prefix):]), Value: cloneBytes(iter.Value())})
			}
		} else {
			for valid := iter.First(); valid && len(entries) < limit; valid = iter.Next() {
				entries = append(entries, Entry{Key: cloneBytes(iter.Key()[len(prefix):]), Value: cloneBytes(iter.Value())})
			}
		}
		if err := iter.Close(); err != nil {
			return nil, err
		}
		return entries, nil
	}
	store.Commit = func(ctx context.Context, request CommitRequest) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return false, err
		}
		marker := batchKey(request.Module, request.BatchID)
		if _, ok, err := get(marker); err != nil {
			return false, err
		} else if ok {
			return false, nil
		}
		batch := db.NewBatch()
		defer batch.Close()
		for _, mutation := range request.Mutations {
			key := stateKey(mutation.State, mutation.Key)
			if mutation.Delete {
				if err := batch.Delete(key, nil); err != nil {
					return false, err
				}
			} else if err := batch.Set(key, mutation.Value, nil); err != nil {
				return false, err
			}
		}
		for _, checkpoint := range commitCheckpoints(request) {
			if err := batch.Set(checkpointKey(checkpoint), uint64Bytes(checkpoint.Position), nil); err != nil {
				return false, err
			}
		}
		if err := batch.Set(marker, append([]byte{1}, request.Result...), nil); err != nil {
			return false, err
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return false, err
		}
		return true, nil
	}
	store.CommitResult = func(ctx context.Context, module ModuleID, batchID BatchID) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		value, found, err := get(batchKey(module, batchID))
		if err != nil || !found {
			return nil, found, err
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
		value, _, err := get(checkpointKey(cursor))
		return decodeUint64(value), err
	}
	store.GetMetadata = func(ctx context.Context, module ModuleID, key []byte) ([]byte, bool, error) {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return nil, false, err
		}
		return get(metadataKey(module, key))
	}
	store.SetMetadata = func(ctx context.Context, module ModuleID, key, value []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return err
		}
		return db.Set(metadataKey(module, key), value, pebble.Sync)
	}
	store.ResetDerived = func(ctx context.Context, module ModuleID) error {
		mu.Lock()
		defer mu.Unlock()
		if err := check(ctx); err != nil {
			return err
		}
		batch := db.NewBatch()
		defer batch.Close()
		for _, prefix := range [][]byte{stateModulePrefix(module), checkpointModulePrefix(module), batchModulePrefix(module)} {
			if err := batch.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
				return err
			}
		}
		return batch.Commit(pebble.Sync)
	}
	store.Snapshot = func(ctx context.Context, destination string) error {
		mu.RLock()
		defer mu.RUnlock()
		if err := check(ctx); err != nil {
			return err
		}
		if err := prepareSnapshotDestination(destination); err != nil {
			return err
		}
		return db.Checkpoint(destination, pebble.WithFlushedWAL())
	}
	store.Close = func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil
		}
		closed = true
		return db.Close()
	}
	return store, nil
}
