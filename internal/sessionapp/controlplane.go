package sessionapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"app/internal/controlplane"
	"app/internal/storage"
)

const (
	cpDefaultTemporalNamespace = "default"
	cpDefaultTemporalTaskQueue = "go-litter-control-plane"
	cpDefaultHeartbeatInterval = 10 * time.Second
	cpDefaultLivenessTimeout   = 30 * time.Second
)

// ControlPlaneConfig bundles the optional Temporal control-plane settings.
type ControlPlaneConfig struct {
	Address           string
	Namespace         string
	TaskQueue         string
	WorkflowID        string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	Control           *controlplane.WorkerControlPlane
}

// StartControlPlane wires a process to the local standalone Temporal server for
// module lifecycle management. It returns a channel for lifecycle errors and a
// cleanup function that sends stop, waits for the workflow, and closes the worker.
// Passing an empty Address (and no pre-built Control) disables the control plane.
func StartControlPlane(ctx context.Context, module storage.ModuleID, version string, taskCount uint32, config ControlPlaneConfig) (<-chan error, func() error, error) {
	if config.Control == nil && config.Address == "" {
		return nil, nil, nil
	}
	if config.Namespace == "" {
		config.Namespace = cpDefaultTemporalNamespace
	}
	if config.TaskQueue == "" {
		config.TaskQueue = cpDefaultTemporalTaskQueue
	}
	if config.WorkflowID == "" {
		config.WorkflowID = fmt.Sprintf("%s-%s-lifecycle", module, version)
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = cpDefaultHeartbeatInterval
	}
	if config.LivenessTimeout == 0 {
		config.LivenessTimeout = cpDefaultLivenessTimeout
	}
	if config.HeartbeatInterval <= 0 || config.LivenessTimeout <= config.HeartbeatInterval {
		return nil, nil, errors.New("positive heartbeat interval and a greater liveness timeout are required")
	}
	control := config.Control
	ownsControl := control == nil
	if control == nil {
		var err error
		control, err = controlplane.NewWorkerControlPlane(controlplane.TemporalConfig{
			Address:    config.Address,
			Namespace:  config.Namespace,
			TaskQueue:  config.TaskQueue,
			WorkflowID: config.WorkflowID,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if err := control.Validate(); err != nil {
		if ownsControl {
			control.Close()
		}
		return nil, nil, err
	}
	workerStarted := false
	lifecycleStarted := false
	var heartbeatCancel context.CancelFunc
	var heartbeatDone <-chan struct{}
	cleanup := func() error {
		if heartbeatCancel != nil {
			heartbeatCancel()
			<-heartbeatDone
		}
		var err error
		if lifecycleStarted {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = control.SignalLifecycle(stopCtx, controlplane.StopSignal)
			if err == nil {
				err = control.WaitLifecycle(stopCtx)
			}
			cancel()
		}
		if workerStarted {
			control.StopWorker()
		}
		control.Close()
		return err
	}
	if err := control.StartWorker(); err != nil {
		control.Close()
		return nil, nil, fmt.Errorf("start Temporal worker: %w", err)
	}
	workerStarted = true
	spec := controlplane.LifecycleSpec{
		Module:          module,
		Version:         version,
		TaskCount:       taskCount,
		LivenessTimeout: config.LivenessTimeout,
	}
	if err := control.StartLifecycle(ctx, spec); err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	lifecycleStarted = true
	if err := control.SignalLifecycle(ctx, controlplane.StartSignal); err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatCancel = cancelHeartbeat
	done := make(chan struct{})
	heartbeatDone = done
	controlErrors := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := control.SignalLifecycle(heartbeatCtx, controlplane.HeartbeatSignal); err != nil {
					controlErrors <- err
					return
				}
			}
		}
	}()
	return controlErrors, cleanup, nil
}
