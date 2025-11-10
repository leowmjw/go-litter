package friends

import (
	"sync"

	"github.com/emirpasic/gods/v2/sets/treeset"

	"golitter/internal/types"
)

// memBackingStore implements in-memory storage using gods library
type memBackingStore struct {
	mu       sync.RWMutex
	friends  map[types.UserID]*treeset.Set[string]
	outgoing map[types.UserID]*treeset.Set[string]
	incoming map[types.UserID]*treeset.Set[string]
}

func newMemBackingStore() *memBackingStore {
	return &memBackingStore{
		friends:  make(map[types.UserID]*treeset.Set[string]),
		outgoing: make(map[types.UserID]*treeset.Set[string]),
		incoming: make(map[types.UserID]*treeset.Set[string]),
	}
}

// NewMemBackingStore creates a new shared in-memory backing store
func NewMemBackingStore() *memBackingStore {
	return newMemBackingStore()
}

func sset(m map[types.UserID]*treeset.Set[string], u types.UserID) *treeset.Set[string] {
	if m[u] == nil {
		m[u] = treeset.New[string]()
	}
	return m[u]
}

func (s *memBackingStore) friendsAddBidirectional(a, b types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.friends, a).Add(string(b))
	sset(s.friends, b).Add(string(a))
	return nil
}

func (s *memBackingStore) friendsRemoveBidirectional(a, b types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.friends[a] != nil {
		s.friends[a].Remove(string(b))
	}
	if s.friends[b] != nil {
		s.friends[b].Remove(string(a))
	}
	return nil
}

func (s *memBackingStore) friendsCount(u types.UserID) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.friends[u] == nil {
		return 0, nil
	}
	return s.friends[u].Size(), nil
}

func (s *memBackingStore) isFriends(a, b types.UserID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.friends[a] == nil {
		return false, nil
	}
	return s.friends[a].Contains(string(b)), nil
}

func (s *memBackingStore) getFriends(u types.UserID) ([]types.UserID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := s.friends[u]
	if set == nil {
		return nil, nil
	}
	result := make([]types.UserID, 0, set.Size())
	it := set.Iterator()
	for it.Begin(); it.Next(); {
		result = append(result, types.UserID(it.Value()))
	}
	return result, nil
}

func (s *memBackingStore) outgoingAdd(from, to types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.outgoing, from).Add(string(to))
	return nil
}

func (s *memBackingStore) incomingAdd(from, to types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sset(s.incoming, to).Add(string(from))
	return nil
}

func (s *memBackingStore) outgoingRemove(from, to types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outgoing[from] != nil {
		s.outgoing[from].Remove(string(to))
	}
	return nil
}

func (s *memBackingStore) incomingRemove(from, to types.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incoming[to] != nil {
		s.incoming[to].Remove(string(from))
	}
	return nil
}

