package tutorial

import (
	"context"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/storage"

	"go.temporal.io/sdk/testsuite"
)

func TestStage1HelloWorldInProcess(t *testing.T) {
	ctx := context.Background()
	module, err := NewStage1(storage.NewMemory(), 4)
	if err != nil {
		t.Fatalf("NewStage1: %v", err)
	}
	if _, found, err := module.LastMessage(ctx, "world"); err != nil || found {
		t.Fatalf("unexpected greeting before append: %v, %v", found, err)
	}
	if err := module.Say(ctx, Greeting{Name: "world", Message: "hello"}); err != nil {
		t.Fatalf("Say: %v", err)
	}
	message, found, err := module.LastMessage(ctx, "world")
	if err != nil || !found || message != "hello" {
		t.Fatalf("LastMessage = %q, %v, %v", message, found, err)
	}
	if err := module.Say(ctx, Greeting{Name: "world", Message: "hi again"}); err != nil {
		t.Fatalf("Say update: %v", err)
	}
	message, found, err = module.LastMessage(ctx, "world")
	if err != nil || !found || message != "hi again" {
		t.Fatalf("LastMessage after update = %q, %v, %v", message, found, err)
	}
	if err := module.Say(ctx, Greeting{}); err == nil {
		t.Fatal("expected validation error for empty greeting")
	}
}

// TestStage1ModuleLifecycleMapping drives the shared Temporal lifecycle
// Workflow with Stage 1's module identity to make the conductor/supervisor/
// worker mapping concrete and testable: the Workflow is the conductor that
// tracks desired task count and liveness; heartbeats stand in for worker
// registration and progress reporting.
func TestStage1ModuleLifecycleMapping(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.StartSignal, nil) }, time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.HeartbeatSignal, nil) }, 30*time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.StopSignal, nil) }, 60*time.Second)
	env.ExecuteWorkflow(controlplane.ModuleLifecycle, controlplane.LifecycleSpec{
		Module: Stage1ModuleName, Version: "tutorial-1", TaskCount: 4, LivenessTimeout: time.Minute,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state controlplane.LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != controlplane.Stopped || state.TaskCount != 4 || state.Heartbeats != 1 {
		t.Fatalf("state = %#v", state)
	}
}
