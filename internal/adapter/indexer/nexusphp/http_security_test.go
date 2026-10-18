package nexusphp_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
)

type securityTransport func(*http.Request) (*http.Response, error)

func (f securityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type securityBody struct {
	io.Reader
	failure error
	closed  bool
}

func (b *securityBody) Read(p []byte) (int, error) {
	if b.failure != nil {
		return 0, b.failure
	}
	return b.Reader.Read(p)
}
func (b *securityBody) Close() error { b.closed = true; return nil }

type securityTimeout struct{}

func (securityTimeout) Error() string   { return "synthetic-timeout-secret" }
func (securityTimeout) Timeout() bool   { return true }
func (securityTimeout) Temporary() bool { return true }

func TestNexusPHPSafeFailuresAndResponseLimit(t *testing.T) {
	for _, fetch := range []bool{false, true} {
		for _, kind := range []string{"transport", "timeout", "body", "limit", "empty", "exact-limit", "forbidden", "unavailable", "redirect"} {
			t.Run(kind+map[bool]string{false: "/search", true: "/fetch"}[fetch], func(t *testing.T) {
				body := &securityBody{Reader: strings.NewReader("<html></html>")}
				want := error(ports.ErrUnavailable)
				status := 200
				switch kind {
				case "timeout":
					want = ports.ErrTimeout
				case "body":
					body.failure = errors.New("GET http://host/?passkey=synthetic-body-secret")
				case "limit":
					body.Reader = strings.NewReader(strings.Repeat("x", (10<<20)+1))
					want = ports.ErrBadResponse
				case "empty":
					body.Reader = strings.NewReader("")
					want = ports.ErrBadResponse
				case "exact-limit":
					body.Reader = strings.NewReader(strings.Repeat("x", 10<<20))
					want = nil
				case "forbidden":
					status = 403
					want = ports.ErrAuthRequired
				case "unavailable":
					status = 503
				case "redirect":
					status = 302
					want = ports.ErrBadResponse
				}
				called := false
				client := nexusphp.NewClient("https://indexer.invalid", "session=synthetic-cookie", "synthetic-passkey", &http.Client{Transport: securityTransport(func(r *http.Request) (*http.Response, error) {
					called = true
					if kind == "transport" {
						return nil, errors.New("GET https://indexer.invalid/?passkey=synthetic-link-secret")
					}
					if kind == "timeout" {
						return nil, securityTimeout{}
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
				})})
				var err error
				if fetch {
					_, err = client.FetchTorrent(context.Background(), "https://indexer.invalid/file?passkey=synthetic-passkey")
				} else {
					_, err = client.Search(context.Background(), "movie", nil, 0, 20)
				}
				if !errors.Is(err, want) || (err != nil && strings.Contains(err.Error(), "synthetic-")) || !called {
					t.Fatalf("failure contract: %v called=%v", err, called)
				}
				if kind != "transport" && kind != "timeout" && !body.closed {
					t.Fatal("response body not closed")
				}
			})
		}
	}
}

func TestNexusPHPCancellationDoesNotRequest(t *testing.T) {
	called := false
	client := nexusphp.NewClient("https://indexer.invalid", "session=synthetic-cookie", "", &http.Client{Transport: securityTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected") })})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.FetchTorrent(ctx, "https://other.invalid/file")
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancel contract %v called=%v", err, called)
	}
}
