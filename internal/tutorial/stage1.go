// Package tutorial implements the Rama quick-tutorial progression (Tutorial
// 1-5 concepts). Each stage is a small, independently testable module built
// on the shared internal/stream, internal/microbatch, internal/storage, and
// internal/partition runtime. Tutorial 6 (RamaSpace) lives in
// internal/ramaspace.
package tutorial

import (
	"context"
	"encoding/json"
	"errors"

	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

// Stage 1 - First Module.
//
// Mapping to the Go/Temporal execution model used by this project:
//   - Cluster / execution environment  -> the Go process plus its Temporal
//     worker registration (see internal/controlplane).
//   - Module (deployable executable)   -> a *Stage1Module value returned by
//     NewStage1, i.e. one set of depots, ETLs, and PStates.
//   - Conductor / supervisor           -> the Temporal ModuleLifecycle
//     Workflow (internal/controlplane), which tracks desired task count,
//     heartbeats, and liveness for the module.
//   - Worker                           -> the Go process's Temporal Worker
//     plus the in-process task event loop that actually appends/processes
//     depot records (internal/stream.Runtime).
//
// A module is launched with an explicit, fixed power-of-two task count (see
// internal/partition.New); there is no online resharding.

const (
	Stage1ModuleName storage.ModuleID   = "tutorial-stage1"
	GreetingsDepot   storage.DepotID    = "greetings"
	GreetingsState   storage.StateID    = "greetings"
	GreetingsTopic   storage.TopologyID = "greetings"
)

var ErrInvalidGreeting = errors.New("name and message are required")

// Greeting is the single depot event for Stage 1: the raw source of truth.
type Greeting struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Stage1Module is the "Hello World" module: one depot, one stream ETL, one
// PState, and a point query.
type Stage1Module struct {
	Say         func(context.Context, Greeting) error
	LastMessage func(context.Context, string) (string, bool, error)
	Replay      func(context.Context) error
}

// NewStage1 launches the module with an explicit task count, mirroring a
// Rama module launch with explicit partitions and worker resources.
func NewStage1(store *storage.Store, taskCount uint32) (*Stage1Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}
	state := func(task uint32) storage.StatePartition {
		return storage.StatePartition{Module: Stage1ModuleName, State: GreetingsState, Partition: task}
	}
	runtime, err := stream.New(Stage1ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot: GreetingsDepot, Topology: GreetingsTopic,
			PartitionKey: func(payload []byte) ([]byte, error) {
				var g Greeting
				if err := json.Unmarshal(payload, &g); err != nil {
					return nil, err
				}
				return []byte(g.Name), nil
			},
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				var g Greeting
				if err := json.Unmarshal(record.Payload, &g); err != nil {
					return nil, err
				}
				event.Set(state(task), []byte(g.Name), []byte(g.Message))
				return nil, nil
			},
		},
	)
	if err != nil {
		return nil, err
	}
	module := &Stage1Module{Replay: runtime.Replay}
	module.Say = func(ctx context.Context, g Greeting) error {
		if g.Name == "" || g.Message == "" {
			return ErrInvalidGreeting
		}
		payload, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = runtime.Append(ctx, GreetingsDepot, "", payload)
		return err
	}
	module.LastMessage = func(ctx context.Context, name string) (string, bool, error) {
		value, found, err := store.GetState(ctx, state(choosePartition([]byte(name))), []byte(name))
		return string(value), found, err
	}
	if err := module.Replay(context.Background()); err != nil {
		return nil, err
	}
	return module, nil
}
