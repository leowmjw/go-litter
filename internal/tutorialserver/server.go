// Package tutorialserver serves the Rama Tutorial 1–5 stages as interactive
// DataStar-backed HTML pages. Each page is self-contained: an explanation,
// a small form, and a live view of the underlying PState.
package tutorialserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/starfederation/datastar-go/datastar"

	"app/internal/storage"
	"app/internal/tutorial"
)

const (
	// datastarBundle is the stable DataStar v1 browser bundle used with the
	// v1.2.0 Go SDK. v1.0.2 is chosen to avoid the very recently released
	// v1.0.3 while still getting the v1.x API used by the SDK.
	datastarBundle = `https://cdn.jsdelivr.net/gh/starfederation/datastar@v1.0.2/bundles/datastar.js`
	defaultAddr    = "127.0.0.1:8081"
)

var pageTmpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
    <script type="module" src="` + datastarBundle + `"></script>
    <style>
        body { font-family: system-ui, -apple-system, BlinkMacSystemFont, sans-serif; max-width: 72ch; margin: 2rem auto; padding: 0 1rem; line-height: 1.6; }
        h1, h2 { color: #222; }
        label { display: block; margin: .5rem 0; }
        input, select, button { font: inherit; padding: .35rem .6rem; margin: .25rem 0; }
        button { cursor: pointer; }
        pre { background: #f5f5f5; padding: 1rem; border-radius: .35rem; overflow-x: auto; }
        .status { color: #666; font-style: italic; }
        nav { margin: 1rem 0; padding: .75rem 0; border-top: 1px solid #ddd; border-bottom: 1px solid #ddd; }
        nav a { margin-right: .75rem; }
        .card { background: #fafafa; border: 1px solid #eee; border-radius: .5rem; padding: 1rem; margin: 1rem 0; }
        code { background: #eee; padding: .1rem .3rem; border-radius: .2rem; }
    </style>
</head>
<body>
    <div data-signals='{{.SignalsJSON}}'>
        <h1>{{.Title}}</h1>
        <p>{{.Description}}</p>
        <nav>
            <a href="/">Home</a>
            <a href="/stage1">Stage 1</a>
            <a href="/stage2">Stage 2</a>
            <a href="/stage3">Stage 3</a>
            <a href="/stage4">Stage 4</a>
            <a href="/stage5">Stage 5</a>
            <a href="/stage6">Stage 6</a>
        </nav>
        <main>
            {{.Content}}
        </main>
        <p class="status">Status: <span data-text="$status">ready</span></p>
    </div>
</body>
</html>
`))

type pageData struct {
	Title       string
	Description string
	Content     template.HTML
	SignalsJSON string
}

// statusSignal is present on every page so the server can give feedback.
type statusSignal struct {
	Status string `json:"status"`
}

func signalsJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func htmlEscape(value string) string {
	return html.EscapeString(value)
}

func writePage(w http.ResponseWriter, p pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, p); err != nil {
		slog.Error("render tutorial page", "error", err)
	}
}

func readSignals[T any](r *http.Request) (T, error) {
	var s T
	if err := datastar.ReadSignals(r, &s); err != nil {
		return s, fmt.Errorf("read signals: %w", err)
	}
	return s, nil
}

func patchSignals[T any](sse *datastar.ServerSentEventGenerator, s T) error {
	return sse.MarshalAndPatchSignals(s)
}

// Server hosts the interactive tutorial pages.
type Server struct {
	addr string
	mux  *http.ServeMux

	stage1 *tutorial.Stage1Module
	stage2 *tutorial.Stage2Module
	stage3 *tutorial.Stage3Module

	stage5Stream *tutorial.Stage5StreamModule
	stage5Micro  *tutorial.Stage5MicrobatchModule
}

// New creates a tutorial server using in-memory stores.
func New(addr string) (*Server, error) {
	if addr == "" {
		addr = defaultAddr
	}

	stage1, err := tutorial.NewStage1(storage.NewMemory(), 4)
	if err != nil {
		return nil, fmt.Errorf("stage1: %w", err)
	}

	stage2, err := tutorial.NewStage2(storage.NewMemory(), 4)
	if err != nil {
		return nil, fmt.Errorf("stage2: %w", err)
	}

	stage3, err := tutorial.NewStage3(storage.NewMemory(), 4)
	if err != nil {
		return nil, fmt.Errorf("stage3: %w", err)
	}

	// Stage 5 stream and microbatch share a module name internally, so give
	// each its own store to keep their checkpoints independent.
	stage5Stream, err := tutorial.NewStage5Stream(storage.NewMemory(), 4)
	if err != nil {
		return nil, fmt.Errorf("stage5 stream: %w", err)
	}
	stage5Micro, err := tutorial.NewStage5Microbatch(storage.NewMemory(), 4)
	if err != nil {
		return nil, fmt.Errorf("stage5 microbatch: %w", err)
	}

	s := &Server{
		addr:         addr,
		mux:          http.NewServeMux(),
		stage1:       stage1,
		stage2:       stage2,
		stage3:       stage3,
		stage5Stream: stage5Stream,
		stage5Micro:  stage5Micro,
	}

	s.addIndexRoutes()
	s.addStage1Routes()
	s.addStage2Routes()
	s.addStage3Routes()
	s.addStage4Routes()
	s.addStage5Routes()
	s.addStage6Placeholder()

	return s, nil
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe starts the tutorial HTTP server and blocks until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0, // SSE connections can be long-lived.
		IdleTimeout:       time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func (s *Server) addIndexRoutes() {
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Rama Tutorial Walkthrough",
			Description: "Interactive Go/Temporal port of the Rama quick tutorial. Work through Stages 1–5, then launch Stage 6 (RamaSpace).",
			SignalsJSON: signalsJSON(statusSignal{Status: "ready"}),
			Content: template.HTML(`
<div class="card">
    <h2>Stages</h2>
    <ol>
        <li><a href="/stage1">Stage 1 — First Module</a>: depot, stream ETL, PState, query.</li>
        <li><a href="/stage2">Stage 2 — Depots, ETLs, and PStates</a>: scalar, map, set, list, fixed-key records.</li>
        <li><a href="/stage3">Stage 3 — Distributed Programming</a>: partitions, repartitioning, co-location.</li>
        <li><a href="/stage4">Stage 4 — Dataflow Programming</a>: branches, bindings, unification, loops.</li>
        <li><a href="/stage5">Stage 5 — Stream vs Microbatch</a>: acknowledged stream vs batched exactly-once microbatch.</li>
        <li><a href="/stage6">Stage 6 — RamaSpace</a>: full social-network capstone.</li>
    </ol>
</div>
`),
		})
	})
}

func errorStatus(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	if msg == "" {
		return "error"
	}
	// Keep the status line short for the UI.
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 120 {
		msg = msg[:120] + "..."
	}
	return msg
}
