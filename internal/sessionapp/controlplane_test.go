package sessionapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"app/internal/controlplane"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestStartControlPlaneLifecycleEndToEnd(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	control := newTestWorkerControlPlane(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controlErrors, cleanup, err := StartControlPlane(ctx, "session", "e2e", 4, ControlPlaneConfig{
		Control:           control,
		HeartbeatInterval: time.Hour,
		LivenessTimeout:   2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("start control plane: %v", err)
	}

	// Wait for the delayed start, heartbeat, and stop callbacks to drive the
	// workflow through its Running and Stopped states.
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	select {
	case err := <-controlErrors:
		if err != nil {
			t.Fatalf("control error: %v", err)
		}
	default:
	}
	if env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}
	var state controlplane.LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if state.Status != controlplane.Stopped {
		t.Fatalf("status = %s, want %s", state.Status, controlplane.Stopped)
	}
	if state.Heartbeats != 1 {
		t.Fatalf("heartbeats = %d, want 1", state.Heartbeats)
	}
}

func newTestWorkerControlPlane(t *testing.T, env *testsuite.TestWorkflowEnvironment) *controlplane.WorkerControlPlane {
	t.Helper()
	done := make(chan struct{})
	started := make(chan struct{})
	var runErr error
	var runErrMu sync.Mutex
	wrap := func(ctx workflow.Context, spec controlplane.LifecycleSpec) (controlplane.LifecycleState, error) {
		close(started)
		return controlplane.ModuleLifecycle(ctx, spec)
	}
	env.RegisterWorkflow(wrap)
	return &controlplane.WorkerControlPlane{
		StartWorker: func() error { return nil },
		StopWorker:  func() {},
		StartLifecycle: func(_ context.Context, spec controlplane.LifecycleSpec) error {
			// Drive the workflow through its lifecycle using delayed callbacks so
			// the testsuite can deliver signals deterministically.
			env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.StartSignal, nil) }, time.Nanosecond)
			env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.HeartbeatSignal, nil) }, time.Second)
			env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.StopSignal, nil) }, 2*time.Second)
			go func() {
				defer close(done)
				env.ExecuteWorkflow(wrap, spec)
				runErrMu.Lock()
				runErr = env.GetWorkflowError()
				runErrMu.Unlock()
			}()
			<-started
			return nil
		},
		SignalLifecycle: func(_ context.Context, signal string) error {
			// Signals are delivered by delayed callbacks to avoid races with the
			// testsuite's workflow execution loop. The real StartControlPlane path
			// is exercised by TestRunSession1TemporalLifecycle.
			_ = signal
			return nil
		},
		WaitLifecycle: func(ctx context.Context) error {
			select {
			case <-done:
				runErrMu.Lock()
				defer runErrMu.Unlock()
				return runErr
			default:
			}
			select {
			case <-done:
				runErrMu.Lock()
				defer runErrMu.Unlock()
				return runErr
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Close: func() {},
	}
}
