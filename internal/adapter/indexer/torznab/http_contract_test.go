package torznab_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type torznabTransport func(*http.Request) (*http.Response, error)

func (f torznabTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type torznabTimeout struct{}

func (torznabTimeout) Error() string   { return "synthetic-body-secret: timed out" }
func (torznabTimeout) Timeout() bool   { return true }
func (torznabTimeout) Temporary() bool { return true }
func callTorznab(t *testing.T, c *torznab.Client, fetch bool, rawURL string) ([]byte, error) {
	t.Helper()
	if fetch {
		return c.FetchTorrent(context.Background(), rawURL)
	}
	_, e := c.Search(context.Background(), "movie", nil, 0, 50)
	return nil, e
}
func TestTorznabExternalErrorsDoNotExposeCredentials(t *testing.T) {
	for _, fetch := range []bool{false, true} {
		t.Run(fmt.Sprint(fetch), func(t *testing.T) {
			secret := "synthetic-torznab-passkey"
			transport := torznabTransport(func(r *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("remote failure %s %s", secret, r.URL.String())
			})
			client := torznab.NewClient("https://indexer.invalid", secret, &http.Client{Transport: transport})
			_, e := callTorznab(t, client, fetch, "https://indexer.invalid/file?passkey="+secret)
			if !errors.Is(e, ports.ErrUnavailable) || strings.Contains(e.Error(), secret) || strings.Contains(e.Error(), "https://") {
				t.Fatal("unsafe or untyped transport failure", e)
			}
		})
	}
	client := torznab.NewClient("https://indexer.invalid/%synthetic-torznab-passkey", "key", nil)
	_, e := client.Search(context.Background(), "movie", nil, 0, 50)
	if !errors.Is(e, ports.ErrBadResponse) || strings.Contains(e.Error(), "synthetic-torznab-passkey") {
		t.Fatal("unsafe or untyped URL failure", e)
	}
}
func TestTorznabFetchPreservesHTTPFailureContract(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ports.ErrAuthRequired}, {403, ports.ErrAuthRequired}, {404, ports.ErrNotFound}, {503, ports.ErrUnavailable}, {302, ports.ErrBadResponse}, {429, nil}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "synthetic-error-body-passkey")
			}))
			defer server.Close()
			_, e := torznab.NewClient(server.URL, "synthetic-api-key", nil).FetchTorrent(context.Background(), server.URL+"/download?passkey=synthetic-link-secret")
			if tc.status == 429 {
				rate, ok := ports.IsRateLimited(e)
				if !ok || rate.RetryAfter != 17*time.Second {
					t.Fatal(e)
				}
			} else if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
			if e == nil || strings.Contains(e.Error(), "synthetic-") {
				t.Fatal("unsafe HTTP error", e)
			}
		})
	}
}
func TestTorznabFetchRejectsTruncatedOversizeBody(t *testing.T) {
	for _, size := range []int{10 << 20, (10 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.CopyN(w, strings.NewReader(strings.Repeat("x", size)), int64(size))
			}))
			defer server.Close()
			data, e := torznab.NewClient(server.URL, "key", nil).FetchTorrent(context.Background(), server.URL+"/torrent")
			if size == 10<<20 {
				if e != nil || len(data) != size {
					t.Fatal(len(data), e)
				}
			} else if !errors.Is(e, ports.ErrBadResponse) || data != nil {
				t.Fatal("accepted truncated torrent body", len(data), e)
			}
		})
	}
}
func TestTorznabCancellationAndClientTimeout(t *testing.T) {
	for _, fetch := range []bool{false, true} {
		t.Run(fmt.Sprint(fetch), func(t *testing.T) {
			client := torznab.NewClient("https://indexer.invalid", "synthetic-key", &http.Client{Transport: torznabTransport(func(r *http.Request) (*http.Response, error) { return nil, torznabTimeout{} })})
			_, e := callTorznab(t, client, fetch, "https://indexer.invalid/torrent?passkey=synthetic-key")
			if !errors.Is(e, ports.ErrTimeout) || strings.Contains(e.Error(), "synthetic-") {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if fetch {
				_, e = client.FetchTorrent(ctx, "https://download.invalid/torrent")
			} else {
				_, e = client.Search(ctx, "movie", nil, 0, 50)
			}
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
}

type torznabFailBody struct {
	err    error
	closed bool
}

func (b *torznabFailBody) Read([]byte) (int, error) { return 0, b.err }
func (b *torznabFailBody) Close() error             { b.closed = true; return nil }
func TestTorznabBodyFailuresAreSafeAndClosed(t *testing.T) {
	for _, fetch := range []bool{false, true} {
		for _, timeout := range []bool{false, true} {
			t.Run(fmt.Sprintf("fetch=%v/timeout=%v", fetch, timeout), func(t *testing.T) {
				failure := errors.New("synthetic-body-secret")
				want := ports.ErrUnavailable
				if timeout {
					failure = torznabTimeout{}
					want = ports.ErrTimeout
				}
				body := &torznabFailBody{err: failure}
				client := torznab.NewClient("https://indexer.invalid", "synthetic-key", &http.Client{Transport: torznabTransport(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})})
				data, e := callTorznab(t, client, fetch, "https://indexer.invalid/torrent?passkey=synthetic-key")
				if !errors.Is(e, want) || data != nil || !body.closed || strings.Contains(e.Error(), "synthetic-") {
					t.Fatal("body failure contract", e, body.closed)
				}
			})
		}
	}
}
func TestTorznabRetryAdviceAndXMLAreSafe(t *testing.T) {
	for _, header := range []string{"9223372036854775807", "malformed-synthetic-secret", "http-date"} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := header
				if header == "http-date" {
					value = time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
				}
				w.Header().Set("Retry-After", value)
				w.WriteHeader(429)
			}))
			defer server.Close()
			for _, fetch := range []bool{false, true} {
				_, e := callTorznab(t, torznab.NewClient(server.URL, "key", nil), fetch, server.URL+"/file")
				rate, ok := ports.IsRateLimited(e)
				if !ok {
					t.Fatal(e)
				}
				if header == "9223372036854775807" && rate.RetryAfter != 24*time.Hour {
					t.Fatal(rate.RetryAfter)
				}
				if strings.HasPrefix(header, "malformed") && rate.RetryAfter != 10*time.Second {
					t.Fatal(rate.RetryAfter)
				}
				if header == "http-date" && (rate.RetryAfter < 40*time.Second || rate.RetryAfter > 46*time.Second) {
					t.Fatal(rate.RetryAfter)
				}
				if strings.Contains(e.Error(), "synthetic-") {
					t.Fatal("unsafe rate advice")
				}
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<rss><channel></synthetic-xml-passkey></rss>")
	}))
	defer server.Close()
	_, e := torznab.NewClient(server.URL, "key", nil).Search(context.Background(), "movie", nil, 0, 50)
	if !errors.Is(e, ports.ErrBadResponse) || strings.Contains(e.Error(), "synthetic-") {
		t.Fatal("unsafe XML error", e)
	}
}
func TestTorznabNativeClientTimeoutDoesNotBecomeUnavailable(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	_, e := torznab.NewClient(server.URL, "key", &http.Client{Timeout: 30 * time.Millisecond}).FetchTorrent(context.Background(), server.URL+"/file?passkey=synthetic-key")
	if !errors.Is(e, ports.ErrTimeout) || strings.Contains(e.Error(), "synthetic-") {
		t.Fatal(e)
	}
}
