package restapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"app/internal/storage"
)

func TestRestAPIModule(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 4)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}

	m.Fetch = func(ctx context.Context, url string) (string, error) {
		return fmt.Sprintf("mock body for %s", url), nil
	}

	url1 := "https://example.com/joke/1"
	url2 := "https://example.com/joke/2"

	if err := m.Append(ctx, url1); err != nil {
		t.Fatalf("append url1: %v", err)
	}
	if err := m.Append(ctx, url2); err != nil {
		t.Fatalf("append url2: %v", err)
	}
	if err := m.Append(ctx, url1); err != nil {
		t.Fatalf("append url1 again: %v", err)
	}

	body1, found, err := m.GetResponse(ctx, url1)
	if err != nil {
		t.Fatalf("get response: %v", err)
	}
	if !found || body1 != "mock body for "+url1 {
		t.Fatalf("response = %q, found=%v", body1, found)
	}

	body2, found, err := m.GetResponse(ctx, url2)
	if err != nil || !found || body2 != "mock body for "+url2 {
		t.Fatalf("response2 = %q, found=%v, err=%v", body2, found, err)
	}
}

func TestRestAPIModuleEnqueueAsync(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 4)
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	fetchCalls := 0
	m.Fetch = func(ctx context.Context, url string) (string, error) {
		fetchCalls++
		return fmt.Sprintf("body for %s", url), nil
	}

	url1 := "https://example.com/joke/1"
	position, err := m.Enqueue(ctx, url1)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if position == 0 {
		t.Fatal("expected a positive depot position")
	}
	if fetchCalls != 0 {
		t.Fatalf("enqueue must not fetch synchronously: calls = %d", fetchCalls)
	}
	if _, found, err := m.GetResponse(ctx, url1); err != nil || found {
		t.Fatalf("response before replay: found=%v err=%v", found, err)
	}
	if err := m.Replay(ctx); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("replay must fetch once: calls = %d", fetchCalls)
	}
	body, found, err := m.GetResponse(ctx, url1)
	if err != nil || !found || body != "body for "+url1 {
		t.Fatalf("response = %q, found=%v, err=%v", body, found, err)
	}
	// Replay is idempotent: no duplicate fetch, same response.
	if err := m.Replay(ctx); err != nil {
		t.Fatalf("replay again: %v", err)
	}
	if fetchCalls != 1 {
		t.Fatalf("replay must be idempotent: calls = %d", fetchCalls)
	}
}

func TestRestAPIModuleEnqueueValidation(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	m, err := New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(context.Background(), ""); !errors.Is(err, ErrEmptyURL) {
		t.Fatalf("empty url error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestHTTPFetcher(t *testing.T) {
	response := func(status int, body string) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
	}
	body, err := newHTTPFetcher(response(http.StatusOK, "ok"))(context.Background(), "https://example.com")
	if err != nil || body != "ok" {
		t.Fatalf("body = %q, err=%v", body, err)
	}
	if _, err := newHTTPFetcher(response(http.StatusBadGateway, "bad"))(context.Background(), "https://example.com"); err == nil {
		t.Fatal("expected status error")
	}
	large := strings.Repeat("x", maxResponseBytes+1)
	if _, err := newHTTPFetcher(response(http.StatusOK, large))(context.Background(), "https://example.com"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("large response error = %v", err)
	}
	if _, err := newHTTPFetcher(response(http.StatusOK, "ignored"))(context.Background(), "http://127.0.0.1"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("unsafe URL error = %v", err)
	}
	if _, err := newHTTPFetcher(response(http.StatusOK, "ignored"))(context.Background(), ":"); err == nil {
		t.Fatal("expected URL parse error")
	}
	transportErr := errors.New("transport failed")
	failingClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	if _, err := newHTTPFetcher(failingClient)(context.Background(), "https://example.com"); !errors.Is(err, transportErr) {
		t.Fatalf("transport error = %v", err)
	}
	readFailureClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(failingReader{})}, nil
	})}
	if _, err := newHTTPFetcher(readFailureClient)(context.Background(), "https://example.com"); err == nil {
		t.Fatal("expected response read error")
	}
}

func TestDefaultFetcherRejectsResolvedLoopback(t *testing.T) {
	store := storage.NewMemory()
	defer store.Close()
	module, err := New(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.Fetch(context.Background(), "http://localhost"); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("localhost fetch error = %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	for _, rawURL := range []string{"file:///tmp/a", "http://127.0.0.1", "http://169.254.169.254", "https://user:pass@example.com"} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateURL(parsed); !errors.Is(err, ErrUnsafeURL) {
			t.Fatalf("validateURL(%q) = %v", rawURL, err)
		}
	}
	parsed, err := url.Parse("https://example.com/path")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateURL(parsed); err != nil {
		t.Fatalf("public URL rejected: %v", err)
	}
}
