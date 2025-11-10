package friends

import (
	"github.com/cockroachdb/pebble"

	"golitter/internal/types"
)

// Store - Interface for Friendship Tasks
type Store interface {
	ProcessEvents()
	AddFriendRequest(potentialFriend types.UserID)
	CancelFriendRequest(potentialFriend types.UserID)
	AcceptFriendRequest(potentialFriend types.UserID) // adds both ways into friends
	Unfriend(currentFriend types.UserID)
	GetFriends() []types.UserID
	IsFriends(user types.UserID) bool
	FriendsCount() int
}

// backingStore is the internal interface for low-level storage operations
type backingStore interface {
	friendsAddBidirectional(a, b types.UserID) error
	friendsRemoveBidirectional(a, b types.UserID) error
	friendsCount(u types.UserID) (int, error)
	isFriends(a, b types.UserID) (bool, error)
	getFriends(u types.UserID) ([]types.UserID, error)
	outgoingAdd(from, to types.UserID) error
	incomingAdd(from, to types.UserID) error
	outgoingRemove(from, to types.UserID) error
	incomingRemove(from, to types.UserID) error
}

// Task represents a user-scoped friendship store
type Task struct {
	userID types.UserID
	store  backingStore
}

// NewMemStore creates a new in-memory friends store for a user
// Note: Each call creates a new backing store. For shared storage, use NewMemStoreWithBacking.
func NewMemStore(userID types.UserID) Store {
	return &Task{
		userID: userID,
		store:  newMemBackingStore(),
	}
}

// NewMemStoreWithBacking creates a new in-memory friends store for a user with a shared backing store
func NewMemStoreWithBacking(userID types.UserID, backing *memBackingStore) Store {
	return &Task{
		userID: userID,
		store:  backing,
	}
}

// NewPebbleStore creates a new Pebble-backed friends store for a user
func NewPebbleStore(userID types.UserID, db *pebble.DB) Store {
	return &Task{
		userID: userID,
		store:  newPebbleBackingStore(db),
	}
}

// ProcessEvents processes pending events (placeholder for future event processing)
func (t *Task) ProcessEvents() {
	// TODO: Implement event processing logic
	// This could process incoming friend requests, unfriend events, etc.
}

// AddFriendRequest adds an outgoing friend request from the current user to potentialFriend
func (t *Task) AddFriendRequest(potentialFriend types.UserID) {
	_ = t.store.outgoingAdd(t.userID, potentialFriend)
	_ = t.store.incomingAdd(t.userID, potentialFriend)
}

// CancelFriendRequest cancels an outgoing friend request
func (t *Task) CancelFriendRequest(potentialFriend types.UserID) {
	_ = t.store.outgoingRemove(t.userID, potentialFriend)
	_ = t.store.incomingRemove(t.userID, potentialFriend)
}

// AcceptFriendRequest accepts an incoming friend request and establishes bidirectional friendship
func (t *Task) AcceptFriendRequest(potentialFriend types.UserID) {
	// Remove from incoming/outgoing requests
	_ = t.store.incomingRemove(potentialFriend, t.userID)
	_ = t.store.outgoingRemove(potentialFriend, t.userID)
	// Add bidirectional friendship
	_ = t.store.friendsAddBidirectional(t.userID, potentialFriend)
}

// Unfriend removes the bidirectional friendship
func (t *Task) Unfriend(currentFriend types.UserID) {
	_ = t.store.friendsRemoveBidirectional(t.userID, currentFriend)
}

// GetFriends returns all friends of the current user
func (t *Task) GetFriends() []types.UserID {
	friends, _ := t.store.getFriends(t.userID)
	return friends
}

// IsFriends checks if the current user is friends with the given user
func (t *Task) IsFriends(user types.UserID) bool {
	isFriend, _ := t.store.isFriends(t.userID, user)
	return isFriend
}

// FriendsCount returns the number of friends for the current user
func (t *Task) FriendsCount() int {
	count, _ := t.store.friendsCount(t.userID)
	return count
}
