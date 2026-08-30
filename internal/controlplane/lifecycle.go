package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"app/internal/storage"
)

const (
	StartSignal     = "module-start"
	HeartbeatSignal = "module-heartbeat"
	StopSignal      = "module-stop"
	StatusQuery     = "module-status"
)

type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Stopped Status = "stopped"
	Failed  Status = "failed"
)

type LifecycleSpec struct {
	Module          storage.ModuleID
	Version         string
	TaskCount       uint32
	LivenessTimeout time.Duration
}

type LifecycleState struct {
	Module      string
	Version     string
	TaskCount   uint32
	Status      Status
	Heartbeats  uint64
	LastFailure string
}

type TemporalConfig struct {
	Address    string
	Namespace  string
	TaskQueue  string
	WorkflowID string
}

// WorkerControlPlane is concrete process wiring with replaceable function fields.
// Its Temporal worker only registers ModuleLifecycle; module data remains local.
type WorkerControlPlane struct {
	StartWorker     func() error
	StopWorker      func()
	StartLifecycle  func(context.Context, LifecycleSpec) error
	SignalLifecycle func(context.Context, string) error
	WaitLifecycle   func(context.Context) error
	Close           func()
}

func NewWorkerControlPlane(config TemporalConfig) (*WorkerControlPlane, error) {
	if config.Address == "" || config.Namespace == "" || config.TaskQueue == "" || config.WorkflowID == "" {
		return nil, errors.New("Temporal address, namespace, task queue, and workflow ID are required")
	}
	temporalClient, err := client.Dial(client.Options{HostPort: config.Address, Namespace: config.Namespace})
	if err != nil {
		return nil, fmt.Errorf("dial Temporal: %w", err)
	}
	temporalWorker := worker.New(temporalClient, config.TaskQueue, worker.Options{})
	temporalWorker.RegisterWorkflow(ModuleLifecycle)
	var lifecycleRun client.WorkflowRun
	return &WorkerControlPlane{
		StartWorker: temporalWorker.Start,
		StopWorker:  temporalWorker.Stop,
		StartLifecycle: func(ctx context.Context, spec LifecycleSpec) error {
			var err error
			lifecycleRun, err = temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
				ID:                    config.WorkflowID,
				TaskQueue:             config.TaskQueue,
				WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
			}, ModuleLifecycle, spec)
			if err != nil {
				return fmt.Errorf("start lifecycle workflow: %w", err)
			}
			return nil
		},
		SignalLifecycle: func(ctx context.Context, signal string) error {
			if err := temporalClient.SignalWorkflow(ctx, config.WorkflowID, "", signal, nil); err != nil {
				return fmt.Errorf("signal lifecycle workflow %q: %w", signal, err)
			}
			return nil
		},
		WaitLifecycle: func(ctx context.Context) error {
			if lifecycleRun == nil {
				return errors.New("lifecycle workflow has not started")
			}
			var state LifecycleState
			if err := lifecycleRun.Get(ctx, &state); err != nil {
				return fmt.Errorf("wait for lifecycle workflow: %w", err)
			}
			return nil
		},
		Close: temporalClient.Close,
	}, nil
}

func (control *WorkerControlPlane) Validate() error {
	if control == nil || control.StartWorker == nil || control.StopWorker == nil || control.StartLifecycle == nil || control.SignalLifecycle == nil || control.WaitLifecycle == nil || control.Close == nil {
		return errors.New("complete worker/control-plane function wiring is required")
	}
	return nil
}

func ModuleLifecycle(ctx workflow.Context, spec LifecycleSpec) (LifecycleState, error) {
	if spec.Module == "" || spec.Version == "" || spec.TaskCount == 0 || spec.TaskCount&(spec.TaskCount-1) != 0 || spec.LivenessTimeout <= 0 {
		return LifecycleState{}, errors.New("module, version, power-of-two task count, and positive liveness timeout are required")
	}
	state := LifecycleState{Module: string(spec.Module), Version: spec.Version, TaskCount: spec.TaskCount, Status: Pending}
	if err := workflow.SetQueryHandler(ctx, StatusQuery, func() (LifecycleState, error) { return state, nil }); err != nil {
		return state, err
	}
	start := workflow.GetSignalChannel(ctx, StartSignal)
	stop := workflow.GetSignalChannel(ctx, StopSignal)
	startSelector := workflow.NewSelector(ctx)
	startSelector.AddReceive(start, func(channel workflow.ReceiveChannel, more bool) {
		if more {
			channel.Receive(ctx, nil)
			state.Status = Running
		}
	})
	startSelector.AddReceive(stop, func(channel workflow.ReceiveChannel, more bool) {
		if more {
			channel.Receive(ctx, nil)
			state.Status = Stopped
		}
	})
	startSelector.Select(ctx)
	if state.Status == Stopped {
		return state, nil
	}
	heartbeat := workflow.GetSignalChannel(ctx, HeartbeatSignal)
	for state.Status == Running {
		if stop.ReceiveAsync(nil) {
			state.Status = Stopped
			break
		}
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, spec.LivenessTimeout)
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(stop, func(channel workflow.ReceiveChannel, more bool) {
			if more {
				channel.Receive(ctx, nil)
				state.Status = Stopped
			}
		})
		selector.AddReceive(heartbeat, func(channel workflow.ReceiveChannel, more bool) {
			if more {
				channel.Receive(ctx, nil)
				state.Heartbeats++
			}
		})
		selector.AddFuture(timer, func(future workflow.Future) {
			if err := future.Get(ctx, nil); err == nil {
				if stop.ReceiveAsync(nil) {
					state.Status = Stopped
				} else if heartbeat.ReceiveAsync(nil) {
					state.Heartbeats++
				} else {
					state.Status = Failed
					state.LastFailure = "liveness timeout"
				}
			}
		})
		selector.Select(ctx)
		cancelTimer()
	}
	return state, nil
}
