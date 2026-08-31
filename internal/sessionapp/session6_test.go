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
	"app/internal/musiccatalog"
	"app/internal/storage"
)

func TestSession6Handler(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	module, err := musiccatalog.NewB(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session6Handler(module)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	badJSON := httptest.NewRecorder()
	handler.ServeHTTP(badJSON, httptest.NewRequest(http.MethodPost, "/albums", bytes.NewBufferString("{")))
	if badJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", badJSON.Code)
	}
	invalid := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"artist":"Frank Ocean","name":"Channel Orange","songs":[]}`)
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/albums", body))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid album status = %d", invalid.Code)
	}
	album := httptest.NewRecorder()
	body = bytes.NewBufferString(`{"artist":"Frank Ocean","name":"Channel Orange","songs":["White feat. John Mayer"]}`)
	handler.ServeHTTP(album, httptest.NewRequest(http.MethodPost, "/albums", body))
	if album.Code != http.StatusAccepted {
		t.Fatalf("album status = %d body=%s", album.Code, album.Body.String())
	}
	if _, err := module.AdvanceAll(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	fetch := httptest.NewRecorder()
	handler.ServeHTTP(fetch, httptest.NewRequest(http.MethodGet, "/album?artist=Frank%20Ocean&name=Channel%20Orange", nil))
	if fetch.Code != http.StatusOK {
		t.Fatalf("album fetch status = %d body=%s", fetch.Code, fetch.Body.String())
	}
	var result musiccatalog.Album
	if err := json.Unmarshal(fetch.Body.Bytes(), &result); err != nil || len(result.Songs) != 1 || result.Songs[0].Name != "White" || len(result.Songs[0].FeaturedArtists) != 1 || result.Songs[0].FeaturedArtists[0] != "John Mayer" {
		t.Fatalf("album = %#v, %v", result, err)
	}
	count := httptest.NewRecorder()
	handler.ServeHTTP(count, httptest.NewRequest(http.MethodGet, "/albums/count?artist=Frank%20Ocean", nil))
	if count.Code != http.StatusOK {
		t.Fatalf("count status = %d body=%s", count.Code, count.Body.String())
	}
	var countResult struct {
		Artist string `json:"artist"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(count.Body.Bytes(), &countResult); err != nil || countResult.Count != 1 {
		t.Fatalf("count = %#v, %v", countResult, err)
	}
	missingArtist := httptest.NewRecorder()
	handler.ServeHTTP(missingArtist, httptest.NewRequest(http.MethodGet, "/album?name=Channel%20Orange", nil))
	if missingArtist.Code != http.StatusBadRequest {
		t.Fatalf("missing artist status = %d", missingArtist.Code)
	}
	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/album?artist=Missing&name=Album", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", notFound.Code)
	}
}

func TestSession6HandlerErrors(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := musiccatalog.NewB(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := Session6Handler(module)
	injected := errors.New("injected")
	originalAppend := module.Append
	module.Append = func(context.Context, musiccatalog.AlbumInput) error { return injected }
	if recorder := requestJSON(t, handler, http.MethodPost, "/albums", musiccatalog.AlbumInput{Artist: "A", Name: "B", Songs: []string{"s"}}); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("append failure status = %d", recorder.Code)
	}
	module.Append = originalAppend
	originalGet := module.GetAlbum
	module.GetAlbum = func(context.Context, string, string) (musiccatalog.Album, bool, error) { return musiccatalog.Album{}, false, injected }
	if recorder := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/album?artist=A&name=B", nil))
		return recorder.Code != http.StatusInternalServerError
	}() {
		t.Fatalf("get album failure status = %d", recorder.Code)
	}
	module.GetAlbum = originalGet
	originalCount := module.CountAlbums
	module.CountAlbums = func(context.Context, string) (int, error) { return 0, injected }
	if recorder := httptest.NewRecorder(); func() bool {
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/albums/count?artist=A", nil))
		return recorder.Code != http.StatusInternalServerError
	}() {
		t.Fatalf("count failure status = %d", recorder.Code)
	}
	module.CountAlbums = originalCount
	module.Rebuild = func(context.Context) error { return injected }
	rebuild := httptest.NewRecorder()
	handler.ServeHTTP(rebuild, httptest.NewRequest(http.MethodPost, "/admin/rebuild", nil))
	if rebuild.Code != http.StatusInternalServerError {
		t.Fatalf("rebuild failure status = %d", rebuild.Code)
	}
}

func TestSession6LiveMigration(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	moduleA, err := musiccatalog.NewA(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := moduleA.Append(ctx, musiccatalog.AlbumInput{Artist: "Post Malone", Name: "F-1 Trillion", Songs: []string{"Have The Heart ft. Dolly Parton"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := moduleA.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	count, err := moduleA.CountAlbums(ctx, "Post Malone")
	if err != nil || count != 1 {
		t.Fatalf("count = %d, %v", count, err)
	}
	album, found, err := moduleA.GetAlbum(ctx, "Post Malone", "F-1 Trillion")
	if err != nil || !found || album.Name != "F-1 Trillion" || len(album.Songs) != 1 || album.Songs[0].Name != "Have The Heart" || len(album.Songs[0].FeaturedArtists) != 1 || album.Songs[0].FeaturedArtists[0] != "Dolly Parton" {
		t.Fatalf("v1 album = %#v, found=%v, err=%v", album, found, err)
	}

	moduleB, err := musiccatalog.NewB(store, 4)
	if err != nil {
		t.Fatal(err)
	}
	album, found, err = moduleB.GetAlbum(ctx, "Post Malone", "F-1 Trillion")
	if err != nil || !found || album.Songs[0].Name != "Have The Heart" || len(album.Songs[0].FeaturedArtists) != 1 || album.Songs[0].FeaturedArtists[0] != "Dolly Parton" {
		t.Fatalf("migrated album = %#v, found=%v, err=%v", album, found, err)
	}
	if err := moduleB.Append(ctx, musiccatalog.AlbumInput{Artist: "Frank Ocean", Name: "Channel Orange", Songs: []string{"White feat. John Mayer"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := moduleB.AdvanceAll(ctx); err != nil {
		t.Fatal(err)
	}
	album, found, err = moduleB.GetAlbum(ctx, "Frank Ocean", "Channel Orange")
	if err != nil || !found || album.Songs[0].Name != "White" || len(album.Songs[0].FeaturedArtists) != 1 || album.Songs[0].FeaturedArtists[0] != "John Mayer" {
		t.Fatalf("v2 album = %#v, found=%v, err=%v", album, found, err)
	}
}

func TestRunSession6ValidationAndErrors(t *testing.T) {
	if err := RunSession6(context.Background(), Session6Config{Interval: 0}); err == nil {
		t.Fatal("expected interval error")
	}
	if err := RunSession6(context.Background(), Session6Config{Interval: time.Second, Version: "C"}); err == nil {
		t.Fatal("expected version error")
	}
	if err := RunSession6(context.Background(), Session6Config{Interval: time.Second, Tasks: math.MaxUint32 + 1}); err == nil {
		t.Fatal("expected task count error")
	}
	badData := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badData, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession6(context.Background(), Session6Config{Interval: time.Second, DataPath: badData}); err == nil {
		t.Fatal("expected Pebble open error")
	}
	if err := RunSession6(context.Background(), Session6Config{Interval: time.Second, Memory: true, Address: "invalid:address"}); err == nil {
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
	if err := RunSession6(context.Background(), Session6Config{Interval: time.Second, Memory: true, Address: "127.0.0.1:0", Tasks: 1, Version: "B", ControlPlane: control}); err == nil {
		t.Fatal("expected control plane start error")
	}
}

func TestRunSession6TemporalLifecycle(t *testing.T) {
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
			if spec.Module != "music-catalog" || spec.Version != "B" || spec.TaskCount != 1 || spec.LivenessTimeout != 50*time.Millisecond {
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
	if err := RunSession6(ctx, Session6Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
		Version:           "B",
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

func TestRunSession6StopsOnTemporalHeartbeatFailure(t *testing.T) {
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
	err := RunSession6(context.Background(), Session6Config{
		Address:           "127.0.0.1:0",
		Memory:            true,
		Tasks:             1,
		Interval:          time.Hour,
		Version:           "B",
		HeartbeatInterval: time.Millisecond,
		LivenessTimeout:   50 * time.Millisecond,
		ControlPlane:      control,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want %v", err, injected)
	}
}

func TestSession6TemporalConfigFromFlagsAndEnvironment(t *testing.T) {
	environment := map[string]string{
		"TEMPORAL_ADDRESS":              "temporal:7233",
		"TEMPORAL_NAMESPACE":            "workshop",
		"SESSION6_TEMPORAL_TASK_QUEUE":  "musiccatalog-control",
		"SESSION6_TEMPORAL_WORKFLOW_ID": "musiccatalog-lifecycle",
	}
	config, err := parseSession6Config([]string{"-temporal-heartbeat=2s", "-temporal-liveness-timeout=7s"}, func(name string) string {
		return environment[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TemporalAddress != "temporal:7233" || config.TemporalNamespace != "workshop" || config.TemporalTaskQueue != "musiccatalog-control" || config.TemporalWorkflow != "musiccatalog-lifecycle" || config.HeartbeatInterval != 2*time.Second || config.LivenessTimeout != 7*time.Second {
		t.Fatalf("config = %#v", config)
	}
}

func TestRunSession6Errors(t *testing.T) {
	if err := RunSession6CLI(context.Background(), []string{"-unknown"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := RunSession6CLI(context.Background(), []string{"-memory", "-tasks=3"}); err == nil {
		t.Fatal("expected task-count error")
	}
	if err := RunSession6CLI(context.Background(), []string{"-memory", "-version=C"}); err == nil {
		t.Fatal("expected version error")
	}
	if strconv.IntSize == 64 {
		if err := RunSession6CLI(context.Background(), []string{"-tasks=4294967296"}); err == nil {
			t.Fatal("expected uint32 overflow error")
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession6(context.Background(), Session6Config{Address: "127.0.0.1:0", DataPath: filepath.Join(file, "store"), Tasks: 1}); err == nil {
		t.Fatal("expected Pebble open error")
	}
}
