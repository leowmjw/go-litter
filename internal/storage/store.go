package storage

import (
	"context"
	"errors"
)

var (
	ErrClosed              = errors.New("store closed")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different payload")
	ErrInvalidLimit        = errors.New("limit must be positive")
	ErrCorruptStore        = errors.New("corrupt store value")
	ErrSnapshotUnsupported = errors.New("snapshot unsupported")
)

type ModuleID string
type DepotID string
type StateID string
type TopologyID string
type BatchID string

type DepotPartition struct {
	Module    ModuleID
	Depot     DepotID
	Partition uint32
}

type StatePartition struct {
	Module    ModuleID
	State     StateID
	Partition uint32
}

type Record struct {
	Position uint64
	Payload  []byte
}

type Entry struct {
	Key   []byte
	Value []byte
}

type Mutation struct {
	State  StatePartition
	Key    []byte
	Value  []byte
	Delete bool
}

type Cursor struct {
	Module    ModuleID
	Topology  TopologyID
	Depot     DepotID
	Partition uint32
	Position  uint64
}

type CommitRequest struct {
	Module      ModuleID
	BatchID     BatchID
	Mutations   []Mutation
	Checkpoint  Cursor
	Checkpoints []Cursor
	Result      []byte
}

type Store struct {
	Append       func(context.Context, DepotPartition, string, []byte) (Record, bool, error)
	Read         func(context.Context, DepotPartition, uint64, int) ([]Record, error)
	HighWater    func(context.Context, DepotPartition) (uint64, error)
	GetState     func(context.Context, StatePartition, []byte) ([]byte, bool, error)
	ScanState    func(context.Context, StatePartition, []byte, []byte, int, bool) ([]Entry, error)
	Commit       func(context.Context, CommitRequest) (bool, error)
	CommitResult func(context.Context, ModuleID, BatchID) ([]byte, bool, error)
	Checkpoint   func(context.Context, Cursor) (uint64, error)
	GetMetadata  func(context.Context, ModuleID, []byte) ([]byte, bool, error)
	SetMetadata  func(context.Context, ModuleID, []byte, []byte) error
	ResetDerived func(context.Context, ModuleID) error
	Snapshot     func(context.Context, string) error
	Close        func() error
}

func commitCheckpoints(request CommitRequest) []Cursor {
	if len(request.Checkpoints) > 0 {
		return request.Checkpoints
	}
	if request.Checkpoint.Module != "" {
		return []Cursor{request.Checkpoint}
	}
	return nil
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
