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
	"app/internal/storage"
	"app/internal/timeseries"
)

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

// Session2Config bundles the settings for the TimeSeries (session-2) HTTP server.
type Session2Config struct {
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

// Session2Handler mounts the TimeSeries HTTP endpoints on a mux.
func Session2Handler(module *timeseries.Module) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /render", func(w http.ResponseWriter, r *http.Request) {
		var l timeseries.RenderLatency
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.Append(r.Context(), l); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, timeseries.ErrRenderLatencyURL) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	})
	mux.HandleFunc("GET /window", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		bucket, err := strconv.Atoi(q.Get("bucket"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		stats, found, err := module.GetWindowStats(r.Context(), q.Get("url"), q.Get("granularity"), bucket)
		if err != nil {
			writeError(w, timeseriesQueryErrorStatus(err), err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("window not found"))
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})
	mux.HandleFunc("GET /range", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, err := strconv.Atoi(q.Get("start"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		end, err := strconv.Atoi(q.Get("end"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		result, err := module.RangeWindowStats(r.Context(), q.Get("url"), q.Get("granularity"), start, end)
		if err != nil {
			writeError(w, timeseriesQueryErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /count", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, err := strconv.Atoi(q.Get("start"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		end, err := strconv.Atoi(q.Get("end"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		count, err := module.CountBuckets(r.Context(), q.Get("url"), q.Get("granularity"), start, end)
		if err != nil {
			writeError(w, timeseriesQueryErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": count})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, err := strconv.Atoi(q.Get("start"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		end, err := strconv.Atoi(q.Get("end"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		stats, err := module.StatsForMinuteRange(r.Context(), q.Get("url"), start, end)
		if err != nil {
			writeError(w, timeseriesQueryErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})
	return mux
}

func timeseriesQueryErrorStatus(err error) int {
	if errors.Is(err, timeseries.ErrInvalidBucket) || errors.Is(err, timeseries.ErrInvalidRange) || errors.Is(err, timeseries.ErrUnknownGranularity) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// RunSession2 runs the TimeSeries HTTP server with optional Temporal lifecycle.
func RunSession2(ctx context.Context, config Session2Config) (runErr error) {
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

	module, err := timeseries.New(store, uint32(config.Tasks))
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}

	controlErrors, controlCleanup, err := StartControlPlane(ctx, timeseries.ModuleName, "session-2", uint32(config.Tasks), ControlPlaneConfig{
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
		Handler:           Session2Handler(module),
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
	slog.Info("session-2 ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
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

// RunSession2CLI parses flags and runs the TimeSeries server.
func RunSession2CLI(ctx context.Context, args []string) error {
	config, err := parseSession2Config(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunSession2(ctx, config)
}

func parseSession2Config(args []string, getenv func(string) string) (Session2Config, error) {
	flags := flag.NewFlagSet("session-2", flag.ContinueOnError)
	config := Session2Config{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/session-2", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "microbatch advance interval")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "SESSION2_TEMPORAL_TASK_QUEUE", defaultTemporalTaskQueue), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "SESSION2_TEMPORAL_WORKFLOW_ID", "session-2-timeseries-lifecycle"), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return Session2Config{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return Session2Config{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}
