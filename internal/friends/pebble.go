package friends

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/cockroachdb/pebble/v2"

	"golitter/internal/types"
)

// pebbleRelations implements Pebble-backed friendship relationships
type pebbleRelations struct {
	db        *pebble.DB
	writeOpts *pebble.WriteOptions
}

func newPebbleRelations(db *pebble.DB, writeOpts *pebble.WriteOptions) *pebbleRelations {
	if writeOpts == nil {
		writeOpts = pebble.Sync
	}
	return &pebbleRelations{db: db, writeOpts: writeOpts}
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

func pfxOut(u types.UserID) []byte {
	return []byte(fmt.Sprintf("out/%s/set/", u))
}

func pfxIn(u types.UserID) []byte {
	return []byte(fmt.Sprintf("in/%s/set/", u))
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

func (p *pebbleRelations) addRelation(a, b types.UserID) error {
	bch := p.db.NewBatch()
	defer bch.Close()
	_ = bch.Set(kFriendsSet(a, b), nil, nil)
	_ = bch.Set(kFriendsSet(b, a), nil, nil)
	_ = bch.Merge(kFriendsSize(a), beI64(+1), nil)
	_ = bch.Merge(kFriendsSize(b), beI64(+1), nil)
	return bch.Commit(p.writeOpts)
}

func (p *pebbleRelations) removeRelation(a, b types.UserID) error {
	bch := p.db.NewBatch()
	defer bch.Close()
	_ = bch.Delete(kFriendsSet(a, b), nil)
	_ = bch.Delete(kFriendsSet(b, a), nil)
	_ = bch.Merge(kFriendsSize(a), beI64(-1), nil)
	_ = bch.Merge(kFriendsSize(b), beI64(-1), nil)
	return bch.Commit(p.writeOpts)
}

func (p *pebbleRelations) countRelations(u types.UserID) (int, error) {
	v, c, err := p.db.Get(kFriendsSize(u))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return int(rdI64(v)), nil
}

func (p *pebbleRelations) checkRelation(a, b types.UserID) (bool, error) {
	_, c, err := p.db.Get(kFriendsSet(a, b))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = c.Close()
	return true, nil
}

func (p *pebbleRelations) listRelations(u types.UserID) ([]types.UserID, error) {
	pfx := pfxFriends(u)
	ub := nextPrefix(pfx)
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
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

func (p *pebbleRelations) addOutgoingRequest(from, to types.UserID) error {
	return p.db.Set(kOut(from, to), nil, p.writeOpts)
}

func (p *pebbleRelations) addIncomingRequest(from, to types.UserID) error {
	return p.db.Set(kIn(from, to), nil, p.writeOpts)
}

func (p *pebbleRelations) removeOutgoingRequest(from, to types.UserID) error {
	return p.db.Delete(kOut(from, to), p.writeOpts)
}

func (p *pebbleRelations) removeIncomingRequest(from, to types.UserID) error {
	return p.db.Delete(kIn(from, to), p.writeOpts)
}

func (p *pebbleRelations) hasIncomingRequest(from, to types.UserID) (bool, error) {
	_, c, err := p.db.Get(kIn(from, to))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = c.Close()
	return true, nil
}

func (p *pebbleRelations) hasOutgoingRequest(from, to types.UserID) (bool, error) {
	_, c, err := p.db.Get(kOut(from, to))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = c.Close()
	return true, nil
}

func (p *pebbleRelations) listOutgoingRequests(u types.UserID) ([]types.UserID, error) {
	pfx := pfxOut(u)
	ub := nextPrefix(pfx)
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
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

func (p *pebbleRelations) listIncomingRequests(u types.UserID) ([]types.UserID, error) {
	pfx := pfxIn(u)
	ub := nextPrefix(pfx)
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
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
