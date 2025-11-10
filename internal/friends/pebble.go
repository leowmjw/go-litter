package friends

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble"

	"golitter/internal/types"
)

// pebbleBackingStore implements Pebble-backed storage
type pebbleBackingStore struct {
	db *pebble.DB
}

func newPebbleBackingStore(db *pebble.DB) *pebbleBackingStore {
	return &pebbleBackingStore{db: db}
}

// Key encoding helpers
func kFriendsSet(u, v types.UserID) []byte {
	return []byte(fmt.Sprintf("friends/%s/set/%s", u, v))
}

func kFriendsSize(u types.UserID) []byte {
	return []byte(fmt.Sprintf("friends/%s/size", u))
}

func pfxFriends(u types.UserID) []byte {
	return []byte(fmt.Sprintf("friends/%s/set/", u))
}

func kOut(u, v types.UserID) []byte {
	return []byte(fmt.Sprintf("out/%s/set/%s", u, v))
}

func kIn(u, v types.UserID) []byte {
	return []byte(fmt.Sprintf("in/%s/set/%s", v, u))
}

func beI64(v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

func rdI64(b []byte) int64 {
	if len(b) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

func nextPrefix(pfx []byte) []byte {
	end := append([]byte{}, pfx...)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end[:i+1]
		}
	}
	return nil
}

func (s *pebbleBackingStore) friendsAddBidirectional(a, b types.UserID) error {
	bch := s.db.NewBatch()
	defer bch.Close()
	_ = bch.Set(kFriendsSet(a, b), nil, nil)
	_ = bch.Set(kFriendsSet(b, a), nil, nil)
	_ = bch.Merge(kFriendsSize(a), beI64(+1), nil)
	_ = bch.Merge(kFriendsSize(b), beI64(+1), nil)
	return bch.Commit(pebble.Sync)
}

func (s *pebbleBackingStore) friendsRemoveBidirectional(a, b types.UserID) error {
	bch := s.db.NewBatch()
	defer bch.Close()
	_ = bch.Delete(kFriendsSet(a, b), nil)
	_ = bch.Delete(kFriendsSet(b, a), nil)
	_ = bch.Merge(kFriendsSize(a), beI64(-1), nil)
	_ = bch.Merge(kFriendsSize(b), beI64(-1), nil)
	return bch.Commit(pebble.Sync)
}

func (s *pebbleBackingStore) friendsCount(u types.UserID) (int, error) {
	v, c, err := s.db.Get(kFriendsSize(u))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return int(rdI64(v)), nil
}

func (s *pebbleBackingStore) isFriends(a, b types.UserID) (bool, error) {
	_, c, err := s.db.Get(kFriendsSet(a, b))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = c.Close()
	return true, nil
}

func (s *pebbleBackingStore) getFriends(u types.UserID) ([]types.UserID, error) {
	pfx := pfxFriends(u)
	ub := nextPrefix(pfx)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var result []types.UserID
	for ok := it.First(); ok; ok = it.Next() {
		val := string(bytes.TrimPrefix(it.Key(), pfx))
		result = append(result, types.UserID(val))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result, nil
}

func (s *pebbleBackingStore) outgoingAdd(from, to types.UserID) error {
	return s.db.Set(kOut(from, to), nil, pebble.Sync)
}

func (s *pebbleBackingStore) incomingAdd(from, to types.UserID) error {
	return s.db.Set(kIn(from, to), nil, pebble.Sync)
}

func (s *pebbleBackingStore) outgoingRemove(from, to types.UserID) error {
	return s.db.Delete(kOut(from, to), pebble.Sync)
}

func (s *pebbleBackingStore) incomingRemove(from, to types.UserID) error {
	return s.db.Delete(kIn(from, to), pebble.Sync)
}

