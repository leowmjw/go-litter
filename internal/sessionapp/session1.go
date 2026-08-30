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

	"app/internal/controlplane"
	"app/internal/profile"
	"app/internal/storage"
)

const (
	defaultTemporalNamespace  = "default"
	defaultTemporalTaskQueue  = "session-1-control-plane"
	defaultTemporalWorkflowID = "session-1-profile-lifecycle"
	defaultHeartbeatInterval  = 10 * time.Second
	defaultLivenessTimeout    = 30 * time.Second
)

type Session1Config struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

func Session1Handler(module *profile.Module) *http.ServeMux {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	writeError := func(w http.ResponseWriter, status int, err error) {
		writeJSON(w, status, map[string]string{"error": err.Error()})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /users", func(w http.ResponseWriter, r *http.Request) {
		var registration profile.Registration
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		userID, registered, err := module.Register(r.Context(), registration)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, profile.ErrInvalidRegistration) {
				status = http.StatusBadRequest
			} else if errors.Is(err, storage.ErrIdempotencyConflict) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		if !registered {
			writeError(w, http.StatusConflict, errors.New("username already registered"))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"registered": true, "userId": userID})
	})
	mux.HandleFunc("GET /users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, found, err := module.GetProfile(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, profile.ErrProfileNotFound)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			UserID       uint64  `json:"userId"`
			Username     string  `json:"username"`
			DisplayName  *string `json:"displayName,omitempty"`
			HeightInches *int    `json:"heightInches,omitempty"`
		}{UserID: result.UserID, Username: result.Username, DisplayName: result.DisplayName, HeightInches: result.HeightInches})
	})
	mux.HandleFunc("PATCH /users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.PathValue("userID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var edits profile.ProfileEdits
		if err := json.NewDecoder(r.Body).Decode(&edits); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		edits.UserID = userID
		if err := module.Edit(r.Context(), edits); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, profile.ErrInvalidEdit) {
				status = http.StatusBadRequest
			} else if errors.Is(err, profile.ErrProfileNotFound) {
				status = http.StatusNotFound
			} else if errors.Is(err, storage.ErrIdempotencyConflict) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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

func RunSession1(ctx context.Context, config Session1Config) (runErr error) {
	var store *storage.Store
	if config.Memory {
		store = storage.NewMemory()
	} else {
		var err error
		store, err = storage.NewPebble(config.DataPath)
		if err != nil {
			return err
		}
	}
	defer store.Close()
	module, err := profile.New(store, uint32(config.Tasks))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.Address)
	if err != nil {
		return err
	}
	controlErrors, stopControlPlane, err := startSession1ControlPlane(ctx, config)
	if err != nil {
		_ = listener.Close()
		return err
	}
	if stopControlPlane != nil {
		defer func() { runErr = errors.Join(runErr, stopControlPlane()) }()
	}
	server := &http.Server{
		Handler:           Session1Handler(module),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	slog.Info("session-1 ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	serverStopped := false
	select {
	case <-ctx.Done():
	case runErr = <-controlErrors:
	case runErr = <-serveErr:
		serverStopped = true
	}
	if !serverStopped {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			runErr = errors.Join(runErr, shutdownErr)
		}
		serveResult := <-serveErr
		if !errors.Is(serveResult, http.ErrServerClosed) {
			runErr = errors.Join(runErr, serveResult)
		}
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		return nil
	}
	return runErr
}

func startSession1ControlPlane(ctx context.Context, config Session1Config) (<-chan error, func() error, error) {
	return StartControlPlane(ctx, profile.ModuleName, "session-1", uint32(config.Tasks), ControlPlaneConfig{
		Address:           config.TemporalAddress,
		Namespace:         config.TemporalNamespace,
		TaskQueue:         config.TemporalTaskQueue,
		WorkflowID:        config.TemporalWorkflow,
		HeartbeatInterval: config.HeartbeatInterval,
		LivenessTimeout:   config.LivenessTimeout,
		Control:           config.ControlPlane,
	})
}

func RunSession1CLI(ctx context.Context, args []string) error {
	config, err := parseSession1Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession1(ctx, config)
}

func parseSession1Config(args []string, getenv func(string) string) (Session1Config, error) {
	flags := flag.NewFlagSet("session-1", flag.ContinueOnError)
	config := Session1Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-1", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION1_TEMPORAL_TASK_QUEUE", defaultTemporalTaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION1_TEMPORAL_WORKFLOW_ID", defaultTemporalWorkflowID), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session1Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session1Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}

func envOr(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}
