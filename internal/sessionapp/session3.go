package sessionapp

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"app/internal/controlplane"
	"app/internal/storage"
	"app/internal/topusers"
)

const (
	defaultSession3TaskQueue  = "session-3-control-plane"
	defaultSession3WorkflowID = "session-3-topusers-lifecycle"
)

// Session3Config bundles the settings for the TopUsers (session-3) HTTP server.
type Session3Config struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	Interval          time.Duration
	TopAmount         int
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

// Session3Handler mounts the TopUsers HTTP endpoints on a mux.
func Session3Handler(module *topusers.Module) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /purchases", func(w http.ResponseWriter, r *http.Request) {
		var p topusers.Purchase
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.Append(r.Context(), p); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, topusers.ErrInvalidPurchase) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /top-users", func(w http.ResponseWriter, r *http.Request) {
		list, err := module.GetTopSpendingUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /top-users/ids", func(w http.ResponseWriter, r *http.Request) {
		ids, err := module.GetTopUserIDs(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"userIds": ids})
	})
	mux.HandleFunc("POST /admin/rebuild", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Rebuild(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilt"})
	})
	return mux
}

// RunSession3 runs the TopUsers HTTP server with optional Temporal lifecycle.
func RunSession3(ctx context.Context, config Session3Config) (runErr error) {
	if config.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	if config.TopAmount <= 0 {
		return errors.New("top amount must be positive")
	}
	if config.Tasks > uint(^uint32(0)) {
		return fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}

	var store *storage.Store
	var err error
	if config.Memory {
		store = storage.NewMemory()
	} else {
		store, err = storage.NewPebble(config.DataPath)
	}
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()

	module, err := topusers.New(store, uint32(config.Tasks), config.TopAmount)
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}

	controlErrors, controlCleanup, err := StartControlPlane(ctx, topusers.ModuleName, "session-3", uint32(config.Tasks), ControlPlaneConfig{
		Address:           config.TemporalAddress,
		Namespace:         config.TemporalNamespace,
		TaskQueue:         config.TemporalTaskQueue,
		WorkflowID:        config.TemporalWorkflow,
		HeartbeatInterval: config.HeartbeatInterval,
		LivenessTimeout:   config.LivenessTimeout,
		Control:           config.ControlPlane,
	})
	if err != nil {
		return fmt.Errorf("start control plane: %w", err)
	}
	if controlCleanup != nil {
		defer func() { runErr = errors.Join(runErr, controlCleanup()) }()
	}

	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler:           Session3Handler(module),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	advanceErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := module.AdvanceAll(ctx); err != nil {
					slog.Error("advance failed", "error", err)
					select {
					case advanceErrors <- err:
					default:
					}
					return
				}
			}
		}
	}()
	slog.Info("session-3 ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		runErr = err
	case err := <-controlErrors:
		runErr = err
	case err := <-advanceErrors:
		runErr = err
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

// RunSession3CLI parses flags and runs the TopUsers server.
func RunSession3CLI(ctx context.Context, args []string) error {
	config, err := parseSession3Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession3(ctx, config)
}

func parseSession3Config(args []string, getenv func(string) string) (Session3Config, error) {
	flags := flag.NewFlagSet("session-3", flag.ContinueOnError)
	config := Session3Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-3", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "microbatch advance interval")
	flags.IntVar(&config.TopAmount, "top-amount", 3, "number of top spending users to track")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION3_TEMPORAL_TASK_QUEUE", defaultSession3TaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION3_TEMPORAL_WORKFLOW_ID", defaultSession3WorkflowID), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session3Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session3Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}
