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
	"strconv"
	"time"

	"app/internal/banktransfer"
	"app/internal/controlplane"
	"app/internal/storage"
)

const (
	defaultSession4TaskQueue  = "session-4-control-plane"
	defaultSession4WorkflowID = "session-4-banktransfer-lifecycle"
)

// Session4Config bundles the settings for the BankTransfer (session-4) HTTP server.
type Session4Config struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	Interval          time.Duration
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

// Session4Handler mounts the BankTransfer HTTP endpoints on a mux.
func Session4Handler(module *banktransfer.Module) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /deposits", func(w http.ResponseWriter, r *http.Request) {
		var d banktransfer.Deposit
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.AppendDeposit(r.Context(), d); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, banktransfer.ErrInvalidDeposit) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("POST /transfers", func(w http.ResponseWriter, r *http.Request) {
		var tr banktransfer.Transfer
		if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.AppendTransfer(r.Context(), tr); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, banktransfer.ErrInvalidTransfer) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /users/{userID}/funds", func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		funds, err := module.GetFunds(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"userId": userID, "funds": funds})
	})
	mux.HandleFunc("GET /users/{userID}/outgoing", func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		transfers, err := module.GetOutgoingTransfers(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, transfers)
	})
	mux.HandleFunc("GET /users/{userID}/incoming", func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		transfers, err := module.GetIncomingTransfers(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, transfers)
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

// RunSession4 runs the BankTransfer HTTP server with optional Temporal lifecycle.
func RunSession4(ctx context.Context, config Session4Config) (runErr error) {
	if config.Interval <= 0 {
		return errors.New("interval must be positive")
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

	module, err := banktransfer.New(store, uint32(config.Tasks))
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}

	controlErrors, controlCleanup, err := StartControlPlane(ctx, banktransfer.ModuleName, "session-4", uint32(config.Tasks), ControlPlaneConfig{
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
		Handler:           Session4Handler(module),
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
	slog.Info("session-4 ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
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

// RunSession4CLI parses flags and runs the BankTransfer server.
func RunSession4CLI(ctx context.Context, args []string) error {
	config, err := parseSession4Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession4(ctx, config)
}

func parseSession4Config(args []string, getenv func(string) string) (Session4Config, error) {
	flags := flag.NewFlagSet("session-4", flag.ContinueOnError)
	config := Session4Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-4", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "microbatch advance interval")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION4_TEMPORAL_TASK_QUEUE", defaultSession4TaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION4_TEMPORAL_WORKFLOW_ID", defaultSession4WorkflowID), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session4Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session4Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}
