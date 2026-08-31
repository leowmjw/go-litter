package sessionapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/storage"
	"app/internal/topusers"
)

func TestSession3Handler(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := topusers.New(store, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session3Handler(module)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	badJSON := httptest.NewRecorder()
	handler.ServeHTTP(badJSON, httptest.NewRequest(http.MethodPost, "/purchases", bytes.NewBufferString("{")))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}
	invalid := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"userId":1,"purchaseCents":0}`)
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/purchases", body))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid purchase status = %d", invalid.Code)
	}
	purchase := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"userId":1,"purchaseCents":300}`)
	handler.ServeHTTP(purchase, httptest.NewRequest(http.MethodPost, "/purchases", body))
	if purchase.Code != http.StatusAccepted {
		t.Fatalf("purchase status = %d body=%s", purchase.Code, purchase.Body.String())
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	top := httptest.NewRecorder()
	handler.ServeHTTP(top, httptest.NewRequest(http.MethodGet, "/top-users", nil))
	if top.Code != http.StatusOK {
		t.Fatalf("top users status = %d body=%s", top.Code, top.Body.String())
	}
	var list []topusers.SpendingUser
	if err := json.Unmarshal(top.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].UserID != 1 || list[0].Total != 300 {
		t.Fatalf("top users = %#v, %v", list, err)
	}
	ids := httptest.NewRecorder()
	handler.ServeHTTP(ids, httptest.NewRequest(http.MethodGet, "/top-users/ids", nil))
	if ids.Code != http.StatusOK {
		t.Fatalf("top ids status = %d body=%s", ids.Code, ids.Body.String())
	}
	var idResult struct {
		UserIDs []uint64 `json:"userIds"`
	}
	if err := json.Unmarshal(ids.Body.Bytes(), &idResult); err != nil || len(idResult.UserIDs) != 1 || idResult.UserIDs[0] != 1 {
		t.Fatalf("top ids = %#v, %v", idResult, err)
	}
}

func TestSession3HandlerErrors(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := topusers.New(store, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session3Handler(module)
	injected := errors.New("injected")
	originalAppend := module.Append
	module.Append = func(context.Context, topusers.Purchase) error { return injected }
	if recorder := requestJSON(t, handler, http.MethodPost, "/purchases", topusers.Purchase{UserID: 1, PurchaseCents: 100}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("append failure status = %d", recorder.Code)
	}
	module.Append = originalAppend
	originalGet := module.GetTopSpendingUsers
	module.GetTopSpendingUsers = func(context.Context) ([]topusers.SpendingUser, error) { return nil, injected }
	if recorder := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/top-users", nil))
		return recorder.Code != http.StatusInternalServerError
	}() {
		t.Fatalf("get failure status = %d", recorder.Code)
	}
	module.GetTopSpendingUsers = originalGet
	module.Rebuild = func(context.Context) error { return injected }
	rebuild := httptest.NewRecorder()
	handler.ServeHTTP(rebuild, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuild.Code != http.StatusInternalServerError {
		t.Fatalf("rebuild failure status = %d", rebuild.Code)
	}
}

func TestSession3TopUsersJavaBehavior(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := topusers.New(store, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []topusers.Purchase{
		{UserID: 0, PurchaseCents: 300},
		{UserID: 1, PurchaseCents: 200},
		{UserID: 2, PurchaseCents: 100},
		{UserID: 3, PurchaseCents: 100},
		{UserID: 4, PurchaseCents: 400},
	} {
		if err := module.Append(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	top, err := module.GetTopSpendingUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []topusers.SpendingUser{{UserID: 4, Total: 400}, {UserID: 0, Total: 300}, {UserID: 1, Total: 200}}
	if fmt.Sprint(top) != fmt.Sprint(want) {
		t.Fatalf("top = %v, want %v", top, want)
	}
	if err := module.Append(ctx, topusers.Purchase{UserID: 3, PurchaseCents: 250}); err != nil {
		t.Fatal(err)
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	top, err = module.GetTopSpendingUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want = []topusers.SpendingUser{{UserID: 4, Total: 400}, {UserID: 3, Total: 350}, {UserID: 0, Total: 300}}
	if fmt.Sprint(top) != fmt.Sprint(want) {
		t.Fatalf("top = %v, want %v", top, want)
	}
	ids, err := module.GetTopUserIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[4 3 0]" {
		t.Fatalf("ids = %v, want [4 3 0]", ids)
	}
}

func TestRunSession3ValidationAndErrors(t *testing.T) {
	if err := RunSession3(context.Background(), Session3Config{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunSession3(context.Background(), Session3Config{Interval: time.Second, TopAmount: 0}); err == nil {
		t.Fatal("expected top amount error")
	}
	if err := RunSession3(context.Background(), Session3Config{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession3(context.Background(), Session3Config{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunSession3(context.Background(), Session3Config{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
		t.Fatal("expected listen error")
	}
	injected := errors.New("injected")
	control := &controlplane.WorkerControlPlane{
		StartWorker:    func() error { return injected },
		StopWorker:     func() {},
		StartLifecycle: func(context.Context, controlplane.LifecycleSpec) error { return nil },
		SignalLifecycle: func(_ context.Context, signal string) error {
			return nil
		},
		WaitLifecycle: func(context.Context) error { return nil },
		Close:         func() {},
	}
	if err := RunSession3(context.Background(), Session3Config{Interval: time.Second, Memory: true, Address: "127.0.0.1:0", Tasks: 1, ControlPlane: control}); err == nil {
		t.Fatal("expected control plane start error")
	}
}

func TestRunSession3TemporalLifecycle(t *testing.T) {
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
			if spec.Module != "top-users" || spec.Version != "session-3" || spec.TaskCount != 1 || spec.LivenessTimeout != 50*time.Millisecond {
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
	if err := RunSession3(ctx, Session3Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
		TopAmount:         3,
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

func TestRunSession3StopsOnTemporalHeartbeatFailure(t *testing.T) {
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
	err := RunSession3(context.Background(), Session3Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
		TopAmount:         3,
		HeartbeatInterval: time.Millisecond,
		LivenessTimeout:   50 * time.Millisecond,
		ControlPlane:      control,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want %v", err, injected)
	}
}

func TestSession3TemporalConfigFromFlagsAndEnvironment(t *testing.T) {
	environment := map[string]string{
		"TEMPORAL_ADDRESS":              "temporal:7233",
		"TEMPORAL_NAMESPACE":            "workshop",
		"SESSION3_TEMPORAL_TASK_QUEUE":  "topusers-control",
		"SESSION3_TEMPORAL_WORKFLOW_ID": "topusers-lifecycle",
	}
	config, err := parseSession3Config([]string{"-temporal-heartbeat=2s", "-temporal-liveness-timeout=7s"}, func(name string) string {
		return environment[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "temporal:7233" || config.TemporalNamespace != "workshop" || config.TemporalTaskQueue != "topusers-control" || config.TemporalWorkflow != "topusers-lifecycle" || config.HeartbeatInterval != 2*time.Second || config.LivenessTimeout != 7*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

func TestRunSession3Errors(t *testing.T) {
	if err := RunSession3CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession3CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if strconv.IntSize == 64 {
		if err := RunSession3CLI(context.Background(), []string{"-tasks=4294967296"}); err == nil {
			t.Fatal("expected uint32 overflow error")
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession3(context.Background(), Session3Config{Address: "127.0.0.1:0", DataPath: filepath.Join(file, "store"), Tasks: 1}); err == nil {
		t.Fatal("expected Pebble open error")
	}
}
