package friends

import (
	"log/slog"

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
	HasOutgoingRequest    func(from, to types.UserID) (bool, error)
	ListOutgoingRequests  func(u types.UserID) ([]types.UserID, error)
	ListIncomingRequests  func(u types.UserID) ([]types.UserID, error)
}

// Friendship provides general friendship operations that work with any user.
// All methods require explicit userID parameters.
type Friendship struct {
	relations Relations
	logger    *slog.Logger

	// Business logic operations (explicit userID required)
	AddFriendRequest    func(from, to types.UserID) error
	CancelFriendRequest func(from, to types.UserID) error
	AcceptFriendRequest func(from, to types.UserID) error
	Unfriend            func(a, b types.UserID) error
	GetFriends          func(user types.UserID) ([]types.UserID, error)
	IsFriends           func(a, b types.UserID) (bool, error)
	FriendsCount        func(user types.UserID) (int, error)
	GetOutgoingRequests func(user types.UserID) ([]types.UserID, error)
	GetIncomingRequests func(user types.UserID) ([]types.UserID, error)

	// Analytics operations (no userID needed)
	TotalFriendships func() (int64, error)
}

// UserFriendship provides user-scoped friendship operations.
// The userID is captured in the struct, so methods don't require it.
type UserFriendship struct {
	userID     types.UserID
	friendship *Friendship
}

// wireBusinessLogic wires up the business logic operations for a Friendship instance.
// This function is shared between NewMem and NewPebble to avoid code duplication.
func wireBusinessLogic(f *Friendship) {
	if f.logger == nil {
		f.logger = slog.Default()
	}

	f.AddFriendRequest = func(from, to types.UserID) error {
		if from == to {
			return &ValidationError{Op: "AddFriendRequest", From: from, To: to, Cause: ErrSelfFriendship}
		}

		hasOutgoing, err := f.relations.HasOutgoingRequest(from, to)
		if err != nil {
			f.logger.Error("failed to check outgoing request", "from", from, "to", to, "error", err)
			return err
		}
		if hasOutgoing {
			return &ValidationError{Op: "AddFriendRequest", From: from, To: to, Cause: ErrRequestAlreadyExists}
		}

		isFriends, err := f.relations.CheckRelation(from, to)
		if err != nil {
			f.logger.Error("failed to check relation", "from", from, "to", to, "error", err)
			return err
		}
		if isFriends {
			return &ValidationError{Op: "AddFriendRequest", From: from, To: to, Cause: ErrAlreadyFriends}
		}

		if err := f.relations.AddOutgoingRequest(from, to); err != nil {
			f.logger.Error("failed to add outgoing request", "from", from, "to", to, "error", err)
			return err
		}
		if err := f.relations.AddIncomingRequest(from, to); err != nil {
			f.logger.Error("failed to add incoming request", "from", from, "to", to, "error", err)
			_ = f.relations.RemoveOutgoingRequest(from, to)
			return err
		}

		f.logger.Info("friend request sent", "from", from, "to", to)
		return nil
	}

	f.CancelFriendRequest = func(from, to types.UserID) error {
		if from == to {
			return &ValidationError{Op: "CancelFriendRequest", From: from, To: to, Cause: ErrSelfFriendship}
		}

		if err := f.relations.RemoveOutgoingRequest(from, to); err != nil {
			f.logger.Error("failed to remove outgoing request", "from", from, "to", to, "error", err)
			return err
		}
		if err := f.relations.RemoveIncomingRequest(from, to); err != nil {
			f.logger.Error("failed to remove incoming request", "from", from, "to", to, "error", err)
			return err
		}

		f.logger.Info("friend request cancelled", "from", from, "to", to)
		return nil
	}

	f.AcceptFriendRequest = func(from, to types.UserID) error {
		if from == to {
			return &ValidationError{Op: "AcceptFriendRequest", From: from, To: to, Cause: ErrSelfFriendship}
		}

		hasRequest, err := f.relations.HasIncomingRequest(from, to)
		if err != nil {
			f.logger.Error("failed to check incoming request", "from", from, "to", to, "error", err)
			return err
		}
		if !hasRequest {
			return &ValidationError{Op: "AcceptFriendRequest", From: from, To: to, Cause: ErrNoIncomingRequest}
		}

		if err := f.relations.RemoveIncomingRequest(from, to); err != nil {
			f.logger.Error("failed to remove incoming request", "from", from, "to", to, "error", err)
			return err
		}
		if err := f.relations.RemoveOutgoingRequest(from, to); err != nil {
			f.logger.Error("failed to remove outgoing request", "from", from, "to", to, "error", err)
			return err
		}
		if err := f.relations.AddRelation(from, to); err != nil {
			f.logger.Error("failed to add relation", "from", from, "to", to, "error", err)
			return err
		}

		f.logger.Info("friend request accepted", "from", from, "to", to)
		return nil
	}

	f.Unfriend = func(a, b types.UserID) error {
		if a == b {
			return &ValidationError{Op: "Unfriend", From: a, To: b, Cause: ErrSelfFriendship}
		}

		exists, err := f.relations.CheckRelation(a, b)
		if err != nil {
			f.logger.Error("failed to check relation", "a", a, "b", b, "error", err)
			return err
		}
		if !exists {
			return &ValidationError{Op: "Unfriend", From: a, To: b, Cause: ErrNotFriends}
		}

		if err := f.relations.RemoveRelation(a, b); err != nil {
			f.logger.Error("failed to remove relation", "a", a, "b", b, "error", err)
			return err
		}

		f.logger.Info("unfriended", "a", a, "b", b)
		return nil
	}

	f.GetFriends = f.relations.ListRelations
	f.IsFriends = f.relations.CheckRelation
	f.FriendsCount = f.relations.CountRelations
	f.GetOutgoingRequests = f.relations.ListOutgoingRequests
	f.GetIncomingRequests = f.relations.ListIncomingRequests

	f.TotalFriendships = func() (int64, error) {
		return 0, nil // TODO: implement if needed
	}
}

// NewMem creates a new in-memory Friendship instance.
func NewMem() *Friendship {
	return NewMemWithLogger(nil)
}

// NewMemWithLogger creates a new in-memory Friendship instance with a custom logger.
func NewMemWithLogger(logger *slog.Logger) *Friendship {
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
		HasOutgoingRequest:    mem.hasOutgoingRequest,
		ListOutgoingRequests:  mem.listOutgoingRequests,
		ListIncomingRequests:  mem.listIncomingRequests,
	}

	f := &Friendship{relations: relations, logger: logger}
	wireBusinessLogic(f)
	return f
}

// NewPebble creates a new Pebble-backed Friendship instance with default write options.
func NewPebble(db *pebble.DB) *Friendship {
	return NewPebbleWithOptions(db, nil, nil)
}

// NewPebbleWithOptions creates a new Pebble-backed Friendship instance with custom options.
func NewPebbleWithOptions(db *pebble.DB, writeOpts *pebble.WriteOptions, logger *slog.Logger) *Friendship {
	if writeOpts == nil {
		writeOpts = pebble.Sync
	}

	peb := newPebbleRelations(db, writeOpts)
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
		HasOutgoingRequest:    peb.hasOutgoingRequest,
		ListOutgoingRequests:  peb.listOutgoingRequests,
		ListIncomingRequests:  peb.listIncomingRequests,
	}

	f := &Friendship{relations: relations, logger: logger}
	wireBusinessLogic(f)
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

// GetOutgoingRequests returns all outgoing friend requests for the scoped user.
func (uf *UserFriendship) GetOutgoingRequests() ([]types.UserID, error) {
	return uf.friendship.GetOutgoingRequests(uf.userID)
}

// GetIncomingRequests returns all incoming friend requests for the scoped user.
func (uf *UserFriendship) GetIncomingRequests() ([]types.UserID, error) {
	return uf.friendship.GetIncomingRequests(uf.userID)
}
