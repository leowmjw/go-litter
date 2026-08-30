package sessionapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/profile"
	"app/internal/storage"
)

func TestSession1Handler(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := profile.New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session1Handler(module)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	registration := profile.Registration{UUID: "alice-1", Username: "alice", PasswordHash: "hash1"}
	created := requestJSON(t, handler, http.MethodPost, "/users", registration)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	var createResult struct {
		UserID uint64 `json:"userId"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResult); err != nil || createResult.UserID == 0 {
		t.Fatalf("create result = %#v, %v", createResult, err)
	}

	conflict := requestJSON(t, handler, http.MethodPost, "/users", profile.Registration{UUID: "alice-2", Username: "alice", PasswordHash: "other"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d", conflict.Code)
	}
	idempotencyConflict := requestJSON(t, handler, http.MethodPost, "/users", profile.Registration{UUID: "alice-1", Username: "alice", PasswordHash: "changed"})
	if idempotencyConflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status = %d", idempotencyConflict.Code)
	}

	path := "/users/" + strconv.FormatUint(createResult.UserID, 10)
	edit := profile.ProfileEdits{RequestID: "edit-1", Edits: []profile.Edit{profile.DisplayName("Alice")}}
	patched := requestJSON(t, handler, http.MethodPatch, path, edit)
	if patched.Code != http.StatusNoContent {
		t.Fatalf("patch status = %d body=%s", patched.Code, patched.Body.String())
	}
	edit.Edits = []profile.Edit{profile.DisplayName("Different")}
	if recorder := requestJSON(t, handler, http.MethodPatch, path, edit); recorder.Code != http.StatusConflict {
		t.Fatalf("patch idempotency conflict status = %d", recorder.Code)
	}

	fetched := httptest.NewRecorder()
	handler.ServeHTTP(fetched, httptest.NewRequest(http.MethodGet, path, nil))
	if fetched.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", fetched.Code, fetched.Body.String())
	}
	var result profile.Profile
	if err := json.Unmarshal(fetched.Body.Bytes(), &result); err != nil || result.DisplayName == nil || *result.DisplayName != "Alice" {
		t.Fatalf("profile = %#v, %v", result, err)
	}
	if bytes.Contains(fetched.Body.Bytes(), []byte("pwdHash")) {
		t.Fatalf("public profile exposed password hash: %s", fetched.Body.String())
	}

	rebuilt := httptest.NewRecorder()
	handler.ServeHTTP(rebuilt, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuilt.Code != http.StatusOK {
		t.Fatalf("rebuild status = %d body=%s", rebuilt.Code, rebuilt.Body.String())
	}
}

func TestSession1HandlerErrors(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := profile.New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session1Handler(module)
	cases := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{method: http.MethodPost, path: "/users", body: "{", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/users/not-a-number", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/users/99", status: http.StatusNotFound},
		{method: http.MethodPatch, path: "/users/not-a-number", body: `{}`, status: http.StatusBadRequest},
		{method: http.MethodPatch, path: "/users/99", body: `{`, status: http.StatusBadRequest},
		{method: http.MethodPatch, path: "/users/99", body: `{"requestId":"edit","edits":[{"field":"displayName","string":"Missing"}]}`, status: http.StatusNotFound},
		{method: http.MethodPatch, path: "/users/99", body: `{}`, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body)))
		if recorder.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestSession1HandlerInjectedFailures(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := profile.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session1Handler(module)
	injected := errors.New("injected")
	originalRegister := module.Register
	module.Register = func(context.Context, profile.Registration) (uint64, bool, error) { return 0, false, injected }
	if recorder := requestJSON(t, handler, http.MethodPost, "/users", profile.Registration{}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("register failure status = %d", recorder.Code)
	}
	module.Register = originalRegister
	userID, _, err := module.Register(context.Background(), profile.Registration{UUID: "alice", Username: "alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/users/" + strconv.FormatUint(userID, 10)
	originalGet := module.GetProfile
	module.GetProfile = func(context.Context, uint64) (profile.Profile, bool, error) {
		return profile.Profile{}, false, injected
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	if get.Code != http.StatusInternalServerError {
		t.Fatalf("get failure status = %d", get.Code)
	}
	module.GetProfile = originalGet
	originalEdit := module.Edit
	module.Edit = func(context.Context, profile.ProfileEdits) error { return injected }
	if recorder := requestJSON(t, handler, http.MethodPatch, path, profile.ProfileEdits{RequestID: "edit", Edits: []profile.Edit{profile.DisplayName("Alice")}}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("edit failure status = %d", recorder.Code)
	}
	module.Edit = originalEdit
	module.Rebuild = func(context.Context) error { return injected }
	rebuild := httptest.NewRecorder()
	handler.ServeHTTP(rebuild, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuild.Code != http.StatusInternalServerError {
		t.Fatalf("rebuild failure status = %d", rebuild.Code)
	}
}

func TestRunSession1StopsWithContext(t *testing.T) {
	t.Setenv("TEMPORAL_ADDRESS", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunSession1(ctx, Session1Config{Address: "127.0.0.1:0", Memory: true, Tasks: 1}); err != nil {
		t.Fatal(err)
	}
	if err := RunSession1CLI(ctx, []string{"-memory", "-addr=127.0.0.1:0", "-tasks=1"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunSession1TemporalLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	control := &controlplane.WorkerControlPlane{
		StartWorker: func() error {
			record("worker-start")
			return nil
		},
		StopWorker: func() { record("worker-stop") },
		StartLifecycle: func(_ context.Context, spec controlplane.LifecycleSpec) error {
			if spec.Module != "profiles" || spec.Version != "session-1" || spec.TaskCount != 1 || spec.LivenessTimeout != 50*time.Millisecond {
				t.Errorf("lifecycle spec = %#v", spec)
			}
			record("lifecycle-start")
			return nil
		},
		SignalLifecycle: func(_ context.Context, signal string) error {
			record(signal)
			if signal == controlplane.HeartbeatSignal {
				cancel()
			}
			return nil
		},
		WaitLifecycle: func(context.Context) error {
			record("lifecycle-wait")
			return nil
		},
		Close: func() { record("close") },
	}
	if err := RunSession1(ctx, Session1Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		HeartbeatInterval: time.Millisecond,
		LivenessTimeout:   50 * time.Millisecond,
		ControlPlane:      control,
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"worker-start", "lifecycle-start", controlplane.StartSignal, controlplane.HeartbeatSignal, controlplane.StopSignal, "lifecycle-wait", "worker-stop", "close"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRunSession1StopsOnTemporalHeartbeatFailure(t *testing.T) {
	injected := errors.New("heartbeat failed")
	control := &controlplane.WorkerControlPlane{
		StartWorker:    func() error { return nil },
		StopWorker:     func() {},
		StartLifecycle: func(context.Context, controlplane.LifecycleSpec) error { return nil },
		SignalLifecycle: func(_ context.Context, signal string) error {
			if signal == controlplane.HeartbeatSignal {
				return injected
			}
			return nil
		},
		WaitLifecycle: func(context.Context) error { return nil },
		Close:         func() {},
	}
	err := RunSession1(context.Background(), Session1Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		HeartbeatInterval: time.Millisecond,
		LivenessTimeout:   50 * time.Millisecond,
		ControlPlane:      control,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want %v", err, injected)
	}
}

func TestSession1TemporalConfigFromFlagsAndEnvironment(t *testing.T) {
	environment := map[string]string{
		"TEMPORAL_ADDRESS":              "temporal:7233",
		"TEMPORAL_NAMESPACE":            "workshop",
		"SESSION1_TEMPORAL_TASK_QUEUE":  "profiles-control",
		"SESSION1_TEMPORAL_WORKFLOW_ID": "profiles-lifecycle",
	}
	config, err := parseSession1Config([]string{"-temporal-heartbeat=2s", "-temporal-liveness-timeout=7s"}, func(name string) string {
		return environment[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "temporal:7233" || config.TemporalNamespace != "workshop" || config.TemporalTaskQueue != "profiles-control" || config.TemporalWorkflow != "profiles-lifecycle" || config.HeartbeatInterval != 2*time.Second || config.LivenessTimeout != 7*time.Second {
		t.Fatalf("config = %#v", config)
	}
	config, err = parseSession1Config([]string{"-temporal-address="}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "" {
		t.Fatalf("dataflow-only Temporal address = %q", config.TemporalAddress)
	}
}

func TestRunSession1Errors(t *testing.T) {
	if err := RunSession1CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession1CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if strconv.IntSize == 64 {
		if err := RunSession1CLI(context.Background(), []string{"-tasks=4294967296"}); err == nil {
			t.Fatal("expected uint32 overflow error")
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession1(context.Background(), Session1Config{Address: "127.0.0.1:0", DataPath: filepath.Join(file, "store"), Tasks: 1}); err == nil {
		t.Fatal("expected Pebble open error")
	}
}

func requestJSON(t *testing.T, handler *http.ServeMux, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}
