package friends

import (
	"github.com/cockroachdb/pebble/v2"

	"golitter/internal/types"
)

// Relations represents the data layer for friendship relationships.
// These are the low-level operations on friendship data.
type Relations struct {
	AddRelation           func(a, b types.UserID) error
	RemoveRelation        func(a, b types.UserID) error
	CountRelations        func(u types.UserID) (int, error)
	CheckRelation         func(a, b types.UserID) (bool, error)
	ListRelations         func(u types.UserID) ([]types.UserID, error)
	AddOutgoingRequest    func(from, to types.UserID) error
	AddIncomingRequest    func(from, to types.UserID) error
	RemoveOutgoingRequest func(from, to types.UserID) error
	RemoveIncomingRequest func(from, to types.UserID) error
	HasIncomingRequest    func(from, to types.UserID) (bool, error)
}

// Friendship provides general friendship operations that work with any user.
// All methods require explicit userID parameters.
type Friendship struct {
	relations Relations

	// Business logic operations (explicit userID required)
	AddFriendRequest    func(from, to types.UserID) error
	CancelFriendRequest func(from, to types.UserID) error
	AcceptFriendRequest func(from, to types.UserID) error
	Unfriend            func(a, b types.UserID) error
	GetFriends          func(user types.UserID) ([]types.UserID, error)
	IsFriends           func(a, b types.UserID) (bool, error)
	FriendsCount        func(user types.UserID) (int, error)

	// Analytics operations (no userID needed)
	TotalFriendships func() (int64, error)
}

// UserFriendship provides user-scoped friendship operations.
// The userID is captured in the struct, so methods don't require it.
type UserFriendship struct {
	userID     types.UserID
	friendship *Friendship
}

// NewMem creates a new in-memory Friendship instance.
func NewMem() *Friendship {
	mem := newMemRelations()
	relations := Relations{
		AddRelation:           mem.addRelation,
		RemoveRelation:        mem.removeRelation,
		CountRelations:        mem.countRelations,
		CheckRelation:         mem.checkRelation,
		ListRelations:         mem.listRelations,
		AddOutgoingRequest:    mem.addOutgoingRequest,
		AddIncomingRequest:    mem.addIncomingRequest,
		RemoveOutgoingRequest: mem.removeOutgoingRequest,
		RemoveIncomingRequest: mem.removeIncomingRequest,
		HasIncomingRequest:    mem.hasIncomingRequest,
	}

	f := &Friendship{relations: relations}

	// Wire business logic operations
	f.AddFriendRequest = func(from, to types.UserID) error {
		f.relations.AddOutgoingRequest(from, to)
		f.relations.AddIncomingRequest(from, to)
		return nil
	}

	f.CancelFriendRequest = func(from, to types.UserID) error {
		f.relations.RemoveOutgoingRequest(from, to)
		f.relations.RemoveIncomingRequest(from, to)
		return nil
	}

	f.AcceptFriendRequest = func(from, to types.UserID) error {
		// Only accept if there's an incoming request
		hasRequest, err := f.relations.HasIncomingRequest(from, to)
		if err != nil {
			return err
		}
		if !hasRequest {
			// No request to accept, just return (safe no-op)
			return nil
		}
		// Remove from requests
		f.relations.RemoveIncomingRequest(from, to)
		f.relations.RemoveOutgoingRequest(from, to)
		// Add bidirectional friendship
		f.relations.AddRelation(from, to)
		return nil
	}

	f.Unfriend = func(a, b types.UserID) error {
		// Only remove if the relation exists
		exists, err := f.relations.CheckRelation(a, b)
		if err != nil {
			return err
		}
		if !exists {
			// Not friends, safe no-op
			return nil
		}
		return f.relations.RemoveRelation(a, b)
	}

	f.GetFriends = f.relations.ListRelations
	f.IsFriends = f.relations.CheckRelation
	f.FriendsCount = f.relations.CountRelations

	// Analytics (placeholder - can be implemented later)
	f.TotalFriendships = func() (int64, error) {
		return 0, nil // TODO: implement if needed
	}

	return f
}

// NewPebble creates a new Pebble-backed Friendship instance.
func NewPebble(db *pebble.DB) *Friendship {
	peb := newPebbleRelations(db)
	relations := Relations{
		AddRelation:           peb.addRelation,
		RemoveRelation:        peb.removeRelation,
		CountRelations:        peb.countRelations,
		CheckRelation:         peb.checkRelation,
		ListRelations:         peb.listRelations,
		AddOutgoingRequest:    peb.addOutgoingRequest,
		AddIncomingRequest:    peb.addIncomingRequest,
		RemoveOutgoingRequest: peb.removeOutgoingRequest,
		RemoveIncomingRequest: peb.removeIncomingRequest,
		HasIncomingRequest:    peb.hasIncomingRequest,
	}

	f := &Friendship{relations: relations}

	// Wire business logic operations (same as mem)
	f.AddFriendRequest = func(from, to types.UserID) error {
		f.relations.AddOutgoingRequest(from, to)
		f.relations.AddIncomingRequest(from, to)
		return nil
	}

	f.CancelFriendRequest = func(from, to types.UserID) error {
		f.relations.RemoveOutgoingRequest(from, to)
		f.relations.RemoveIncomingRequest(from, to)
		return nil
	}

	f.AcceptFriendRequest = func(from, to types.UserID) error {
		// Only accept if there's an incoming request
		hasRequest, err := f.relations.HasIncomingRequest(from, to)
		if err != nil {
			return err
		}
		if !hasRequest {
			// No request to accept, just return (safe no-op)
			return nil
		}
		// Remove from requests
		f.relations.RemoveIncomingRequest(from, to)
		f.relations.RemoveOutgoingRequest(from, to)
		// Add bidirectional friendship
		f.relations.AddRelation(from, to)
		return nil
	}

	f.Unfriend = func(a, b types.UserID) error {
		// Only remove if the relation exists
		exists, err := f.relations.CheckRelation(a, b)
		if err != nil {
			return err
		}
		if !exists {
			// Not friends, safe no-op
			return nil
		}
		return f.relations.RemoveRelation(a, b)
	}

	f.GetFriends = f.relations.ListRelations
	f.IsFriends = f.relations.CheckRelation
	f.FriendsCount = f.relations.CountRelations

	// Analytics (placeholder)
	f.TotalFriendships = func() (int64, error) {
		return 0, nil // TODO: implement if needed
	}

	return f
}

// ForUser returns a UserFriendship instance scoped to the given user.
// Methods on UserFriendship don't require userID parameters.
func (f *Friendship) ForUser(userID types.UserID) *UserFriendship {
	return &UserFriendship{
		userID:     userID,
		friendship: f,
	}
}

// AddFriendRequest sends a friend request from the scoped user to the given user.
func (uf *UserFriendship) AddFriendRequest(to types.UserID) error {
	return uf.friendship.AddFriendRequest(uf.userID, to)
}

// CancelFriendRequest cancels a friend request from the scoped user to the given user.
func (uf *UserFriendship) CancelFriendRequest(to types.UserID) error {
	return uf.friendship.CancelFriendRequest(uf.userID, to)
}

// AcceptFriendRequest accepts a friend request from the given user.
func (uf *UserFriendship) AcceptFriendRequest(from types.UserID) error {
	return uf.friendship.AcceptFriendRequest(from, uf.userID)
}

// Unfriend removes the friendship between the scoped user and the given user.
func (uf *UserFriendship) Unfriend(friend types.UserID) error {
	return uf.friendship.Unfriend(uf.userID, friend)
}

// GetFriends returns all friends of the scoped user.
func (uf *UserFriendship) GetFriends() ([]types.UserID, error) {
	return uf.friendship.GetFriends(uf.userID)
}

// IsFriends checks if the scoped user is friends with the given user.
func (uf *UserFriendship) IsFriends(friend types.UserID) (bool, error) {
	return uf.friendship.IsFriends(uf.userID, friend)
}

// FriendsCount returns the number of friends for the scoped user.
func (uf *UserFriendship) FriendsCount() (int, error) {
	return uf.friendship.FriendsCount(uf.userID)
}
