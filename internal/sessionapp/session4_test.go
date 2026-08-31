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

	"app/internal/banktransfer"
	"app/internal/controlplane"
	"app/internal/storage"

	"go.temporal.io/sdk/testsuite"
)

func TestSession4Handler(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := banktransfer.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session4Handler(module)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	badJSON := httptest.NewRecorder()
	handler.ServeHTTP(badJSON, httptest.NewRequest(http.MethodPost, "/deposits", bytes.NewBufferString("{")))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}
	deposit := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"userId":0,"amt":200}`)
	handler.ServeHTTP(deposit, httptest.NewRequest(http.MethodPost, "/deposits", body))
	if deposit.Code != http.StatusAccepted {
		t.Fatalf("deposit status = %d body=%s", deposit.Code, deposit.Body.String())
	}
	invalidDeposit := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"userId":0,"amt":0}`)
	handler.ServeHTTP(invalidDeposit, httptest.NewRequest(http.MethodPost, "/deposits", body))
	if invalidDeposit.Code != http.StatusBadRequest {
		t.Fatalf("invalid deposit status = %d", invalidDeposit.Code)
	}
	transfer := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"transferId":"alice->bob1","fromUserId":0,"toUserId":1,"amt":50}`)
	handler.ServeHTTP(transfer, httptest.NewRequest(http.MethodPost, "/transfers", body))
	if transfer.Code != http.StatusAccepted {
		t.Fatalf("transfer status = %d body=%s", transfer.Code, transfer.Body.String())
	}
	invalidTransfer := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"transferId":"","fromUserId":0,"toUserId":1,"amt":50}`)
	handler.ServeHTTP(invalidTransfer, httptest.NewRequest(http.MethodPost, "/transfers", body))
	if invalidTransfer.Code != http.StatusBadRequest {
		t.Fatalf("invalid transfer status = %d", invalidTransfer.Code)
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	funds := httptest.NewRecorder()
	handler.ServeHTTP(funds, httptest.NewRequest(http.MethodGet, "/users/0/funds", nil))
	if funds.Code != http.StatusOK {
		t.Fatalf("funds status = %d body=%s", funds.Code, funds.Body.String())
	}
	var fundResult struct {
		UserID uint64 `json:"userId"`
		Funds  int    `json:"funds"`
	}
	if err := json.Unmarshal(funds.Body.Bytes(), &fundResult); err != nil || fundResult.Funds != 150 {
		t.Fatalf("funds = %#v, %v", fundResult, err)
	}
	outgoing := httptest.NewRecorder()
	handler.ServeHTTP(outgoing, httptest.NewRequest(http.MethodGet, "/users/0/outgoing", nil))
	if outgoing.Code != http.StatusOK {
		t.Fatalf("outgoing status = %d body=%s", outgoing.Code, outgoing.Body.String())
	}
	var outgoingResult map[string]banktransfer.OutgoingRecord
	if err := json.Unmarshal(outgoing.Body.Bytes(), &outgoingResult); err != nil || len(outgoingResult) != 1 || !outgoingResult["alice->bob1"].IsSuccess {
		t.Fatalf("outgoing = %#v, %v", outgoingResult, err)
	}
	incoming := httptest.NewRecorder()
	handler.ServeHTTP(incoming, httptest.NewRequest(http.MethodGet, "/users/1/incoming", nil))
	if incoming.Code != http.StatusOK {
		t.Fatalf("incoming status = %d body=%s", incoming.Code, incoming.Body.String())
	}
	var incomingResult map[string]banktransfer.IncomingRecord
	if err := json.Unmarshal(incoming.Body.Bytes(), &incomingResult); err != nil || len(incomingResult) != 1 || !incomingResult["alice->bob1"].IsSuccess {
		t.Fatalf("incoming = %#v, %v", incomingResult, err)
	}
}

func TestSession4HandlerErrors(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := banktransfer.New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session4Handler(module)
	badUser := httptest.NewRecorder()
	handler.ServeHTTP(badUser, httptest.NewRequest(http.MethodGet, "/users/not-a-number/funds", nil))
	if badUser.Code != http.StatusBadRequest {
		t.Fatalf("bad user id status = %d", badUser.Code)
	}
	injected := errors.New("injected")
	originalAppend := module.AppendDeposit
	module.AppendDeposit = func(context.Context, banktransfer.Deposit) error { return injected }
	if recorder := requestJSON(t, handler, http.MethodPost, "/deposits", banktransfer.Deposit{UserID: 0, Amt: 1}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("append failure status = %d", recorder.Code)
	}
	module.AppendDeposit = originalAppend
	originalGet := module.GetFunds
	module.GetFunds = func(context.Context, uint64) (int, error) { return 0, injected }
	if recorder := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/users/0/funds", nil))
		return recorder.Code != http.StatusInternalServerError
	}() {
		t.Fatalf("get funds failure status = %d", badUser.Code)
	}
	module.GetFunds = originalGet
	module.Rebuild = func(context.Context) error { return injected }
	rebuild := httptest.NewRecorder()
	handler.ServeHTTP(rebuild, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuild.Code != http.StatusInternalServerError {
		t.Fatalf("rebuild failure status = %d", rebuild.Code)
	}
}

func TestSession4BankTransferJavaBehavior(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := banktransfer.New(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []banktransfer.Deposit{
		{UserID: 0, Amt: 200},
		{UserID: 1, Amt: 100},
		{UserID: 2, Amt: 100},
	} {
		if err := module.AppendDeposit(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []banktransfer.Transfer{
		{TransferID: "alice->bob1", FromUserID: 0, ToUserID: 1, Amt: 50},
		{TransferID: "alice->charlie1", FromUserID: 0, ToUserID: 2, Amt: 160},
		{TransferID: "alice->charlie2", FromUserID: 0, ToUserID: 2, Amt: 25},
		{TransferID: "charlie->bob1", FromUserID: 2, ToUserID: 1, Amt: 10},
	} {
		if err := module.AppendTransfer(ctx, tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	funds0, err := module.GetFunds(ctx, 0)
	if err != nil || funds0 != 125 {
		t.Fatalf("alice funds = %d, %v", funds0, err)
	}
	funds1, err := module.GetFunds(ctx, 1)
	if err != nil || funds1 != 160 {
		t.Fatalf("bob funds = %d, %v", funds1, err)
	}
	funds2, err := module.GetFunds(ctx, 2)
	if err != nil || funds2 != 115 {
		t.Fatalf("charlie funds = %d, %v", funds2, err)
	}
	outgoing0, err := module.GetOutgoingTransfers(ctx, 0)
	if err != nil || len(outgoing0) != 3 {
		t.Fatalf("alice outgoing = %d, %v", len(outgoing0), err)
	}
	if !outgoing0["alice->bob1"].IsSuccess || outgoing0["alice->charlie1"].IsSuccess || !outgoing0["alice->charlie2"].IsSuccess {
		t.Fatalf("alice outgoing records = %#v", outgoing0)
	}
	incoming1, err := module.GetIncomingTransfers(ctx, 1)
	if err != nil || len(incoming1) != 2 {
		t.Fatalf("bob incoming = %d, %v", len(incoming1), err)
	}
	incoming2, err := module.GetIncomingTransfers(ctx, 2)
	if err != nil || len(incoming2) != 2 {
		t.Fatalf("charlie incoming = %d, %v", len(incoming2), err)
	}
	if incoming2["alice->charlie1"].IsSuccess || !incoming2["alice->charlie2"].IsSuccess {
		t.Fatalf("charlie incoming records = %#v", incoming2)
	}
}

func TestRunSession4ValidationAndErrors(t *testing.T) {
	if err := RunSession4(context.Background(), Session4Config{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunSession4(context.Background(), Session4Config{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession4(context.Background(), Session4Config{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunSession4(context.Background(), Session4Config{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestRunSession4TemporalEndToEnd(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	control := newTestWorkerControlPlane(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunSession4(ctx, Session4Config{
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
		t.Fatalf("run session 4: %v", err)
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

func TestRunSession4TemporalLifecycle(t *testing.T) {
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
			if spec.Module != "bank-transfer" || spec.Version != "session-4" || spec.TaskCount != 1 || spec.LivenessTimeout != 50*time.Millisecond {
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
	if err := RunSession4(ctx, Session4Config{
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

func TestRunSession4StopsOnTemporalHeartbeatFailure(t *testing.T) {
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
	err := RunSession4(context.Background(), Session4Config{
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

func TestSession4TemporalConfigFromFlagsAndEnvironment(t *testing.T) {
	environment := map[string]string{
		"TEMPORAL_ADDRESS":              "temporal:7233",
		"TEMPORAL_NAMESPACE":            "workshop",
		"SESSION4_TEMPORAL_TASK_QUEUE":  "banktransfer-control",
		"SESSION4_TEMPORAL_WORKFLOW_ID": "banktransfer-lifecycle",
	}
	config, err := parseSession4Config([]string{"-temporal-heartbeat=2s", "-temporal-liveness-timeout=7s"}, func(name string) string {
		return environment[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "temporal:7233" || config.TemporalNamespace != "workshop" || config.TemporalTaskQueue != "banktransfer-control" || config.TemporalWorkflow != "banktransfer-lifecycle" || config.HeartbeatInterval != 2*time.Second || config.LivenessTimeout != 7*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

func TestRunSession4Errors(t *testing.T) {
	if err := RunSession4CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession4CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if strconv.IntSize == 64 {
		if err := RunSession4CLI(context.Background(), []string{"-tasks=4294967296"}); err == nil {
			t.Fatal("expected uint32 overflow error")
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession4(context.Background(), Session4Config{Address: "127.0.0.1:0", DataPath: filepath.Join(file, "store"), Tasks: 1}); err == nil {
		t.Fatal("expected Pebble open error")
	}
}
