package jellyfin_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type failingTransport func(*http.Request) (*http.Response, error)

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutFailure struct{}

func (timeoutFailure) Error() string   { return "private-token-needle transport timeout" }
func (timeoutFailure) Timeout() bool   { return true }
func (timeoutFailure) Temporary() bool { return true }
func operations(c *jellyfin.Client) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{"refresh": func(ctx context.Context) error { return c.RefreshPaths(ctx, []string{"/media/movie.mkv"}) }, "find": func(ctx context.Context) error { _, _, e := c.FindByPath(ctx, "/media/movie.mkv"); return e }, "test": c.Test}
}
func TestJellyfinSafeTypedHTTPFailures(t *testing.T) {
	const secret = "private-token-needle"
	for _, failure := range []struct {
		name             string
		cause, errorType error
	}{{"transport", fmt.Errorf("remote %s http://private.invalid/", secret), ports.ErrUnavailable}, {"timeout", timeoutFailure{}, ports.ErrTimeout}, {"cancel", context.Canceled, context.Canceled}} {
		c := jellyfin.NewClient("http://private.invalid/?credential="+secret, secret, &http.Client{Transport: failingTransport(func(*http.Request) (*http.Response, error) { return nil, failure.cause })})
		for name, op := range operations(c) {
			t.Run(failure.name+"/"+name, func(t *testing.T) {
				e := op(context.Background())
				if !errors.Is(e, failure.errorType) {
					t.Fatal("wrong error type", e)
				}
				if strings.Contains(fmt.Sprint(e), secret) || strings.Contains(fmt.Sprint(e), "http://") {
					t.Fatal("unsafe error", e)
				}
			})
		}
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled-context", func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), context.Canceled},
		{"expired-context", func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			cancel()
			return ctx
		}(), ports.ErrTimeout},
	} {
		c := jellyfin.NewClient("http://private.invalid/", secret, &http.Client{Transport: failingTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("private %s", secret) })})
		for name, op := range operations(c) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				if e := op(tc.ctx); !errors.Is(e, tc.want) || strings.Contains(fmt.Sprint(e), secret) {
					t.Fatal("wrong context error", e)
				}
			})
		}
	}
	for _, status := range []struct {
		code int
		want error
	}{{401, ports.ErrAuthRequired}, {403, ports.ErrAuthRequired}, {429, nil}, {503, ports.ErrUnavailable}, {302, ports.ErrBadResponse}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status.code) }))
		c := jellyfin.NewClient(server.URL, secret, nil)
		for name, op := range operations(c) {
			t.Run(fmt.Sprint(status.code)+"/"+name, func(t *testing.T) {
				e := op(context.Background())
				valid := errors.Is(e, status.want)
				if status.code == 429 {
					_, valid = ports.IsRateLimited(e)
				}
				if !valid {
					t.Fatal("wrong status error", e)
				}
			})
		}
		server.Close()
	}
	for name, op := range operations(jellyfin.NewClient("http://private.invalid/%zz?credential="+secret, secret, nil)) {
		t.Run("badurl/"+name, func(t *testing.T) {
			e := op(context.Background())
			if !errors.Is(e, ports.ErrBadResponse) || strings.Contains(fmt.Sprint(e), secret) {
				t.Fatal("unsafe construction failure", e)
			}
		})
	}
}
