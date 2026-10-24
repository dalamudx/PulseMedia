package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func seedSpaceNumber(n int64) *int64 { return &n }
func TestSeedingSpaceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       *int64
		failure    bool
	}{
		{"zero", `{"result":"success","arguments":{"path":"/downloads","size-bytes":0}}`, seedSpaceNumber(0), false},
		{"precise-integer", `{"result":"success","arguments":{"path":"/downloads","size-bytes":9007199254740993}}`, seedSpaceNumber(9007199254740993), false},
		{"missing", `{"result":"success","arguments":{"path":"/downloads"}}`, nil, false},
		{"null", `{"result":"success","arguments":{"path":"/downloads","size-bytes":null}}`, nil, false},
		{"unknown", `{"result":"success","arguments":{"path":"/downloads","size-bytes":-1}}`, nil, false},
		{"wrong-path", `{"result":"success","arguments":{"path":"/elsewhere","size-bytes":0}}`, nil, true},
		{"fractional", `{"result":"success","arguments":{"path":"/downloads","size-bytes":0.5}}`, nil, true},
		{"duplicate", `{"result":"success","arguments":{"path":"/downloads","size-bytes":0,"size-bytes":1}}`, nil, true},
		{"duplicate-outer", `{"result":"failure","result":"success","arguments":{"path":"/downloads","size-bytes":0}}`, nil, true},
		{"unsupported", `{"result":"method name not recognized","arguments":{}}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req struct {
					Method    string         `json:"method"`
					Arguments map[string]any `json:"arguments"`
				}
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "free-space" || req.Arguments["path"] != "/downloads" || len(req.Arguments) != 1 {
					t.Error("not exact path native read")
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
			got, err := c.SeedingObserver().ReadSeedingFreeSpace(t.Context(), "/downloads")
			if (err != nil) != tc.failure || got.Path != "/downloads" || calls.Load() != 1 {
				t.Fatal(got, err)
			}
			if tc.want == nil {
				if got.FreeBytes != nil {
					t.Fatal("unknown became known")
				}
			} else if got.FreeBytes == nil || *got.FreeBytes != *tc.want {
				t.Fatal("zero/integer precision lost")
			}
		})
	}
}
func TestSeedingSpaceInvalidAndCancelledNoIO(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer s.Close()
	c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
	r := c.SeedingObserver()
	for _, p := range []string{"", "relative", "/downloads/../else", "/downloads\n"} {
		if _, err := r.ReadSeedingFreeSpace(t.Context(), p); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.ReadSeedingFreeSpace(ctx, "/downloads"); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("cancel/input network", err)
	}
	if _, err := c.automaticTVReadRPC(t.Context(), "torrent-remove", map[string]any{}); !errors.Is(err, ports.ErrRejected) || calls.Load() != 0 {
		t.Fatal("read transport permits mutation", err)
	}
}
