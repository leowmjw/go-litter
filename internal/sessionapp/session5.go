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
	"app/internal/restapi"
	"app/internal/storage"
)

const (
	defaultSession5TaskQueue  = "session-5-control-plane"
	defaultSession5WorkflowID = "session-5-restapi-lifecycle"
)

type fetchRequest struct {
	URL string `json:"url"`
}

// Session5Config bundles the settings for the RestAPI (session-5) HTTP server.
type Session5Config struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	Interval          time.Duration
	Fetch             func(context.Context, string) (string, error)
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

// Session5Handler mounts the RestAPI HTTP endpoints on a mux.
func Session5Handler(module *restapi.Module) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
		var req fetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		// Enqueue the URL and let the background worker perform the outbound
		// HTTP GET, keeping the request path free of external I/O.
		if _, err := module.Enqueue(r.Context(), req.URL); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, restapi.ErrEmptyURL) || errors.Is(err, restapi.ErrUnsafeURL) || errors.Is(err, restapi.ErrResponseTooLarge) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /response", func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			writeError(w, http.StatusBadRequest, restapi.ErrEmptyURL)
			return
		}
		body, found, err := module.GetResponse(r.Context(), url)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("response not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": url, "body": body})
	})
	mux.HandleFunc("POST /admin/replay", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Replay(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "replayed"})
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

// RunSession5 runs the RestAPI HTTP server with optional Temporal lifecycle.
func RunSession5(ctx context.Context, config Session5Config) (runErr error) {
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

	module, err := restapi.New(store, uint32(config.Tasks))
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}
	if config.Fetch != nil {
		module.Fetch = config.Fetch
	}

	controlErrors, controlCleanup, err := StartControlPlane(ctx, restapi.ModuleName, "session-5", uint32(config.Tasks), ControlPlaneConfig{
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
		Handler:           Session5Handler(module),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	// The background worker drains the GET depot on an interval, performing
	// the outbound HTTP fetches off the request path. Failed fetches stop the
	// session and are surfaced as a run error so liveness is observable.
	workerErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := module.Replay(ctx); err != nil {
					slog.Error("fetch worker failed", "error", err)
					select {
					case workerErrors <- err:
					default:
					}
					return
				}
			}
		}
	}()
	slog.Info("session-5 ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
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
	case err := <-workerErrors:
		runErr = err
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

// RunSession5CLI parses flags and runs the RestAPI server.
func RunSession5CLI(ctx context.Context, args []string) error {
	config, err := parseSession5Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession5(ctx, config)
}

func parseSession5Config(args []string, getenv func(string) string) (Session5Config, error) {
	flags := flag.NewFlagSet("session-5", flag.ContinueOnError)
	config := Session5Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-5", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "fetch worker drain interval")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION5_TEMPORAL_TASK_QUEUE", defaultSession5TaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION5_TEMPORAL_WORKFLOW_ID", defaultSession5WorkflowID), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session5Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session5Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}
