package tutorialserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	server, err := New("ignored")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server, httptest.NewServer(server.Handler())
}

func postSSE(t *testing.T, ts *httptest.Server, path string, body string) string {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d", path, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("POST %s content-type = %q, want text/event-stream", path, got)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestStage1PageAndSSE(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stage1")
	if err != nil {
		t.Fatalf("GET /stage1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stage1 status = %d", resp.StatusCode)
	}

	body := postSSE(t, ts, "/stage1/say", `{"name":"alice","message":"hello"}`)
	if !strings.Contains(body, `"last":"hello"`) {
		t.Fatalf("SSE body does not contain updated last message: %s", body)
	}
}

func TestStage4LinearPipeline(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"linear","n":"10"}`)
	if !strings.Contains(body, `doubled = 20`) || !strings.Contains(body, `plusOne = 21`) {
		t.Fatalf("unexpected linear output: %s", body)
	}
}

func TestStage4BranchUnifyPipeline(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"branch-unify","n":"0"}`)
	if !strings.Contains(body, `tag = left-value`) || !strings.Contains(body, `tag = right-value`) {
		t.Fatalf("unexpected branch output: %s", body)
	}
}

func TestStage4LoopPipeline(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"loop","n":"5"}`)
	if !strings.Contains(body, `sum = 15`) {
		t.Fatalf("unexpected loop output: %s", body)
	}
}

func TestStage5StreamVisibleImmediately(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage5/stream-ping", `{"key":"x"}`)
	if !strings.Contains(body, `"streamCount":"1"`) {
		t.Fatalf("stream count should be visible immediately: %s", body)
	}
}

func TestStage5MicrobatchNeedsAdvance(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage5/microbatch-ping", `{"key":"y"}`)
	if strings.Contains(body, `"microbatchCount":"1"`) {
		t.Fatalf("microbatch count should not be visible before advance: %s", body)
	}

	body = postSSE(t, ts, "/stage5/advance", `{"key":"y"}`)
	if !strings.Contains(body, `"microbatchCount":"1"`) {
		t.Fatalf("microbatch count should be 1 after advance: %s", body)
	}

	// A second advance with no new records has no additional effect.
	body = postSSE(t, ts, "/stage5/advance", `{"key":"y"}`)
	if bytes.Count([]byte(body), []byte(`"microbatchCount":"1"`)) != 1 {
		t.Fatalf("second advance should still report 1: %s", body)
	}
}

func TestIndexPage(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", resp.StatusCode)
	}
}
