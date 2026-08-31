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
	"app/internal/ramaspace"
	"app/internal/storage"
)

// QuickTutorialConfig bundles the settings for the RamaSpace (quick-tutorial,
// Tutorial 6) HTTP server.
type QuickTutorialConfig struct {
	Address           string
	DataPath          string
	Memory            bool
	Tasks             uint
	SchemaVersion     int
	Interval          time.Duration
	TemporalAddress   string
	TemporalNamespace string
	TemporalTaskQueue string
	TemporalWorkflow  string
	HeartbeatInterval time.Duration
	LivenessTimeout   time.Duration
	ControlPlane      *controlplane.WorkerControlPlane
}

type loginRequest struct {
	UserID       string `json:"userId"`
	PasswordHash string `json:"passwordHash"`
}

// QuickTutorialHandler mounts the RamaSpace HTTP endpoints on a mux.
func QuickTutorialHandler(module *ramaspace.Module) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	mux.HandleFunc("POST /users", func(w http.ResponseWriter, r *http.Request) {
		var reg ramaspace.UserRegistration
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		registered, err := module.RegisterUser(r.Context(), reg)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		if !registered {
			writeError(w, http.StatusConflict, errors.New("user id already registered"))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]bool{"registered": true})
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		hash, found, err := module.GetPasswordHash(r.Context(), req.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found || hash != req.PasswordHash {
			writeError(w, http.StatusUnauthorized, errors.New("invalid credentials"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
	})
	mux.HandleFunc("GET /users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		profile, found, err := module.GetProfile(r.Context(), r.PathValue("userID"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, ramaspace.ErrUserNotFound)
			return
		}
		writeJSON(w, http.StatusOK, profile)
	})
	mux.HandleFunc("PATCH /users/{userID}", func(w http.ResponseWriter, r *http.Request) {
		var edit ramaspace.ProfileEdit
		if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		edit.UserID = r.PathValue("userID")
		if err := module.EditProfile(r.Context(), edit); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /friends/requests", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID     string `json:"userId"`
			DestUserID string `json:"destUserId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.FriendRequest(r.Context(), req.UserID, req.DestUserID); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("DELETE /friends/requests", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID     string `json:"userId"`
			DestUserID string `json:"destUserId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.CancelFriendRequest(r.Context(), req.UserID, req.DestUserID); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /friends", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID     string `json:"userId"`
			DestUserID string `json:"destUserId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.AcceptFriendRequest(r.Context(), req.UserID, req.DestUserID); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("DELETE /friends", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID     string `json:"userId"`
			DestUserID string `json:"destUserId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.RemoveFriendship(r.Context(), req.UserID, req.DestUserID); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /friends/{userID}", func(w http.ResponseWriter, r *http.Request) {
		limit, err := pageLimit(r, ramaspace.MaxPageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := module.ListFriends(r.Context(), r.PathValue("userID"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	mux.HandleFunc("GET /friends/{userID}/count", func(w http.ResponseWriter, r *http.Request) {
		count, err := module.FriendCount(r.Context(), r.PathValue("userID"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]uint64{"count": count})
	})
	mux.HandleFunc("GET /friends/{userID}/outgoing", func(w http.ResponseWriter, r *http.Request) {
		limit, err := pageLimit(r, ramaspace.MaxPageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := module.ListOutgoingRequests(r.Context(), r.PathValue("userID"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	mux.HandleFunc("GET /friends/{userID}/incoming", func(w http.ResponseWriter, r *http.Request) {
		limit, err := pageLimit(r, ramaspace.MaxPageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := module.ListIncomingRequests(r.Context(), r.PathValue("userID"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})

	mux.HandleFunc("POST /posts", func(w http.ResponseWriter, r *http.Request) {
		var post ramaspace.Post
		if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.Post(r.Context(), post); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /posts/{userID}", func(w http.ResponseWriter, r *http.Request) {
		limit, err := pageLimit(r, ramaspace.MaxPageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cursor, err := strconv.ParseUint(orDefault(r.URL.Query().Get("cursor"), "0"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := module.ResolvePosts(r.Context(), r.PathValue("userID"), cursor, limit)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
	mux.HandleFunc("GET /posts/{userID}/count", func(w http.ResponseWriter, r *http.Request) {
		count, err := module.PostCount(r.Context(), r.PathValue("userID"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]uint64{"count": count})
	})

	mux.HandleFunc("POST /profile-views", func(w http.ResponseWriter, r *http.Request) {
		var view ramaspace.ProfileView
		if err := json.NewDecoder(r.Body).Decode(&view); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := module.RecordProfileView(r.Context(), view); err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /profile-views/{userID}/count", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, err := strconv.ParseInt(q.Get("from"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		to, err := strconv.ParseInt(q.Get("to"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		count, err := module.ProfileViewCount(r.Context(), r.PathValue("userID"), from, to)
		if err != nil {
			writeError(w, ramaspaceErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]uint64{"count": count})
	})

	mux.HandleFunc("POST /admin/advance", func(w http.ResponseWriter, r *http.Request) {
		n, err := module.Advance(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"processed": n})
	})
	mux.HandleFunc("POST /admin/rebuild", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Rebuild(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "rebuilt"})
	})
	mux.HandleFunc("POST /admin/migrate-profiles", func(w http.ResponseWriter, r *http.Request) {
		migrated, err := module.MigrateProfiles(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"migrated": migrated})
	})
	return mux
}

func ramaspaceErrorStatus(err error) int {
	switch {
	case errors.Is(err, ramaspace.ErrUserNotFound):
		return http.StatusNotFound
	case errors.Is(err, ramaspace.ErrInvalidRegistration),
		errors.Is(err, ramaspace.ErrInvalidEdit),
		errors.Is(err, ramaspace.ErrUnknownField),
		errors.Is(err, ramaspace.ErrInvalidFriendRequest),
		errors.Is(err, ramaspace.ErrInvalidFriendship),
		errors.Is(err, ramaspace.ErrInvalidPost),
		errors.Is(err, ramaspace.ErrInvalidProfileView),
		errors.Is(err, ramaspace.ErrInvalidPageSize),
		errors.Is(err, ramaspace.ErrInvalidRange):
		return http.StatusBadRequest
	case errors.Is(err, storage.ErrIdempotencyConflict):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func pageLimit(r *http.Request, defaultLimit int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, nil
	}
	return strconv.Atoi(raw)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// RunQuickTutorial runs the RamaSpace HTTP server with optional Temporal
// lifecycle, advancing the Posts/ProfileViews microbatch topologies on a
// fixed interval (the configurable microbatch window from PRD.md).
func RunQuickTutorial(ctx context.Context, config QuickTutorialConfig) (runErr error) {
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

	schemaVersion := config.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = ramaspace.SchemaVersion2
	}
	module, err := ramaspace.New(store, uint32(config.Tasks), schemaVersion, nil)
	if err != nil {
		return fmt.Errorf("new module: %w", err)
	}

	controlErrors, controlCleanup, err := StartControlPlane(ctx, ramaspace.ModuleName, "quick-tutorial", uint32(config.Tasks), ControlPlaneConfig{
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
		Handler:           QuickTutorialHandler(module),
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
				if _, err := module.Advance(ctx); err != nil {
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
	slog.Info("quick-tutorial ready", "address", listener.Addr().String(), "storage", map[bool]string{true: "memory", false: "pebble"}[config.Memory], "temporal", controlErrors != nil)
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

// RunQuickTutorialCLI parses flags and runs the RamaSpace server.
func RunQuickTutorialCLI(ctx context.Context, args []string) error {
	config, err := parseQuickTutorialConfig(args, os.Getenv)
	if err != nil {
		return err
	}
	return RunQuickTutorial(ctx, config)
}

func parseQuickTutorialConfig(args []string, getenv func(string) string) (QuickTutorialConfig, error) {
	flags := flag.NewFlagSet("quick-tutorial", flag.ContinueOnError)
	config := QuickTutorialConfig{}
	flags.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flags.StringVar(&config.DataPath, "data", ".data/quick-tutorial", "Pebble data directory")
	flags.BoolVar(&config.Memory, "memory", false, "use non-durable in-memory storage")
	flags.UintVar(&config.Tasks, "tasks", 4, "power-of-two logical task count")
	flags.IntVar(&config.SchemaVersion, "schema-version", ramaspace.SchemaVersion2, "RamaSpace profile schema version (1 or 2)")
	flags.DurationVar(&config.Interval, "interval", 30*time.Second, "microbatch advance interval")
	flags.StringVar(&config.TemporalAddress, "temporal-address", getenv("TEMPORAL_ADDRESS"), "Temporal server address; empty disables the control plane")
	flags.StringVar(&config.TemporalNamespace, "temporal-namespace", envOr(getenv, "TEMPORAL_NAMESPACE", defaultTemporalNamespace), "Temporal namespace")
	flags.StringVar(&config.TemporalTaskQueue, "temporal-task-queue", envOr(getenv, "QUICK_TUTORIAL_TEMPORAL_TASK_QUEUE", "quick-tutorial-control-plane"), "Temporal lifecycle task queue")
	flags.StringVar(&config.TemporalWorkflow, "temporal-workflow-id", envOr(getenv, "QUICK_TUTORIAL_TEMPORAL_WORKFLOW_ID", "quick-tutorial-ramaspace-lifecycle"), "Temporal lifecycle workflow ID")
	flags.DurationVar(&config.HeartbeatInterval, "temporal-heartbeat", defaultHeartbeatInterval, "Temporal lifecycle heartbeat interval")
	flags.DurationVar(&config.LivenessTimeout, "temporal-liveness-timeout", defaultLivenessTimeout, "Temporal lifecycle liveness timeout")
	if err := flags.Parse(args); err != nil {
		return QuickTutorialConfig{}, err
	}
	if config.Tasks > uint(^uint32(0)) {
		return QuickTutorialConfig{}, fmt.Errorf("task count exceeds uint32: %d", config.Tasks)
	}
	return config, nil
}
