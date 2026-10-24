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
	"time"
)

func seedingNative(t *testing.T, body string) (ports.SeedingReadClient, *atomic.Int32) {
	t.Helper()
	reads := &atomic.Int32{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.Method != http.MethodPost {
				t.Error("not native auth")
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			reads.Add(1)
			if r.Method != http.MethodGet || r.URL.Query().Get("hashes") != automaticTVTestHash || len(r.URL.Query()) != 1 {
				t.Error("incorrect identity query")
			}
			if _, err := r.Cookie("SID"); err != nil {
				t.Error("unauthenticated native read")
			}
			_, _ = io.WriteString(w, body)
		default:
			t.Error("unexpected mutation/probe", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(s.Close)
	c, err := NewClient(s.URL, "u", "fixture-secret", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c.SeedingObserver(), reads
}
func seedingBody(t *testing.T) string {
	row := automaticTVTestTask()
	row["seeding_time"] = int64(7200)
	row["ratio"] = 2.75
	row["downloaded"] = 999999999
	row["uploaded"] = 1
	row["added_on"] = 1
	return automaticTVTaskBody(t, row)
}
func TestSeedingNativeExactFacts(t *testing.T) {
	r, count := seedingNative(t, seedingBody(t))
	v, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
	if err != nil || v == nil || !v.Facts.Complete || v.InfoHash != automaticTVTestHash || v.OperationTag != automaticTVTestTag || v.Facts.SeedingSeconds == nil || *v.Facts.SeedingSeconds != 7200 || v.Facts.ShareRatio == nil || *v.Facts.ShareRatio != 2.75 || v.Facts.FreeBytes != nil || v.Facts.FreeSpaceMatchesDownloadPath || count.Load() != 1 {
		t.Fatal("native facts", v, err)
	}
	if _, ok := r.(ports.DownloadClient); ok {
		t.Fatal("read capability exposes mutations")
	}
}
func TestSeedingNativeUnknownsAndState(t *testing.T) {
	for _, name := range []string{"missing", "null", "zero", "capped", "negative-ratio", "incomplete", "checking"} {
		t.Run(name, func(t *testing.T) {
			row := automaticTVTestTask()
			switch name {
			case "null":
				row["ratio"] = nil
				row["seeding_time"] = nil
			case "zero":
				row["ratio"] = 0
				row["seeding_time"] = 0
			case "capped":
				row["ratio"] = 9999
			case "negative-ratio":
				row["ratio"] = -1
			case "incomplete":
				row["completed"] = 1
			case "checking":
				row["state"] = "checkingUP"
			}
			r, _ := seedingNative(t, automaticTVTaskBody(t, row))
			v, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
			if err != nil {
				t.Fatal(err)
			}
			if name == "zero" && (v.Facts.ShareRatio == nil || *v.Facts.ShareRatio != 0 || v.Facts.SeedingSeconds == nil || *v.Facts.SeedingSeconds != 0) {
				t.Fatal("zero lost")
			}
			if (name == "missing" || name == "null" || name == "negative-ratio") && v.Facts.ShareRatio != nil {
				t.Fatal("unknown inferred")
			}
			if name == "capped" && (v.Facts.ShareRatio == nil || *v.Facts.ShareRatio != 9999 || v.Facts.InfiniteShareRatio) {
				t.Fatal("capped became infinite")
			}
			if (name == "checking" || name == "incomplete") && v.Facts.Complete {
				t.Fatal("untrusted completion")
			}
		})
	}
}
func TestSeedingNativeRejectsIdentityAndMalformed(t *testing.T) {
	base := seedingBody(t)
	for _, tc := range []struct {
		name, body string
		want       error
	}{{"missing", "[]", ports.ErrNotFound}, {"wrong-hash", strings.Replace(base, automaticTVTestHash, strings.Repeat("a", 40), 1), ports.ErrUnavailable}, {"wrong-tag", strings.Replace(base, automaticTVTestTag, "other", 1), ports.ErrUnavailable}, {"duplicate-task", base[:len(base)-1] + "," + base[1:], ports.ErrUnavailable}, {"duplicate-key", strings.Replace(base, `"ratio":2.75`, `"ratio":2.75,"ratio":5`, 1), ports.ErrUnavailable}, {"bad-ratio", strings.Replace(base, `"ratio":2.75`, `"ratio":"private"`, 1), ports.ErrBadResponse}, {"fractional-seconds", strings.Replace(base, `"seeding_time":7200`, `"seeding_time":1.5`, 1), ports.ErrBadResponse}, {"negative-seconds", strings.Replace(base, `"seeding_time":7200`, `"seeding_time":-1`, 1), ports.ErrBadResponse}} {
		t.Run(tc.name, func(t *testing.T) {
			r, n := seedingNative(t, tc.body)
			if _, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag); !errors.Is(err, tc.want) || n.Load() != 1 {
				t.Fatal("bad native accepted/retried", err)
			}
		})
	}
}
func TestSeedingNativeInvalidInputNoIO(t *testing.T) {
	r, n := seedingNative(t, seedingBody(t))
	for _, v := range [][2]string{{"bad", automaticTVTestTag}, {strings.ToUpper(automaticTVTestHash), automaticTVTestTag}, {automaticTVTestHash, "pulsemedia:op:bad"}, {automaticTVTestHash, "pulsemedia:op:00000000-0000-0000-0000-000000000000"}} {
		if _, err := r.ReadSeedingTorrent(t.Context(), v[0], v[1]); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.ReadSeedingTorrent(ctx, automaticTVTestHash, automaticTVTestTag); !errors.Is(err, context.Canceled) || n.Load() != 0 {
		t.Fatal("cancel/input network", err)
	}
}
func TestSeedingNativeCancellationAndStatus(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var reads atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/auth/login") {
					_, _ = io.WriteString(w, "Ok.")
					return
				}
				reads.Add(1)
				w.WriteHeader(code)
				_, _ = io.WriteString(w, "private-native")
			}))
			defer s.Close()
			c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
			_, err := c.SeedingObserver().ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
			if err == nil || strings.Contains(err.Error(), "private-native") || reads.Load() != 1 {
				t.Fatal("unsafe failure/replay", err)
			}
		})
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/login") {
			_, _ = io.WriteString(w, "Ok.")
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); s.Close() }()
	c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.SeedingObserver().ReadSeedingTorrent(ctx, automaticTVTestHash, automaticTVTestTag)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not return")
	}
}
