package sessionapp

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/storage"
	"app/internal/timeseries"

	"go.temporal.io/sdk/testsuite"
)

func TestRunSession2TemporalEndToEnd(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	control := newTestWorkerControlPlane(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunSession2(ctx, Session2Config{
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
		t.Fatalf("run session 2: %v", err)
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

func TestRunSession2ValidationAndErrors(t *testing.T) {
	if err := RunSession2(context.Background(), Session2Config{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunSession2(context.Background(), Session2Config{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession2(context.Background(), Session2Config{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunSession2(context.Background(), Session2Config{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestSession2Handler(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := timeseries.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session2Handler(module)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	badJSON := httptest.NewRecorder()
	handler.ServeHTTP(badJSON, httptest.NewRequest(http.MethodPost, "/render", bytes.NewBufferString("{")))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}
	render := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"url":"example.com","renderMillis":10,"timestampMillis":1000000000000}`)
	handler.ServeHTTP(render, httptest.NewRequest(http.MethodPost, "/render", body))
	if render.Code != http.StatusAccepted {
		t.Fatalf("render status = %d", render.Code)
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	bucket := int(1000000000000 / (60 * 1000))
	window := httptest.NewRecorder()
	handler.ServeHTTP(window, httptest.NewRequest(http.MethodGet, "/window?url=example.com&granularity=m&bucket="+strconv.Itoa(bucket), nil))
	if window.Code != http.StatusOK {
		t.Fatalf("window status = %d body=%s", window.Code, window.Body.String())
	}
}

func TestTimeseriesQueryErrorStatus(t *testing.T) {
	if status := timeseriesQueryErrorStatus(timeseries.ErrInvalidBucket); status != http.StatusBadRequest {
		t.Fatalf("invalid bucket status = %d", status)
	}
	if status := timeseriesQueryErrorStatus(timeseries.ErrInvalidRange); status != http.StatusBadRequest {
		t.Fatalf("invalid range status = %d", status)
	}
	if status := timeseriesQueryErrorStatus(timeseries.ErrUnknownGranularity); status != http.StatusBadRequest {
		t.Fatalf("unknown granularity status = %d", status)
	}
	if status := timeseriesQueryErrorStatus(errors.New("other")); status != http.StatusInternalServerError {
		t.Fatalf("other error status = %d", status)
	}
}

func TestRunSession2CLIErrors(t *testing.T) {
	if err := RunSession2CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession2CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if err := RunSession2CLI(context.Background(), []string{"-memory", "-tasks=4294967296"}); err == nil {
		t.Fatal("expected uint32 overflow error")
	}
}
