package friends

import (
	"sync"

	"github.com/emirpasic/gods/v2/sets/treeset"

	"golitter/internal/types"
)

// memRelations implements in-memory friendship relationships using gods library
type memRelations struct {
	mu       sync.RWMutex
	friends  map[types.UserID]*treeset.Set[string]
	outgoing map[types.UserID]*treeset.Set[string]
	incoming map[types.UserID]*treeset.Set[string]
}

func newMemRelations() *memRelations {
	return &memRelations{
		friends:  make(map[types.UserID]*treeset.Set[string]),
		outgoing: make(map[types.UserID]*treeset.Set[string]),
		incoming: make(map[types.UserID]*treeset.Set[string]),
	}
}

func sset(m map[types.UserID]*treeset.Set[string], u types.UserID) *treeset.Set[string] {
	if m[u] == nil {
		m[u] = treeset.New[string]()
	}
	return m[u]
}

func (m *memRelations) addRelation(a, b types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sset(m.friends, a).Add(string(b))
	sset(m.friends, b).Add(string(a))
	return nil
}

func (m *memRelations) removeRelation(a, b types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.friends[a] != nil {
		m.friends[a].Remove(string(b))
		if m.friends[a].Empty() {
			delete(m.friends, a)
		}
	}
	if m.friends[b] != nil {
		m.friends[b].Remove(string(a))
		if m.friends[b].Empty() {
			delete(m.friends, b)
		}
	}
	return nil
}

func (m *memRelations) countRelations(u types.UserID) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.friends[u] == nil {
		return 0, nil
	}
	return m.friends[u].Size(), nil
}

func (m *memRelations) checkRelation(a, b types.UserID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.friends[a] == nil {
		return false, nil
	}
	return m.friends[a].Contains(string(b)), nil
}

func (m *memRelations) listRelations(u types.UserID) ([]types.UserID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.friends[u]
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

func (m *memRelations) addOutgoingRequest(from, to types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sset(m.outgoing, from).Add(string(to))
	return nil
}

func (m *memRelations) addIncomingRequest(from, to types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sset(m.incoming, to).Add(string(from))
	return nil
}

func (m *memRelations) removeOutgoingRequest(from, to types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.outgoing[from] != nil {
		m.outgoing[from].Remove(string(to))
		if m.outgoing[from].Empty() {
			delete(m.outgoing, from)
		}
	}
	return nil
}

func (m *memRelations) removeIncomingRequest(from, to types.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.incoming[to] != nil {
		m.incoming[to].Remove(string(from))
		if m.incoming[to].Empty() {
			delete(m.incoming, to)
		}
	}
	return nil
}

func (m *memRelations) hasIncomingRequest(from, to types.UserID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.incoming[to] == nil {
		return false, nil
	}
	return m.incoming[to].Contains(string(from)), nil
}

func (m *memRelations) hasOutgoingRequest(from, to types.UserID) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.outgoing[from] == nil {
		return false, nil
	}
	return m.outgoing[from].Contains(string(to)), nil
}

func (m *memRelations) listOutgoingRequests(u types.UserID) ([]types.UserID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.outgoing[u]
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

func (m *memRelations) listIncomingRequests(u types.UserID) ([]types.UserID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := m.incoming[u]
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
