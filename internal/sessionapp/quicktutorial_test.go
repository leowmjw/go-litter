package sessionapp

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"app/internal/controlplane"
	"app/internal/ramaspace"
	"app/internal/storage"

	"go.temporal.io/sdk/testsuite"
)

// TestRunQuickTutorialTemporalEndToEnd proves worker death is detected by
// virtual liveness time and that a clean stop reaches the Stopped state,
// reusing the same Temporal lifecycle Workflow every session command uses.
func TestRunQuickTutorialTemporalEndToEnd(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	control := newTestWorkerControlPlane(t, env)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunQuickTutorial(ctx, QuickTutorialConfig{
			Address:           "127.0.0.1:0",
			Memory:            true,
			Tasks:             1,
			SchemaVersion:     ramaspace.SchemaVersion2,
			Interval:          time.Hour,
			HeartbeatInterval: time.Hour,
			LivenessTimeout:   2 * time.Hour,
			ControlPlane:      control,
		})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run quick-tutorial: %v", err)
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

// TestModuleLifecycleDetectsQuickTutorialWorkerDeath drives the shared
// lifecycle Workflow directly with RamaSpace's module identity through a
// virtual one-minute liveness timeout (no heartbeat delivered), proving
// dead-worker detection without any real sleep.
func TestModuleLifecycleDetectsQuickTutorialWorkerDeath(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(controlplane.StartSignal, nil) }, time.Second)
	env.ExecuteWorkflow(controlplane.ModuleLifecycle, controlplane.LifecycleSpec{
		Module: ramaspace.ModuleName, Version: "quick-tutorial", TaskCount: 4, LivenessTimeout: time.Minute,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state controlplane.LifecycleState
	if err := env.GetWorkflowResult(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != controlplane.Failed || state.LastFailure != "liveness timeout" {
		t.Fatalf("state = %#v", state)
	}
}

func TestRunQuickTutorialValidationAndErrors(t *testing.T) {
	if err := RunQuickTutorial(context.Background(), QuickTutorialConfig{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunQuickTutorial(context.Background(), QuickTutorialConfig{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunQuickTutorial(context.Background(), QuickTutorialConfig{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunQuickTutorial(context.Background(), QuickTutorialConfig{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestQuickTutorialHandlerFullFlow(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := ramaspace.New(store, 2, ramaspace.SchemaVersion2, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := QuickTutorialHandler(module)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var reader *bytes.Buffer
		if body == "" {
			reader = bytes.NewBufferString("")
		} else {
			reader = bytes.NewBufferString(body)
		}
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
		return rec
	}

	if rec := do(http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}

	badJSON := do(http.MethodPost, "/users", "{")
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}

	register := do(http.MethodPost, "/users", `{"userId":"alice","email":"a@example.com","displayName":"Alice","passwordHash":"hash","registrationUuid":"uuid-1"}`)
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d body=%s", register.Code, register.Body.String())
	}
	dup := do(http.MethodPost, "/users", `{"userId":"alice","email":"x@example.com","displayName":"X","passwordHash":"hash2","registrationUuid":"uuid-2"}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d", dup.Code)
	}

	login := do(http.MethodPost, "/login", `{"userId":"alice","passwordHash":"hash"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	badLogin := do(http.MethodPost, "/login", `{"userId":"alice","passwordHash":"wrong"}`)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d", badLogin.Code)
	}

	get := do(http.MethodGet, "/users/alice", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d", get.Code)
	}
	var profile ramaspace.Profile
	if err := json.Unmarshal(get.Body.Bytes(), &profile); err != nil || profile.DisplayName != "Alice" {
		t.Fatalf("profile = %+v, %v", profile, err)
	}

	edit := do(http.MethodPatch, "/users/alice", `{"field":"bio","value":"hi"}`)
	if edit.Code != http.StatusNoContent {
		t.Fatalf("edit status = %d body=%s", edit.Code, edit.Body.String())
	}
	missingEdit := do(http.MethodGet, "/users/missing", "")
	if missingEdit.Code != http.StatusNotFound {
		t.Fatalf("missing user status = %d", missingEdit.Code)
	}

	if _, err := module.RegisterUser(ctx, ramaspace.UserRegistration{UserID: "bob", Email: "b@example.com", DisplayName: "Bob", PasswordHash: "h", RegistrationUUID: "uuid-bob"}); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	freq := do(http.MethodPost, "/friends/requests", `{"userId":"bob","destUserId":"alice"}`)
	if freq.Code != http.StatusAccepted {
		t.Fatalf("friend request status = %d body=%s", freq.Code, freq.Body.String())
	}
	incoming := do(http.MethodGet, "/friends/alice/incoming", "")
	if incoming.Code != http.StatusOK {
		t.Fatalf("incoming status = %d", incoming.Code)
	}
	accept := do(http.MethodPost, "/friends", `{"userId":"alice","destUserId":"bob"}`)
	if accept.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d body=%s", accept.Code, accept.Body.String())
	}
	count := do(http.MethodGet, "/friends/alice/count", "")
	if count.Code != http.StatusOK {
		t.Fatalf("count status = %d", count.Code)
	}
	var countBody map[string]uint64
	if err := json.Unmarshal(count.Body.Bytes(), &countBody); err != nil || countBody["count"] != 1 {
		t.Fatalf("count body = %v, %v", countBody, err)
	}
	remove := do(http.MethodDelete, "/friends", `{"userId":"alice","destUserId":"bob"}`)
	if remove.Code != http.StatusNoContent {
		t.Fatalf("remove status = %d", remove.Code)
	}

	post := do(http.MethodPost, "/posts", `{"author":"bob","destination":"alice","content":"hello"}`)
	if post.Code != http.StatusAccepted {
		t.Fatalf("post status = %d body=%s", post.Code, post.Body.String())
	}
	advance := do(http.MethodPost, "/admin/advance", "")
	if advance.Code != http.StatusOK {
		t.Fatalf("advance status = %d body=%s", advance.Code, advance.Body.String())
	}
	postsPage := do(http.MethodGet, "/posts/alice?limit=20", "")
	if postsPage.Code != http.StatusOK {
		t.Fatalf("posts page status = %d body=%s", postsPage.Code, postsPage.Body.String())
	}
	var page ramaspace.PostPage
	if err := json.Unmarshal(postsPage.Body.Bytes(), &page); err != nil || len(page.Posts) != 1 || page.Posts[0].AuthorDisplayName != "Bob" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	postCount := do(http.MethodGet, "/posts/alice/count", "")
	if postCount.Code != http.StatusOK {
		t.Fatalf("post count status = %d", postCount.Code)
	}

	view := do(http.MethodPost, "/profile-views", `{"userId":"alice","timestampMillis":1700000000000}`)
	if view.Code != http.StatusAccepted {
		t.Fatalf("view status = %d body=%s", view.Code, view.Body.String())
	}
	if _, err := module.Advance(ctx); err != nil {
		t.Fatalf("advance for view: %v", err)
	}
	hour := int64(1700000000000) / int64(time.Hour/time.Millisecond)
	viewCount := do(http.MethodGet, "/profile-views/alice/count?from="+itoa64(hour)+"&to="+itoa64(hour+1), "")
	if viewCount.Code != http.StatusOK {
		t.Fatalf("view count status = %d body=%s", viewCount.Code, viewCount.Body.String())
	}

	migrate := do(http.MethodPost, "/admin/migrate-profiles", "")
	if migrate.Code != http.StatusOK {
		t.Fatalf("migrate status = %d", migrate.Code)
	}
	rebuild := do(http.MethodPost, "/admin/rebuild", "")
	if rebuild.Code != http.StatusOK {
		t.Fatalf("rebuild status = %d body=%s", rebuild.Code, rebuild.Body.String())
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	digits := "0123456789"
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append([]byte{digits[v%10]}, buf...)
		v /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
