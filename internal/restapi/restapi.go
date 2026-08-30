package restapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"app/internal/partition"
	"app/internal/storage"
	"app/internal/stream"
)

const (
	ModuleName      storage.ModuleID   = "rest-api"
	GetDepot        storage.DepotID    = "get"
	ResponsesState  storage.StateID    = "responses"
	GetHttpTopology storage.TopologyID = "get-http"
)

const maxResponseBytes = 4 << 20

var (
	ErrEmptyURL         = errors.New("url is required")
	ErrUnsafeURL        = errors.New("only public HTTP and HTTPS URLs are allowed")
	ErrResponseTooLarge = errors.New("response body exceeds size limit")
)

// Fetch is a dependency-injectable HTTP fetcher. The default implementation
// performs outbound HTTP GET requests; callers may override it for testing or
// to add URL allowlisting before production use.
type Module struct {
	Fetch       func(context.Context, string) (string, error)
	Append      func(context.Context, string) error
	Replay      func(context.Context) error
	Rebuild     func(context.Context) error
	GetResponse func(context.Context, string) (string, bool, error)
}

func New(store *storage.Store, taskCount uint32) (*Module, error) {
	choosePartition, err := partition.New(taskCount)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if isPublicIP(address.IP) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			}
		}
		return nil, ErrUnsafeURL
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return validateURL(req.URL)
		},
	}
	module := &Module{Fetch: newHTTPFetcher(client)}

	runtime, err := stream.New(ModuleName, taskCount, choosePartition, store,
		stream.Source{
			Depot:        GetDepot,
			Topology:     GetHttpTopology,
			PartitionKey: func(payload []byte) ([]byte, error) { return payload, nil },
			Handle: func(ctx context.Context, event *stream.Event, record storage.Record, task uint32) ([]byte, error) {
				url := string(record.Payload)
				if url == "" {
					return nil, ErrEmptyURL
				}
				body, err := module.Fetch(ctx, url)
				if err != nil {
					return nil, err
				}
				event.Set(storage.StatePartition{Module: ModuleName, State: ResponsesState, Partition: task}, []byte(url), []byte(body))
				return []byte(body), nil
			},
		})
	if err != nil {
		return nil, err
	}

	module.Append = func(ctx context.Context, url string) error {
		if url == "" {
			return ErrEmptyURL
		}
		_, err := runtime.Append(ctx, GetDepot, "", []byte(url))
		return err
	}
	module.Replay = runtime.Replay
	module.Rebuild = runtime.Rebuild
	module.GetResponse = func(ctx context.Context, url string) (string, bool, error) {
		value, found, err := store.GetState(ctx, storage.StatePartition{Module: ModuleName, State: ResponsesState, Partition: choosePartition([]byte(url))}, []byte(url))
		if err != nil || !found {
			return "", found, err
		}
		return string(value), true, nil
	}
	return module, nil
}

func newHTTPFetcher(client *http.Client) func(context.Context, string) (string, error) {
	return func(ctx context.Context, rawURL string) (string, error) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return "", err
		}
		if err := validateURL(parsed); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return "", fmt.Errorf("GET %s: status %d", parsed.Redacted(), resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return "", err
		}
		if len(body) > maxResponseBytes {
			return "", ErrResponseTooLarge
		}
		return string(body), nil
	}
}

func validateURL(parsed *url.URL) error {
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return ErrUnsafeURL
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !isPublicIP(ip) {
		return ErrUnsafeURL
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}
