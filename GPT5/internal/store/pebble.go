package store

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/cockroachdb/pebble"
)

type PebbleStore struct{ DB *pebble.DB }

func NewPebble(db *pebble.DB) *PebbleStore { return &PebbleStore{DB: db} }

// Profiles
func (s *PebbleStore) PutProfile(u UserID, p Profile) error {
	// Check if profile already exists (use email as indicator since it's required)
	_, c, err := s.DB.Get(KProf(u, "email"))
	isNew := err == pebble.ErrNotFound
	if c != nil {
		c.Close()
	}

	b := s.DB.NewBatch()
	defer b.Close()
	_ = b.Set(KProf(u, "email"), []byte(p.Email), nil)
	_ = b.Set(KProf(u, "displayName"), []byte(p.DisplayName), nil)
	_ = b.Set(KProf(u, "bio"), []byte(p.Bio), nil)
	_ = b.Set(KProf(u, "location"), []byte(p.Location), nil)
	_ = b.Set(KProf(u, "profilePic"), []byte(p.ProfilePic), nil)
	_ = b.Set(KProf(u, "joinedAtMs"), BeI64(p.JoinedAtMs), nil)
	_ = b.Set(KProf(u, "pwdHash"), BeI64(int64(p.PwdHash)), nil)
	// Only increment total users if this is a new profile
	if isNew {
		_ = b.Merge(KMetricTotalUsers(), BeI64(1), nil)
	}
	return b.Commit(pebble.Sync)
}
func (s *PebbleStore) GetPwdHash(u UserID) (int, bool, error) {
	v, c, err := s.DB.Get(KProf(u, "pwdHash"))
	if err == pebble.ErrNotFound {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer c.Close()
	return int(RdI64(v)), true, nil
}
func (s *PebbleStore) GetProfileSubset(u UserID) (Profile, bool, error) {
	get := func(f string) ([]byte, bool, error) {
		v, c, e := s.DB.Get(KProf(u, f))
		if e == pebble.ErrNotFound {
			return nil, false, nil
		}
		if e != nil {
			return nil, false, e
		}
		defer c.Close()
		return append([]byte{}, v...), true, nil
	}
	var p Profile
	if b, ok, _ := get("email"); !ok {
		return p, false, nil
	} else {
		p.Email = string(b)
	}
	if b, ok, _ := get("displayName"); ok {
		p.DisplayName = string(b)
	}
	if b, ok, _ := get("bio"); ok {
		p.Bio = string(b)
	}
	if b, ok, _ := get("location"); ok {
		p.Location = string(b)
	}
	if b, ok, _ := get("profilePic"); ok {
		p.ProfilePic = string(b)
	}
	if b, ok, _ := get("joinedAtMs"); ok {
		p.JoinedAtMs = RdI64(b)
	}
	if b, ok, _ := get("pwdHash"); ok {
		p.PwdHash = int(RdI64(b))
	}
	return p, true, nil
}

// Helpers
func pageSet(db *pebble.DB, pfx []byte, start string, limit int) ([]string, error) {
	ub := NextPrefix(pfx)
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	out := make([]string, 0, limit)
	for ok := it.First(); ok; ok = it.Next() {
		val := string(bytes.TrimPrefix(it.Key(), pfx))
		if val <= start {
			continue
		}
		out = append(out, val)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Friends / Requests
func (s *PebbleStore) FriendsAddBidirectional(a, b UserID) error {
	bch := s.DB.NewBatch()
	defer bch.Close()
	_ = bch.Set(KFriendsSet(a, b), nil, nil)
	_ = bch.Set(KFriendsSet(b, a), nil, nil)
	_ = bch.Merge(KFriendsSize(a), BeI64(+1), nil)
	_ = bch.Merge(KFriendsSize(b), BeI64(+1), nil)
	_ = bch.Merge(KMetricTotalFriendEdges(), BeI64(+2), nil)
	return bch.Commit(pebble.Sync)
}
func (s *PebbleStore) FriendsRemoveBidirectional(a, b UserID) error {
	bch := s.DB.NewBatch()
	defer bch.Close()
	_ = bch.Delete(KFriendsSet(a, b), nil)
	_ = bch.Delete(KFriendsSet(b, a), nil)
	_ = bch.Merge(KFriendsSize(a), BeI64(-1), nil)
	_ = bch.Merge(KFriendsSize(b), BeI64(-1), nil)
	_ = bch.Merge(KMetricTotalFriendEdges(), BeI64(-2), nil)
	return bch.Commit(pebble.Sync)
}
func (s *PebbleStore) FriendsCount(u UserID) (int64, error) {
	v, c, err := s.DB.Get(KFriendsSize(u))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return RdI64(v), nil
}
func (s *PebbleStore) IsFriends(a, b UserID) (bool, error) {
	_, c, err := s.DB.Get(KFriendsSet(a, b))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = c.Close()
	return true, nil
}
func (s *PebbleStore) FriendsPage(u UserID, start string, limit int) ([]string, error) {
	return pageSet(s.DB, PfxFriends(u), start, limit)
}
func (s *PebbleStore) OutgoingAdd(from, to UserID) error {
	return s.DB.Set(KOut(from, to), nil, pebble.Sync)
}
func (s *PebbleStore) IncomingAdd(from, to UserID) error {
	return s.DB.Set(KIn(from, to), nil, pebble.Sync)
}
func (s *PebbleStore) OutgoingRemove(from, to UserID) error {
	return s.DB.Delete(KOut(from, to), pebble.Sync)
}
func (s *PebbleStore) IncomingRemove(from, to UserID) error {
	return s.DB.Delete(KIn(from, to), pebble.Sync)
}
func (s *PebbleStore) GetOutgoing(u UserID, start string, limit int) ([]string, error) {
	return pageSet(s.DB, PfxOut(u), start, limit)
}
func (s *PebbleStore) GetIncoming(u UserID, start string, limit int) ([]string, error) {
	return pageSet(s.DB, PfxIn(u), start, limit)
}

// Views
func (s *PebbleStore) IncProfileViews(u UserID, hour int64, delta int64) error {
	return s.DB.Merge(KViews(u, hour), BeI64(delta), pebble.Sync)
}
func (s *PebbleStore) SumProfileViews(u UserID, startHour, endHour int64) (int64, error) {
	lo := KViews(u, startHour)
	hi := KViews(u, endHour)
	ub := append(append([]byte{}, hi...), 0x01)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: ub})
	if err != nil {
		return 0, err
	}
	defer it.Close()
	var sum int64
	for ok := it.First(); ok; ok = it.Next() {
		sum += RdI64(it.Value())
	}
	return sum, nil
}

// Posts
func (s *PebbleStore) NextPostID(u UserID) (int64, error) {
	v, c, err := s.DB.Get(KPostNext(u))
	if err == pebble.ErrNotFound {
		id := int64(1 << 62)
		if err := s.DB.Set(KPostNext(u), BeI64(id-1), pebble.Sync); err != nil {
			return 0, err
		}
		return id, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	cur := RdI64(v)
	if err := s.DB.Set(KPostNext(u), BeI64(cur-1), pebble.Sync); err != nil {
		return 0, err
	}
	return cur, nil
}
func (s *PebbleStore) PutPost(u UserID, id int64, p Post) error {
	b := s.DB.NewBatch()
	defer b.Close()
	_ = b.Set(KPost(u, id), []byte(p.UserID+"\x00"+p.ToUserID+"\x00"+p.Content), nil)
	_ = b.Merge([]byte("metrics/postsTotal"), BeI64(+1), nil)
	return b.Commit(pebble.Sync)
}
func (s *PebbleStore) PostsCount(u UserID) (int64, error) {
	pfx := PfxPosts(u)
	ub := NextPrefix(pfx)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
	if err != nil {
		return 0, err
	}
	defer it.Close()
	var c int64
	for ok := it.First(); ok; ok = it.Next() {
		c++
	}
	return c, nil
}
func (s *PebbleStore) PostsRangeFrom(u UserID, start int64, limit int) ([]int64, []Post, error) {
	pfx := PfxPosts(u)
	ub := NextPrefix(pfx)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
	if err != nil {
		return nil, nil, err
	}
	defer it.Close()
	keys := make([]int64, 0, limit)
	vals := make([]Post, 0, limit)
	for ok := it.Last(); ok; ok = it.Prev() {
		raw := bytes.TrimPrefix(it.Key(), pfx)
		if len(raw) != 8 {
			continue
		}
		id := int64(binary.BigEndian.Uint64(raw))
		if id > start {
			continue
		}
		parts := bytes.SplitN(it.Value(), []byte{0x00}, 3)
		var p Post
		if len(parts) > 0 {
			p.UserID = string(parts[0])
		}
		if len(parts) > 1 {
			p.ToUserID = string(parts[1])
		}
		if len(parts) > 2 {
			p.Content = string(parts[2])
		}
		keys = append(keys, id)
		vals = append(vals, p)
		if len(keys) >= limit {
			break
		}
	}
	return keys, vals, nil
}

// Analytics
func (s *PebbleStore) TotalUsers() (int64, error) {
	v, c, err := s.DB.Get(KMetricTotalUsers())
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return RdI64(v), nil
}
func (s *PebbleStore) TotalFriendEdges() (int64, error) {
	v, c, err := s.DB.Get(KMetricTotalFriendEdges())
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return RdI64(v), nil
}
func (s *PebbleStore) TopKByViews(k int) ([]UserID, []int64, error) {
	pfx := PfxViewsTotal()
	ub := NextPrefix(pfx)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: ub})
	if err != nil {
		return nil, nil, err
	}
	defer it.Close()
	type pair struct {
		u string
		c int64
	}
	acc := []pair{}
	for ok := it.First(); ok; ok = it.Next() {
		u := string(it.Key()[len(pfx):])
		acc = append(acc, pair{u, RdI64(it.Value())})
	}
	for i := 0; i < len(acc); i++ {
		for j := i + 1; j < len(acc); j++ {
			if acc[j].c > acc[i].c {
				acc[i], acc[j] = acc[j], acc[i]
			}
		}
	}
	if len(acc) > k {
		acc = acc[:k]
	}
	users := make([]UserID, len(acc))
	counts := make([]int64, len(acc))
	for i, p := range acc {
		users[i], counts[i] = p.u, p.c
	}
	return users, counts, nil
}
func (s *PebbleStore) TopKByFriendCount(k int) ([]UserID, []int64, error) {
	it, err := s.DB.NewIter(&pebble.IterOptions{})
	if err != nil {
		return nil, nil, err
	}
	defer it.Close()
	type pair struct {
		u string
		c int64
	}
	acc := []pair{}
	for ok := it.First(); ok; ok = it.Next() {
		if !bytes.HasPrefix(it.Key(), []byte("friends/")) || !bytes.HasSuffix(it.Key(), []byte("/size")) {
			continue
		}
		key := string(it.Key())
		var u string
		if _, e := fmt.Sscanf(key, "friends/%s/size", &u); e == nil {
			acc = append(acc, pair{u, RdI64(it.Value())})
		}
	}
	for i := 0; i < len(acc); i++ {
		for j := i + 1; j < len(acc); j++ {
			if acc[j].c > acc[i].c {
				acc[i], acc[j] = acc[j], acc[i]
			}
		}
	}
	if len(acc) > k {
		acc = acc[:k]
	}
	users := make([]UserID, len(acc))
	counts := make([]int64, len(acc))
	for i, p := range acc {
		users[i], counts[i] = p.u, p.c
	}
	return users, counts, nil
}
func (s *PebbleStore) PendingRequestCounts(u UserID) (int64, int64, error) {
	out, err := pageSet(s.DB, PfxOut(u), "", 1<<30)
	if err != nil {
		return 0, 0, err
	}
	in, err2 := pageSet(s.DB, PfxIn(u), "", 1<<30)
	if err2 != nil {
		return 0, 0, err2
	}
	return int64(len(out)), int64(len(in)), nil
}

// Feed retention helper
func (s *PebbleStore) RetainFeedUpTo(part int, upto uint64) error {
	start := FeedKey(part, 0)
	end := FeedKey(part, upto+1)
	return s.DB.DeleteRange(start, end, pebble.Sync)
}
