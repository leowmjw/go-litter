package store

import (
	"sort"
	"sync"

	"github.com/emirpasic/gods/v2/maps/treemap"
	"github.com/emirpasic/gods/v2/sets/treeset"
)

type MemStore struct {
	mu sync.RWMutex

	profiles  map[string]Profile
	friends   map[string]*treeset.Set[string]
	outgoing  map[string]*treeset.Set[string]
	incoming  map[string]*treeset.Set[string]
	views     map[string]*treemap.Map[int64, int64]
	posts     map[string]*treemap.Map[int64, Post]
	nextPost  map[string]int64
	usersSeen *treeset.Set[string]

	totalUsers       int64
	totalFriendEdges int64
}

func NewMem() *MemStore {
	return &MemStore{
		profiles:  map[string]Profile{},
		friends:   map[string]*treeset.Set[string]{},
		outgoing:  map[string]*treeset.Set[string]{},
		incoming:  map[string]*treeset.Set[string]{},
		views:     map[string]*treemap.Map[int64, int64]{},
		posts:     map[string]*treemap.Map[int64, Post]{},
		nextPost:  map[string]int64{},
		usersSeen: treeset.New[string](),
	}
}

func sset(m map[string]*treeset.Set[string], u string) *treeset.Set[string] {
	if m[u] == nil {
		m[u] = treeset.New[string]()
	}
	return m[u]
}

func (s *MemStore) PutProfile(u UserID, p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[u]; !ok {
		s.totalUsers++
		s.usersSeen.Add(u)
	}
	s.profiles[u] = p
	return nil
}
func (s *MemStore) GetPwdHash(u UserID) (int, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[u]
	if !ok {
		return 0, false, nil
	}
	return p.PwdHash, true, nil
}
func (s *MemStore) GetProfileSubset(u UserID) (Profile, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[u]
	return p, ok, nil
}

func (s *MemStore) FriendsAddBidirectional(a, b UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.friends, a).Add(b)
	sset(s.friends, b).Add(a)
	s.totalFriendEdges += 2
	return nil
}
func (s *MemStore) FriendsRemoveBidirectional(a, b UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.friends[a] != nil && s.friends[a].Contains(b) {
		s.friends[a].Remove(b)
		s.totalFriendEdges--
	}
	if s.friends[b] != nil && s.friends[b].Contains(a) {
		s.friends[b].Remove(a)
		s.totalFriendEdges--
	}
	return nil
}
func (s *MemStore) FriendsCount(u UserID) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.friends[u] == nil {
		return 0, nil
	}
	return int64(s.friends[u].Size()), nil
}
func (s *MemStore) FriendsPage(u UserID, start string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.friends[u]
	if set == nil {
		return nil, nil
	}
	out := make([]string, 0, limit)
	it := set.Iterator()
	for it.Begin(); it.Next(); {
		v := it.Value()
		if v <= start {
			continue
		}
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (s *MemStore) IsFriends(a, b UserID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.friends[a] == nil {
		return false, nil
	}
	return s.friends[a].Contains(b), nil
}

func (s *MemStore) OutgoingAdd(from, to UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.outgoing, from).Add(to)
	return nil
}
func (s *MemStore) IncomingAdd(from, to UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.incoming, to).Add(from)
	return nil
}
func (s *MemStore) OutgoingRemove(from, to UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outgoing[from] != nil {
		s.outgoing[from].Remove(to)
	}
	return nil
}
func (s *MemStore) IncomingRemove(from, to UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incoming[to] != nil {
		s.incoming[to].Remove(from)
	}
	return nil
}
func (s *MemStore) GetOutgoing(u UserID, start string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.outgoing[u]
	if set == nil {
		return nil, nil
	}
	out := make([]string, 0, limit)
	it := set.Iterator()
	for it.Begin(); it.Next(); {
		v := it.Value()
		if v <= start {
			continue
		}
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (s *MemStore) GetIncoming(u UserID, start string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.incoming[u]
	if set == nil {
		return nil, nil
	}
	out := make([]string, 0, limit)
	it := set.Iterator()
	for it.Begin(); it.Next(); {
		v := it.Value()
		if v <= start {
			continue
		}
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemStore) IncProfileViews(u UserID, hour int64, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.views[u] == nil {
		s.views[u] = treemap.New[int64, int64]()
	}
	cur, _ := s.views[u].Get(hour)
	s.views[u].Put(hour, cur+delta)
	return nil
}
func (s *MemStore) SumProfileViews(u UserID, startHour, endHour int64) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.views[u]
	if m == nil {
		return 0, nil
	}
	var sum int64
	it := m.Iterator()
	for it.Begin(); it.Next(); {
		k := it.Key()
		if k < startHour || k > endHour {
			continue
		}
		sum += it.Value()
	}
	return sum, nil
}

func (s *MemStore) NextPostID(u UserID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nextPost[u]; !ok {
		s.nextPost[u] = 1 << 62
	}
	id := s.nextPost[u]
	s.nextPost[u]--
	return id, nil
}
func (s *MemStore) PutPost(u UserID, id int64, p Post) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.posts[u] == nil {
		s.posts[u] = treemap.New[int64, Post]()
	}
	s.posts[u].Put(id, p)
	return nil
}

func (s *MemStore) PostsCount(u UserID) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.posts[u] == nil {
		return 0, nil
	}
	return int64(s.posts[u].Size()), nil
}
func (s *MemStore) PostsRangeFrom(u UserID, start int64, limit int) ([]int64, []Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.posts[u]
	if m == nil {
		return nil, nil, nil
	}
	keys := make([]int64, 0, m.Size())
	vals := make([]Post, 0, m.Size())
	it := m.Iterator()
	for it.Begin(); it.Next(); {
		k := it.Key()
		if k > start {
			continue
		}
		keys = append(keys, k)
		vals = append(vals, it.Value())
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] > keys[j] })
	if len(keys) > limit {
		keys = keys[:limit]
		vals = vals[:limit]
	}
	return keys, vals, nil
}

func (s *MemStore) TotalUsers() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalUsers, nil
}
func (s *MemStore) TotalFriendEdges() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalFriendEdges, nil
}
func (s *MemStore) TopKByViews(k int) ([]UserID, []int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type pair struct {
		u string
		c int64
	}
	arr := []pair{}
	for u, m := range s.views {
		var sum int64
		it := m.Iterator()
		for it.Begin(); it.Next(); {
			sum += it.Value()
		}
		arr = append(arr, pair{u, sum})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].c > arr[j].c })
	if len(arr) > k {
		arr = arr[:k]
	}
	users := make([]UserID, len(arr))
	counts := make([]int64, len(arr))
	for i, p := range arr {
		users[i], counts[i] = p.u, p.c
	}
	return users, counts, nil
}
func (s *MemStore) TopKByFriendCount(k int) ([]UserID, []int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type pair struct {
		u string
		c int64
	}
	arr := []pair{}
	it := s.usersSeen.Iterator()
	for it.Begin(); it.Next(); {
		u := it.Value()
		var c int64
		if s.friends[u] != nil {
			c = int64(s.friends[u].Size())
		}
		arr = append(arr, pair{u, c})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].c > arr[j].c })
	if len(arr) > k {
		arr = arr[:k]
	}
	users := make([]UserID, len(arr))
	counts := make([]int64, len(arr))
	for i, p := range arr {
		users[i], counts[i] = p.u, p.c
	}
	return users, counts, nil
}
func (s *MemStore) PendingRequestCounts(u UserID) (int64, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out, in int64
	if s.outgoing[u] != nil {
		out = int64(s.outgoing[u].Size())
	}
	if s.incoming[u] != nil {
		in = int64(s.incoming[u].Size())
	}
	return out, in, nil
}
