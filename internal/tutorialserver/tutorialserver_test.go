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
	if !strings.Contains(body, `<dd>20</dd>`) || !strings.Contains(body, `<dd>21</dd>`) {
		t.Fatalf("unexpected linear output: %s", body)
	}
}

func TestStage4BranchUnifyPipeline(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"branch-unify","n":"0"}`)
	if !strings.Contains(body, `<dd>left-value</dd>`) || !strings.Contains(body, `<dd>right-value</dd>`) {
		t.Fatalf("unexpected branch output: %s", body)
	}
}

func TestStage4LoopPipeline(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"loop","n":"5"}`)
	if !strings.Contains(body, `<dd>15</dd>`) {
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

func TestStage2RendersStructuredPStates(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	postSSE(t, ts, "/stage2/record", `{"user":"alice","tag":"sports","score":"10"}`)
	body := postSSE(t, ts, "/stage2/inspect", `{"inspectUser":"alice"}`)
	for _, want := range []string{`Global total count`, `<li>sports</li>`, `<li>sports</li>`, `events=1`, `highScore=10`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Stage 2 output missing %q: %s", want, body)
		}
	}
}

func TestStage3RendersPartitionedInbox(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	postSSE(t, ts, "/stage3/send", `{"sender":"alice","recipient":"bob","message":"hello"}`)
	body := postSSE(t, ts, "/stage3/inspect", `{"inspectUser":"bob"}`)
	if !strings.Contains(body, `lives on task`) || !strings.Contains(body, `<li>alice: hello</li>`) {
		t.Fatalf("unexpected Stage 3 output: %s", body)
	}
}

func TestStage4RendersBindingCards(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage4/run", `{"example":"branch-unify","n":"3"}`)
	if !strings.Contains(body, `<h4>Output 1</h4>`) || !strings.Contains(body, `<h4>Output 2</h4>`) {
		t.Fatalf("unexpected Stage 4 fragment: %s", body)
	}
}

func TestStage5RendersPendingAndCommittedStates(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage5/microbatch-ping", `{"key":"z"}`)
	if !strings.Contains(body, `depot append pending; PState unchanged`) {
		t.Fatalf("pending state not rendered: %s", body)
	}
	body = postSSE(t, ts, "/stage5/advance", `{"key":"z"}`)
	if !strings.Contains(body, `checkpoint committed`) || !strings.Contains(body, `<p class="metric">1</p>`) {
		t.Fatalf("committed state not rendered: %s", body)
	}
}

func TestStage6InteractiveCapstone(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	body := postSSE(t, ts, "/stage6/seed", `{}`)
	if !strings.Contains(body, `<strong>Alice</strong>`) || !strings.Contains(body, `<strong>Bob</strong>`) {
		t.Fatalf("seeded profiles not rendered: %s", body)
	}
	body = postSSE(t, ts, "/stage6/request", `{}`)
	if !strings.Contains(body, `<li>bob</li>`) || !strings.Contains(body, `<li>alice</li>`) {
		t.Fatalf("friend request not rendered bidirectionally: %s", body)
	}
	body = postSSE(t, ts, "/stage6/accept", `{}`)
	if !strings.Contains(body, `Friend count</dt><dd>1</dd>`) {
		t.Fatalf("friendship not rendered: %s", body)
	}
	body = postSSE(t, ts, "/stage6/post", `{}`)
	if !strings.Contains(body, `No materialized posts`) {
		t.Fatalf("post must remain invisible before advance: %s", body)
	}
	postSSE(t, ts, "/stage6/view", `{}`)
	body = postSSE(t, ts, "/stage6/advance", `{}`)
	if !strings.Contains(body, `Hello from the RamaSpace tutorial`) || !strings.Contains(body, `Current hour bucket`) {
		t.Fatalf("microbatch effects not rendered after advance: %s", body)
	}
}

func TestStage6RawAPIIsMounted(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stage6/api/healthz")
	if err != nil {
		t.Fatalf("GET Stage 6 health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Stage 6 health status = %d", resp.StatusCode)
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
