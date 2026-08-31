package sessionapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/restapi"
	"app/internal/storage"

	"go.temporal.io/sdk/testsuite"
)

func TestSession5Handler(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := restapi.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	module.Fetch = func(ctx context.Context, url string) (string, error) {
		return "mock body for " + url, nil
	}
	handler := Session5Handler(module)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	badJSON := httptest.NewRecorder()
	handler.ServeHTTP(badJSON, httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewBufferString("{")))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}
	fetch := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"url":"https://example.com/joke/1"}`)
	handler.ServeHTTP(fetch, httptest.NewRequest(http.MethodPost, "/fetch", body))
	if fetch.Code != http.StatusAccepted {
		t.Fatalf("fetch status = %d body=%s", fetch.Code, fetch.Body.String())
	}
	empty := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"url":""}`)
	handler.ServeHTTP(empty, httptest.NewRequest(http.MethodPost, "/fetch", body))
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty url status = %d", empty.Code)
	}
	noParam := httptest.NewRecorder()
	handler.ServeHTTP(noParam, httptest.NewRequest(http.MethodGet, "/response", nil))
	if noParam.Code != http.StatusBadRequest {
		t.Fatalf("missing url status = %d", noParam.Code)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/response?url=https://example.com/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing response status = %d", missing.Code)
	}
	// Enqueued URL is processed by the async worker (here: a manual drain).
	if err := module.Replay(context.Background()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/response?url=https://example.com/joke/1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		URL  string `json:"url"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Body != "mock body for https://example.com/joke/1" {
		t.Fatalf("response = %#v, %v", result, err)
	}
}

func TestSession5HandlerWithHTTPServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello from fake server")
	}))
	defer server.Close()
	store := storage.NewMemory()
	defer store.Close()
	module, err := restapi.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	module.Fetch = func(ctx context.Context, url string) (string, error) {
		resp, err := http.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	handler := Session5Handler(module)
	fetch := httptest.NewRecorder()
	payload, _ := json.Marshal(map[string]string{"url": server.URL})
	handler.ServeHTTP(fetch, httptest.NewRequest(http.MethodPost, "/fetch", bytes.NewReader(payload)))
	if fetch.Code != http.StatusAccepted {
		t.Fatalf("fetch status = %d body=%s", fetch.Code, fetch.Body.String())
	}
	if err := module.Replay(context.Background()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/response?url="+server.URL, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Body != "hello from fake server" {
		t.Fatalf("response = %#v, %v", result, err)
	}
}

func TestSession5HandlerErrors(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := restapi.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session5Handler(module)
	injected := errors.New("injected")
	originalFetch := module.Fetch
	module.Fetch = func(context.Context, string) (string, error) { return "", injected }
	// Enqueue itself must not fail on a bad fetch; the worker surfaces it later.
	if recorder := requestJSON(t, handler, http.MethodPost, "/fetch", map[string]string{"url": "https://example.com"}); recorder.Code != http.StatusAccepted {
		t.Fatalf("enqueue with failing fetch status = %d", recorder.Code)
	}
	if err := module.Replay(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("replay error = %v, want %v", err, injected)
	}
	module.Fetch = originalFetch
	originalEnqueue := module.Enqueue
	module.Enqueue = func(context.Context, string) (uint64, error) { return 0, injected }
	if recorder := requestJSON(t, handler, http.MethodPost, "/fetch", map[string]string{"url": "https://example.com"}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("enqueue failure status = %d", recorder.Code)
	}
	module.Enqueue = originalEnqueue
	originalGet := module.GetResponse
	module.GetResponse = func(context.Context, string) (string, bool, error) { return "", false, injected }
	if recorder := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/response?url=https://example.com", nil))
		return recorder.Code != http.StatusInternalServerError
	}() {
		t.Fatalf("get response failure status = %d", recorder.Code)
	}
	module.GetResponse = originalGet
	module.Replay = func(context.Context) error { return injected }
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/admin/replay", nil))
	if replay.Code != http.StatusInternalServerError {
		t.Fatalf("replay failure status = %d", replay.Code)
	}
	module.Rebuild = func(context.Context) error { return injected }
	rebuild := httptest.NewRecorder()
	handler.ServeHTTP(rebuild, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuild.Code != http.StatusInternalServerError {
		t.Fatalf("rebuild failure status = %d", rebuild.Code)
	}
}

func TestSession5LatestResponseJavaBehavior(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := restapi.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	module.Fetch = func(ctx context.Context, url string) (string, error) {
		n := calls.Add(1)
		return fmt.Sprintf("body-%d for %s", n, url), nil
	}
	url1 := "https://example.com/joke/1"
	url2 := "https://example.com/joke/2"
	if err := module.Append(ctx, url1); err != nil {
		t.Fatal(err)
	}
	if err := module.Append(ctx, url2); err != nil {
		t.Fatal(err)
	}
	if err := module.Append(ctx, url1); err != nil {
		t.Fatal(err)
	}
	body1, found, err := module.GetResponse(ctx, url1)
	if err != nil || !found || body1 != "body-3 for "+url1 {
		t.Fatalf("url1 = %q, found=%v, err=%v", body1, found, err)
	}
	body2, found, err := module.GetResponse(ctx, url2)
	if err != nil || !found || body2 != "body-2 for "+url2 {
		t.Fatalf("url2 = %q, found=%v, err=%v", body2, found, err)
	}
}

// TestSession5WorkerRetriesUntilSuccess verifies the async worker path: a
// failing fetch blocks progress (record not skipped) and a later successful
// drain commits the response exactly once.
func TestSession5WorkerRetriesUntilSuccess(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := restapi.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	fail := true
	module.Fetch = func(ctx context.Context, url string) (string, error) {
		if fail {
			return "", errors.New("upstream down")
		}
		return "recovered body", nil
	}
	if _, err := module.Enqueue(ctx, "https://example.com/retry"); err != nil {
		t.Fatal(err)
	}
	if err := module.Replay(ctx); err == nil {
		t.Fatal("expected fetch failure to block progress")
	}
	fail = false
	if err := module.Replay(ctx); err != nil {
		t.Fatalf("replay after recovery: %v", err)
	}
	body, found, err := module.GetResponse(ctx, "https://example.com/retry")
	if err != nil || !found || body != "recovered body" {
		t.Fatalf("response = %q, found=%v, err=%v", body, found, err)
	}
}

func TestRunSession5ValidationAndErrors(t *testing.T) {
	if err := RunSession5(context.Background(), Session5Config{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunSession5(context.Background(), Session5Config{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession5(context.Background(), Session5Config{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunSession5(context.Background(), Session5Config{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestRunSession5StopsWithContext(t *testing.T) {
	t.Setenv("TEMPORAL_ADDRESS", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunSession5(ctx, Session5Config{Address: "127.0.0.1:0", Memory: true, Tasks: 1, Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}
}

func TestRunSession5WorkerErrorSurfaces(t *testing.T) {
	// The worker must surface a fetch failure as a run error so the process
	// exits rather than silently dropping URLs. http://localhost is rejected
	// by the default fetcher's URL validation without any network access.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunSession5(ctx, Session5Config{
			Address:           address,
			Memory:            true,
			Tasks:             1,
			Interval:          time.Millisecond,
			HeartbeatInterval: time.Hour,
			LivenessTimeout:   2 * time.Hour,
		})
	}()
	// Wait for the server to come up, then enqueue a URL the fetcher rejects.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("server did not start")
		case <-time.After(5 * time.Millisecond):
		}
		resp, err := http.Post("http://"+address+"/fetch", "application/json", bytes.NewBufferString(`{"url":"http://localhost"}`))
		if err != nil {
			continue
		}
		resp.Body.Close()
		break
	}
	select {
	case err := <-done:
		if !errors.Is(err, restapi.ErrUnsafeURL) {
			t.Fatalf("error = %v, want %v", err, restapi.ErrUnsafeURL)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker error did not surface")
	}
}

func TestRunSession5FetchInjection(t *testing.T) {
	// Dependency injection around external I/O: a config-supplied Fetch drives
	// the async worker without any real network access.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunSession5(ctx, Session5Config{
			Address:           address,
			Memory:            true,
			Tasks:             1,
			Interval:          time.Millisecond,
			Fetch:             func(context.Context, string) (string, error) { return "injected body", nil },
			HeartbeatInterval: time.Hour,
			LivenessTimeout:   2 * time.Hour,
		})
	}()
	// Enqueue through the HTTP server so the record lands in the run's store.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("server did not start")
		case <-time.After(5 * time.Millisecond):
		}
		resp, err := http.Post("http://"+address+"/fetch", "application/json", bytes.NewBufferString(`{"url":"https://example.com/di"}`))
		if err == nil {
			resp.Body.Close()
			break
		}
	}
	for {
		resp, err := http.Get("http://" + address + "/response?url=https%3A%2F%2Fexample.com%2Fdi")
		if err == nil {
			var result struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && result.Body == "injected body" {
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("worker did not process enqueued URL")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run session 5: %v", err)
	}
}

func TestRunSession5TemporalLifecycle(t *testing.T) {
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
			if spec.Module != "rest-api" || spec.Version != "session-5" || spec.TaskCount != 1 || spec.LivenessTimeout != 50*time.Millisecond {
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
	if err := RunSession5(ctx, Session5Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
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

func TestRunSession5StopsOnTemporalHeartbeatFailure(t *testing.T) {
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
	err := RunSession5(context.Background(), Session5Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
		HeartbeatInterval: time.Millisecond,
		LivenessTimeout:   50 * time.Millisecond,
		ControlPlane:      control,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want %v", err, injected)
	}
}

func TestRunSession5TemporalEndToEnd(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	control := newTestWorkerControlPlane(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunSession5(ctx, Session5Config{
			Address:           "127.0.0.1:0",
			Memory:            true,
			Tasks:             1,
			Interval:          time.Hour,
			HeartbeatInterval: time.Hour,
			LivenessTimeout:   2 * time.Hour,
			ControlPlane:      control,
		})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run session 5: %v", err)
	}

	if env.GetWorkflowError() != nil {
		t.Fatalf("workflow error: %v", env.GetWorkflowError())
	}
	var state controlplane.LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if state.Status != controlplane.Stopped {
		t.Fatalf("status = %s, want %s", state.Status, controlplane.Stopped)
	}
	if state.Heartbeats != 1 {
		t.Fatalf("heartbeats = %d, want 1", state.Heartbeats)
	}
}

func TestSession5TemporalConfigFromFlagsAndEnvironment(t *testing.T) {
	environment := map[string]string{
		"TEMPORAL_ADDRESS":              "temporal:7233",
		"TEMPORAL_NAMESPACE":            "workshop",
		"SESSION5_TEMPORAL_TASK_QUEUE":  "restapi-control",
		"SESSION5_TEMPORAL_WORKFLOW_ID": "restapi-lifecycle",
	}
	config, err := parseSession5Config([]string{"-temporal-heartbeat=2s", "-temporal-liveness-timeout=7s"}, func(name string) string {
		return environment[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "temporal:7233" || config.TemporalNamespace != "workshop" || config.TemporalTaskQueue != "restapi-control" || config.TemporalWorkflow != "restapi-lifecycle" || config.HeartbeatInterval != 2*time.Second || config.LivenessTimeout != 7*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

func TestRunSession5Errors(t *testing.T) {
	if err := RunSession5CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession5CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if strconv.IntSize == 64 {
		if err := RunSession5CLI(context.Background(), []string{"-tasks=4294967296"}); err == nil {
			t.Fatal("expected uint32 overflow error")
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession5(context.Background(), Session5Config{Address: "127.0.0.1:0", DataPath: filepath.Join(file, "store"), Tasks: 1}); err == nil {
		t.Fatal("expected Pebble open error")
	}
}
