package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func seedSpaceNumber(n int64) *int64 { return &n }
func TestSeedingSpaceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, initial, after, body string
		want                       *int64
		failure                    bool
		calls                      int
	}{
		{"zero", "/downloads", "/downloads", `{"full_update":true,"server_state":{"free_space_on_disk":0}}`, seedSpaceNumber(0), false, 3},
		{"precise-integer", "/downloads/", "/downloads", `{"full_update":true,"server_state":{"free_space_on_disk":9007199254740993}}`, seedSpaceNumber(9007199254740993), false, 3},
		{"different-path", "/elsewhere", "/elsewhere", `{}`, nil, false, 1},
		{"child-not-proven-same-disk", "/", "/", `{}`, nil, false, 1},
		{"changed-default", "/downloads", "/elsewhere", `{"full_update":true,"server_state":{"free_space_on_disk":1}}`, nil, false, 3},
		{"unknown", "/downloads", "/downloads", `{"full_update":true,"server_state":{}}`, nil, false, 2},
		{"negative-unknown", "/downloads", "/downloads", `{"full_update":true,"server_state":{"free_space_on_disk":-1}}`, nil, false, 2},
		{"partial", "/downloads", "/downloads", `{"full_update":false,"server_state":{"free_space_on_disk":0}}`, nil, true, 2},
		{"fractional", "/downloads", "/downloads", `{"full_update":true,"server_state":{"free_space_on_disk":0.5}}`, nil, true, 2},
		{"duplicate", "/downloads", "/downloads", `{"full_update":true,"server_state":{"free_space_on_disk":0,"free_space_on_disk":1}}`, nil, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, defaults atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/app/defaultSavePath":
					reads.Add(1)
					if r.Method != http.MethodGet {
						t.Error("not native read")
					}
					v := tc.initial
					if defaults.Add(1) > 1 {
						v = tc.after
					}
					_, _ = io.WriteString(w, v)
				case "/api/v2/sync/maindata":
					reads.Add(1)
					if r.Method != http.MethodGet || r.URL.Query().Get("rid") != "0" || len(r.URL.Query()) != 1 {
						t.Error("not fresh full native read")
					}
					_, _ = io.WriteString(w, tc.body)
				default:
					t.Error("unexpected native endpoint", r.URL.Path)
				}
			}))
			defer s.Close()
			c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
			got, err := c.SeedingObserver().ReadSeedingFreeSpace(t.Context(), "/downloads")
			if (err != nil) != tc.failure || got.Path != "/downloads" || reads.Load() != int32(tc.calls) {
				t.Fatal(got, err, reads.Load())
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
	if _, err := r.ReadSeedingFreeSpace(t.Context(), "/downloads"); err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("unsafe failure", err)
	}
}
