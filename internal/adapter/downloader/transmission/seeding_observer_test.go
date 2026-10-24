package transmission

import (
	"context"
	"encoding/json"
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
		reads.Add(1)
		var input rpcRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&input) != nil || input.Method != "torrent-get" {
			t.Error("unexpected native mutation/probe")
		}
		args := input.Arguments.(map[string]any)
		ids, _ := args["ids"].([]any)
		fields, _ := args["fields"].([]any)
		if len(ids) != 1 || ids[0] != automaticTVTestHash || len(fields) != 9 {
			t.Error("wrong native identity fields")
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	c, err := NewClient(s.URL, "fixture-user", "fixture-secret", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c.SeedingObserver(), reads
}
func seedingBody() string {
	row := automaticTVTestTask()
	row["secondsSeeding"] = int64(7200)
	row["uploadRatio"] = 2.75
	row["downloadedEver"] = 99999999
	row["uploadedEver"] = 1
	row["addedDate"] = 1
	return automaticTVTestBody(row)
}
func TestSeedingNativeExactFacts(t *testing.T) {
	r, n := seedingNative(t, seedingBody())
	v, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
	if err != nil || v == nil || !v.Facts.Complete || v.InfoHash != automaticTVTestHash || v.OperationTag != automaticTVTestTag || v.Facts.SeedingSeconds == nil || *v.Facts.SeedingSeconds != 7200 || v.Facts.ShareRatio == nil || *v.Facts.ShareRatio != 2.75 || v.Facts.FreeBytes != nil || v.Facts.FreeSpaceMatchesDownloadPath || n.Load() != 1 {
		t.Fatal("native facts", v, err)
	}
	if _, ok := r.(ports.DownloadClient); ok {
		t.Fatal("read capability exposes mutations")
	}
}
func TestSeedingNativeUnknownsAndState(t *testing.T) {
	for _, name := range []string{"missing", "null", "zero", "infinite", "unknown-ratio", "incomplete", "checking"} {
		t.Run(name, func(t *testing.T) {
			row := automaticTVTestTask()
			switch name {
			case "null":
				row["uploadRatio"] = nil
				row["secondsSeeding"] = nil
			case "zero":
				row["uploadRatio"] = 0
				row["secondsSeeding"] = 0
			case "infinite":
				row["uploadRatio"] = -2
			case "unknown-ratio":
				row["uploadRatio"] = -1
			case "incomplete":
				row["haveValid"] = 1
			case "checking":
				row["status"] = 2
			}
			r, _ := seedingNative(t, automaticTVTestBody(row))
			v, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
			if err != nil {
				t.Fatal(err)
			}
			if name == "zero" && (v.Facts.ShareRatio == nil || *v.Facts.ShareRatio != 0 || v.Facts.SeedingSeconds == nil || *v.Facts.SeedingSeconds != 0) {
				t.Fatal("zero lost")
			}
			if (name == "missing" || name == "null" || name == "unknown-ratio") && v.Facts.ShareRatio != nil {
				t.Fatal("unknown inferred")
			}
			if name == "infinite" && (!v.Facts.InfiniteShareRatio || v.Facts.ShareRatio != nil) {
				t.Fatal("native infinity lost")
			}
			if (name == "checking" || name == "incomplete") && v.Facts.Complete {
				t.Fatal("untrusted completion")
			}
		})
	}
}
func TestSeedingNativeRejectsIdentityAndMalformed(t *testing.T) {
	base := seedingBody()
	for _, tc := range []struct {
		name, body string
		want       error
	}{{"missing", automaticTVTestBody(), ports.ErrNotFound}, {"wrong-hash", strings.Replace(base, strings.ToUpper(automaticTVTestHash), strings.Repeat("a", 40), 1), ports.ErrUnavailable}, {"wrong-tag", strings.Replace(base, automaticTVTestTag, "other", 1), ports.ErrUnavailable}, {"duplicate-task", automaticTVTestBody(automaticTVTestTask(), automaticTVTestTask()), ports.ErrUnavailable}, {"duplicate-key", strings.Replace(base, `"uploadRatio":2.75`, `"uploadRatio":2.75,"uploadRatio":5`, 1), ports.ErrUnavailable}, {"case-alias", strings.Replace(base, `"uploadRatio":2.75`, `"UPLOADRATIO":2.75`, 1), ports.ErrUnavailable}, {"bad-ratio", strings.Replace(base, `"uploadRatio":2.75`, `"uploadRatio":"private"`, 1), ports.ErrBadResponse}, {"fractional-seconds", strings.Replace(base, `"secondsSeeding":7200`, `"secondsSeeding":1.5`, 1), ports.ErrBadResponse}, {"negative-seconds", strings.Replace(base, `"secondsSeeding":7200`, `"secondsSeeding":-1`, 1), ports.ErrBadResponse}} {
		t.Run(tc.name, func(t *testing.T) {
			r, n := seedingNative(t, tc.body)
			if _, err := r.ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag); !errors.Is(err, tc.want) || n.Load() != 1 {
				t.Fatal("bad native accepted/retried", err)
			}
		})
	}
}
func TestSeedingNativeInvalidInputNoIO(t *testing.T) {
	r, n := seedingNative(t, seedingBody())
	for _, v := range [][2]string{{"bad", automaticTVTestTag}, {strings.ToUpper(automaticTVTestHash), automaticTVTestTag}, {automaticTVTestHash, "pulsemedia:op:bad"}} {
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
func TestSeedingNativeCancellationAndSession(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") != "owned-session" {
			w.Header().Set("X-Transmission-Session-Id", "owned-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = io.WriteString(w, seedingBody())
	}))
	defer s.Close()
	c, _ := NewClient(s.URL, "u", "fixture-secret", s.Client())
	if _, err := c.SeedingObserver().ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag); err != nil || calls.Load() != 2 {
		t.Fatal("read-only session negotiation", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	block := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); block.Close() }()
	c, _ = NewClient(block.URL, "u", "fixture-secret", block.Client())
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
