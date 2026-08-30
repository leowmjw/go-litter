package controlplane

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
)

func TestModuleLifecycleHeartbeatAndStop(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(StartSignal, nil) }, time.Second)
	env.RegisterDelayedCallback(func() {
		value, err := env.QueryWorkflow(StatusQuery)
		if err != nil {
			t.Errorf("query: %v", err)
			return
		}
		var state LifecycleState
		if err := value.Get(&state); err != nil {
			t.Errorf("decode query: %v", err)
		} else if state.Status != Running {
			t.Errorf("query status = %s", state.Status)
		}
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(HeartbeatSignal, nil) }, 30*time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(HeartbeatSignal, nil) }, 80*time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(StopSignal, nil) }, 130*time.Second)
	env.ExecuteWorkflow(ModuleLifecycle, LifecycleSpec{Module: "profiles", Version: "v1", TaskCount: 4, LivenessTimeout: time.Minute})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != Stopped || state.Heartbeats != 2 || state.Version != "v1" || state.TaskCount != 4 {
		t.Fatalf("state = %#v", state)
	}
}

func TestModuleLifecycleTimeout(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(StartSignal, nil) }, time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(HeartbeatSignal, nil) }, 30*time.Second)
	env.ExecuteWorkflow(ModuleLifecycle, LifecycleSpec{Module: "profiles", Version: "v1", TaskCount: 4, LivenessTimeout: time.Minute})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != Failed || state.LastFailure != "liveness timeout" || state.Heartbeats != 1 {
		t.Fatalf("state = %#v", state)
	}
}

func TestModuleLifecycleStopBeforeStart(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(StopSignal, nil) }, time.Second)
	env.ExecuteWorkflow(ModuleLifecycle, LifecycleSpec{Module: "profiles", Version: "v1", TaskCount: 4, LivenessTimeout: time.Minute})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != Stopped || state.Heartbeats != 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestWorkerControlPlaneValidation(t *testing.T) {
	if err := (*WorkerControlPlane)(nil).Validate(); err == nil {
		t.Fatal("expected nil wiring error")
	}
	control := &WorkerControlPlane{
		StartWorker:     func() error { return nil },
		StopWorker:      func() {},
		StartLifecycle:  func(context.Context, LifecycleSpec) error { return nil },
		SignalLifecycle: func(context.Context, string) error { return nil },
		WaitLifecycle:   func(context.Context) error { return nil },
		Close:           func() {},
	}
	if err := control.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkerControlPlane(TemporalConfig{}); err == nil {
		t.Fatal("expected Temporal configuration error")
	}
}

func TestModuleLifecycleValidation(t *testing.T) {
	for _, spec := range []LifecycleSpec{
		{},
		{Module: "profiles", Version: "v1", TaskCount: 3, LivenessTimeout: time.Minute},
		{Module: "profiles", Version: "v1", TaskCount: 4},
	} {
		env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
		env.ExecuteWorkflow(ModuleLifecycle, spec)
		if env.GetWorkflowError() == nil {
			t.Fatalf("expected validation error for %#v", spec)
		}
	}
}
