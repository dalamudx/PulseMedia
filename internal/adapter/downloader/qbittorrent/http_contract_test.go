package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type contractTransport func(*http.Request) (*http.Response, error)

func (f contractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type contractTimeout struct{}

func (contractTimeout) Error() string { return "synthetic-qbit-credential-url" }
func (contractTimeout) Timeout() bool { return true }
func contractCall(c *Client, name string, ctx context.Context) error {
	switch name {
	case "login":
		return c.Test(ctx)
	case "add":
		return c.AddTorrent(ctx, ports.AddTorrentParam{DownloadURL: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)})
	case "get":
		_, e := c.GetTorrent(ctx, strings.Repeat("a", 40))
		return e
	case "list":
		_, e := c.ListTorrents(ctx, "synthetic-tag")
		return e
	case "remove":
		return c.RemoveTorrent(ctx, strings.Repeat("a", 40), false)
	default:
		_, e := c.GetTorrentFiles(ctx, strings.Repeat("a", 40))
		return e
	}
}

var contractOperations = []string{"login", "add", "get", "list", "remove", "files"}

func TestQBExternalHTTPFailuresSafeAndTyped(t *testing.T) {
	for _, operation := range contractOperations {
		for _, tc := range []struct {
			name          string
			ctx           context.Context
			failure, want error
		}{{"transport", context.Background(), errors.New("synthetic-qbit-secret synthetic-qbit-credential-url"), ports.ErrUnavailable}, {"timeout", context.Background(), contractTimeout{}, ports.ErrTimeout}, {"deadline", context.Background(), context.DeadlineExceeded, ports.ErrTimeout}, {"cancel", context.Background(), context.Canceled, context.Canceled}} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				c, e := NewClient("https://synthetic-qbit.invalid/private-credential", "synthetic-user", "synthetic-secret", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) { return nil, tc.failure })})
				if e != nil {
					t.Fatal(e)
				}
				c.loggedIn = operation != "login"
				e = contractCall(c, operation, tc.ctx)
				if !errors.Is(e, tc.want) {
					t.Fatalf("error %v want %v", e, tc.want)
				}
				for _, secret := range []string{"synthetic-qbit", "private-credential", "synthetic-tag"} {
					if strings.Contains(e.Error(), secret) {
						t.Fatal("external error leaked fixture material")
					}
				}
			})
		}
	}
}
func TestQBHTTPStatusesRemainDistinct(t *testing.T) {
	for _, operation := range contractOperations {
		for _, tc := range []struct {
			status int
			want   error
		}{{401, ports.ErrAuthRequired}, {403, ports.ErrAuthRequired}, {404, ports.ErrNotFound}, {408, ports.ErrTimeout}, {429, nil}, {500, ports.ErrUnavailable}, {503, ports.ErrUnavailable}, {504, ports.ErrTimeout}, {302, ports.ErrBadResponse}} {
			t.Run(operation+"/"+http.StatusText(tc.status), func(t *testing.T) {
				c, e := NewClient("https://synthetic-qbit.invalid", "fixture", "fixture", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{"3"}}, Body: io.NopCloser(strings.NewReader("synthetic-qbit-secret"))}, nil
				})})
				if e != nil {
					t.Fatal(e)
				}
				c.loggedIn = operation != "login"
				e = contractCall(c, operation, context.Background())
				if tc.status == 429 {
					rate, ok := ports.IsRateLimited(e)
					if !ok || rate.RetryAfter != 3*time.Second {
						t.Fatalf("rate advice lost: %v", e)
					}
				} else if !errors.Is(e, tc.want) {
					t.Fatalf("status %d error %v want %v", tc.status, e, tc.want)
				}
				if e == nil || strings.Contains(e.Error(), "synthetic-qbit") {
					t.Fatal("unsafe HTTP error")
				}
			})
		}
	}
}
func TestQBBadRequestURLDoesNotEscape(t *testing.T) {
	for _, operation := range contractOperations {
		t.Run(operation, func(t *testing.T) {
			c, e := NewClient("http://synthetic-qbit-secret@%zz", "fixture", "fixture", nil)
			if e != nil {
				return
			}
			c.loggedIn = operation != "login"
			e = contractCall(c, operation, context.Background())
			if !errors.Is(e, ports.ErrBadResponse) || strings.Contains(e.Error(), "synthetic-qbit") {
				t.Fatalf("invalid URL error %v", e)
			}
		})
	}
}
