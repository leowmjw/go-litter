package friends

import (
	"errors"
	"fmt"

	"golitter/internal/types"
)

var (
	ErrNoIncomingRequest    = errors.New("no incoming friend request found")
	ErrAlreadyFriends       = errors.New("users are already friends")
	ErrNotFriends           = errors.New("users are not friends")
	ErrSelfFriendship       = errors.New("cannot perform friendship operation with self")
	ErrRequestAlreadyExists = errors.New("friend request already exists")
)

type ValidationError struct {
	Op    string
	From  types.UserID
	To    types.UserID
	Cause error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("friendship %s(%s -> %s): %v", e.Op, e.From, e.To, e.Cause)
}

func (e *ValidationError) Unwrap() error {
	return e.Cause
}
