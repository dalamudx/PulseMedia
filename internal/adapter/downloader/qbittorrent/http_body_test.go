package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"testing"
)

type checkedBody struct {
	io.Reader
	closed bool
}

func (b *checkedBody) Close() error { b.closed = true; return nil }

type brokenHTTPBody struct{ failure error }

func (b brokenHTTPBody) Read([]byte) (int, error) { return 0, b.failure }
func TestQBResponseReadAndJSONBoundaries(t *testing.T) {
	for _, operation := range []string{"login", "get", "list", "files"} {
		for n, failure := range []error{errors.New("synthetic-qbit-secret read failure"), contractTimeout{}, context.Canceled} {
			t.Run(operation+"/"+failure.Error(), func(t *testing.T) {
				b := &checkedBody{Reader: brokenHTTPBody{failure}}
				c, e := NewClient("https://synthetic-qbit.invalid", "fixture", "fixture", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: b}, nil
				})})
				if e != nil {
					t.Fatal(e)
				}
				c.loggedIn = operation != "login"
				e = contractCall(c, operation, context.Background())
				want := []error{ports.ErrUnavailable, ports.ErrTimeout, context.Canceled}[n]
				if !errors.Is(e, want) || !b.closed || strings.Contains(e.Error(), "synthetic-qbit") {
					t.Fatalf("read/close contract: error=%v closed=%t", e, b.closed)
				}
			})
		}
	}
	for _, operation := range []string{"get", "list"} {
		for _, tc := range []struct {
			name, body string
			want       error
		}{{"empty", "[]", nil}, {"null", "null", ports.ErrBadResponse}, {"trailing-array", "[] []", ports.ErrBadResponse}, {"malformed", "[synthetic-qbit-secret", ports.ErrBadResponse}, {"exact-limit", "[]" + strings.Repeat(" ", int(torrentResponseLimit)-2), nil}, {"oversize", "[]" + strings.Repeat(" ", int(torrentResponseLimit)-1), ports.ErrBadResponse}} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				b := &checkedBody{Reader: strings.NewReader(tc.body)}
				c, e := NewClient("https://synthetic-qbit.invalid", "fixture", "fixture", &http.Client{Transport: contractTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: b}, nil
				})})
				if e != nil {
					t.Fatal(e)
				}
				c.loggedIn = true
				e = contractCall(c, operation, context.Background())
				want := tc.want
				if want == nil && operation == "get" {
					want = ports.ErrNotFound
				}
				if !errors.Is(e, want) || !b.closed {
					t.Fatalf("JSON boundary %v closed=%t", e, b.closed)
				}
				if e != nil && strings.Contains(e.Error(), "synthetic-qbit") {
					t.Fatal("malformed body leaked")
				}
			})
		}
	}
}
func TestQBExpiredSessionReloginsWithoutReplayingOperation(t *testing.T) {
	for _, operation := range []string{"add", "get", "list", "remove", "files"} {
		t.Run(operation, func(t *testing.T) {
			logins, calls := 0, 0
			c, e := NewClient("https://synthetic-qbit.invalid", "fixture", "fixture", &http.Client{Transport: contractTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, "[]"
				if r.URL.Path == "/api/v2/auth/login" {
					logins++
					body = "Ok."
				} else {
					calls++
					if calls == 1 {
						status = 401
					} else if operation == "add" || operation == "remove" {
						body = "Ok."
					} else if operation == "files" {
						body = `[{"index":0,"name":"one.mkv","size":1,"progress":1}]`
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			if e != nil {
				t.Fatal(e)
			}
			c.loggedIn = true
			e = contractCall(c, operation, context.Background())
			if !errors.Is(e, ports.ErrAuthRequired) || calls != 1 || logins != 0 {
				t.Fatalf("unexpected implicit replay: %v", e)
			}
			e = contractCall(c, operation, context.Background())
			if operation == "get" {
				if !errors.Is(e, ports.ErrNotFound) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if calls != 2 || logins != 1 {
				t.Fatalf("expected fresh login: %d %d", calls, logins)
			}
		})
	}
}
