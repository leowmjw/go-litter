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
	"sync/atomic"
	"time"

	"app/internal/controlplane"
	"app/internal/musiccatalog"
	"app/internal/storage"
)

const (
	defaultSession6TaskQueue  = "session-6-control-plane"
	defaultSession6WorkflowID = "session-6-musiccatalog-lifecycle"
)

// session6Module holds the active MusicCatalog module version. The current
// pointer is swapped atomically by a live update, letting the old and new
// module versions coexist over the same store during a rollout.
type session6Module struct {
	version string
	module  *musiccatalog.Module
}

// Session6Config bundles the settings for the MusicCatalog (session-6) HTTP server.
type Session6Config struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	Interval          time.Duration
	Version           string
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

// Session6Handler mounts the MusicCatalog HTTP endpoints on a mux.
// The holder is consulted on every request so a live module update takes
// effect without restarting the process. buildModule constructs a module
// version over the shared store for /admin/update.
func Session6Handler(holder *atomic.Pointer[session6Module], buildModule func(string) (*musiccatalog.Module, error)) *http.ServeMux {
	mux := http.NewServeMux()
	current := func() *session6Module {
		entry := holder.Load()
		if entry == nil {
			return nil
		}
		return entry
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		entry := current()
		if entry == nil {
			writeError(w, http.StatusInternalServerError, errors.New("module not initialized"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"version": entry.version})
	})
	mux.HandleFunc("POST /albums", func(w http.ResponseWriter, r *http.Request) {
		var a musiccatalog.AlbumInput
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := current().module.Append(r.Context(), a); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, musiccatalog.ErrInvalidAlbum) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /album", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		artist := q.Get("artist")
		name := q.Get("name")
		if artist == "" || name == "" {
			writeError(w, http.StatusBadRequest, errors.New("artist and name are required"))
			return
		}
		album, found, err := current().module.GetAlbum(r.Context(), artist, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("album not found"))
			return
		}
		writeJSON(w, http.StatusOK, album)
	})
	mux.HandleFunc("GET /albums/count", func(w http.ResponseWriter, r *http.Request) {
		artist := r.URL.Query().Get("artist")
		if artist == "" {
			writeError(w, http.StatusBadRequest, errors.New("artist is required"))
			return
		}
		count, err := current().module.CountAlbums(r.Context(), artist)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artist": artist, "count": count})
	})
	mux.HandleFunc("POST /admin/rebuild", func(w http.ResponseWriter, r *http.Request) {
		if err := current().module.Rebuild(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilt"})
	})
	mux.HandleFunc("POST /admin/update", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.Version != "A" && req.Version != "B" {
			writeError(w, http.StatusBadRequest, errors.New("version must be A or B"))
			return
		}
		next, err := buildModule(req.Version)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		holder.Store(&session6Module{version: req.Version, module: next})
		writeJSON(w, http.StatusOK, map[string]string{"version": req.Version, "status": "updated"})
	})
	return mux
}

// RunSession6 runs the MusicCatalog HTTP server with optional Temporal lifecycle.
func RunSession6(ctx context.Context, config Session6Config) (runErr error) {
	if config.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	if config.Version != "A" && config.Version != "B" {
		return errors.New("version must be A or B")
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

	newModule := func(version string) (*musiccatalog.Module, error) {
		if version == "A" {
			return musiccatalog.NewA(store, uint32(config.Tasks))
		}
		return musiccatalog.NewB(store, uint32(config.Tasks))
	}
	module, err := newModule(config.Version)
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}
	holder := &atomic.Pointer[session6Module]{}
	holder.Store(&session6Module{version: config.Version, module: module})

	controlErrors, controlCleanup, err := StartControlPlane(ctx, musiccatalog.ModuleName, config.Version, uint32(config.Tasks), ControlPlaneConfig{
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
		Handler:           Session6Handler(holder, newModule),
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
				if _, err := holder.Load().module.AdvanceAll(ctx); err != nil {
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
	slog.Info("session-6 ready", "address", listener.Addr().String(), "version", config.Version, "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
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

// RunSession6CLI parses flags and runs the MusicCatalog server.
func RunSession6CLI(ctx context.Context, args []string) error {
	config, err := parseSession6Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession6(ctx, config)
}

func parseSession6Config(args []string, getenv func(string) string) (Session6Config, error) {
	flags := flag.NewFlagSet("session-6", flag.ContinueOnError)
	config := Session6Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-6", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "microbatch advance interval")
	flags.StringVar(&config.Version, "version", "A", "initial module version: A (raw songs) or B (parsed songs)")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION6_TEMPORAL_TASK_QUEUE", defaultSession6TaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION6_TEMPORAL_WORKFLOW_ID", defaultSession6WorkflowID), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session6Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session6Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	if config.Version != "A" && config.Version != "B" {
		return Session6Config{}, fmt.Errorf("version must be A or B: %q", config.Version)
	}
	return config, nil
}
