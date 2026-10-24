package qbittorrent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type seedingRemovalFixture struct {
	client                    *Client
	body                      atomic.Value
	reads, removes, redirects atomic.Int32
	entered, release          chan struct{}
	once                      sync.Once
}

func newSeedingRemovalFixture(t *testing.T, mode string, tls bool) *seedingRemovalFixture {
	t.Helper()
	f := &seedingRemovalFixture{entered: make(chan struct{}), release: make(chan struct{})}
	f.body.Store(seedingBody(t))
	var s *httptest.Server
	s = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			f.reads.Add(1)
			_, _ = io.WriteString(w, f.body.Load().(string))
		case "/api/v2/torrents/delete":
			f.removes.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil || len(r.PostForm) != 2 || r.PostForm.Get("hashes") != automaticTVTestHash || r.PostForm.Get("deleteFiles") != "false" {
				t.Error("removal lost exact task or requested data deletion")
			}
			if _, err := r.Cookie("SID"); err != nil {
				t.Error("unauthenticated native removal")
			}
			switch mode {
			case "lost":
				if tls {
					panic(http.ErrAbortHandler)
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			case "redirect":
				http.Redirect(w, r, s.URL+"/owned-redirect", http.StatusTemporaryRedirect)
			case "bad-ack":
				_, _ = io.WriteString(w, "unexpected response")
			case "rate-limit":
				w.WriteHeader(http.StatusTooManyRequests)
			case "auth":
				w.WriteHeader(http.StatusForbidden)
			case "blocked":
				close(f.entered)
				<-f.release
			default:
				w.WriteHeader(http.StatusOK)
			}
		case "/owned-redirect":
			f.redirects.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Error("unexpected protocol call", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	if tls {
		s.EnableHTTP2 = true
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(func() { f.once.Do(func() { close(f.release) }); s.Close() })
	var err error
	f.client, err = NewClient(s.URL, "owned-user", "owned-secret", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *seedingRemovalFixture) freeze(t *testing.T) ports.SeedingRemovalTarget {
	t.Helper()
	observed, err := f.client.SeedingObserver().ReadSeedingTorrent(t.Context(), automaticTVTestHash, automaticTVTestTag)
	if err != nil {
		t.Fatal(err)
	}
	return ports.SeedingRemovalTarget{InfoHash: observed.InfoHash, OperationTag: observed.OperationTag, SavePath: observed.SavePath, TotalBytes: observed.TotalBytes}
}
func (f *seedingRemovalFixture) cap(t *testing.T, target ports.SeedingRemovalTarget) ports.SeedingRemovalClient {
	t.Helper()
	cap, err := f.client.SeedingRemover(target, func(context.Context, ports.SeedingRemoteTorrent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return cap
}
func TestSeedingRemovalSingleUse(t *testing.T) {
	f := newSeedingRemovalFixture(t, "success", false)
	cap := f.cap(t, f.freeze(t))
	out, err := cap.RemoveRetainingFiles(t.Context())
	if err != nil || out != ports.SeedingRemovalAcknowledged {
		t.Fatal("removal not acknowledged", out, err)
	}
	out, err = cap.RemoveRetainingFiles(t.Context())
	if out != ports.SeedingRemovalNotDispatched || !errors.Is(err, ports.ErrRejected) || f.removes.Load() != 1 || f.reads.Load() != 2 {
		t.Fatal("capability replayed", out, err)
	}
}
func TestSeedingRemovalRejectsChangedFacts(t *testing.T) {
	for _, kind := range []string{"hash", "tag", "path", "bytes", "partial", "malformed", "absent"} {
		t.Run(kind, func(t *testing.T) {
			f := newSeedingRemovalFixture(t, "success", false)
			cap := f.cap(t, f.freeze(t))
			row := automaticTVTestTask()
			switch kind {
			case "hash":
				row["hash"] = strings.Repeat("b", 40)
			case "tag":
				row["tags"] = "unowned"
			case "path":
				row["save_path"] = "/changed"
			case "bytes":
				row["total_size"] = int64(6)
				row["completed"] = int64(6)
			case "partial":
				row["progress"] = 0
				row["completed"] = int64(0)
				row["state"] = "downloading"
			}
			body := automaticTVTaskBody(t, row)
			if kind == "malformed" {
				body = "{"
			}
			if kind == "absent" {
				body = "[]"
			}
			f.body.Store(body)
			out, err := cap.RemoveRetainingFiles(t.Context())
			if kind == "absent" {
				if out != ports.SeedingRemovalAbsent || err != nil {
					t.Fatal("absence not read-only", out, err)
				}
			} else if out != ports.SeedingRemovalNotDispatched || err == nil {
				t.Fatal("changed original was removed", out, err)
			}
			if f.removes.Load() != 0 {
				t.Fatal("changed task received mutation")
			}
		})
	}
}
func TestSeedingRemovalUnknownDoesNotReplay(t *testing.T) {
	for _, tc := range []struct {
		mode string
		tls  bool
	}{{"lost", false}, {"lost", true}, {"redirect", false}, {"bad-ack", false}, {"rate-limit", false}, {"auth", false}} {
		name := tc.mode
		if tc.tls {
			name += "-http2"
		}
		t.Run(name, func(t *testing.T) {
			f := newSeedingRemovalFixture(t, tc.mode, tc.tls)
			cap := f.cap(t, f.freeze(t))
			out, err := cap.RemoveRetainingFiles(t.Context())
			if err == nil || out != ports.SeedingRemovalUnknown {
				t.Fatal("uncertain mutation not retained", out, err)
			}
			if out, err = cap.RemoveRetainingFiles(t.Context()); out != ports.SeedingRemovalNotDispatched || !errors.Is(err, ports.ErrRejected) {
				t.Fatal("uncertain request replayable", out, err)
			}
			if f.removes.Load() != 1 || f.redirects.Load() != 0 {
				t.Fatal("mutation retried or redirected", f.removes.Load(), f.redirects.Load())
			}
		})
	}
}
func TestSeedingRemovalInvalidAndCancelled(t *testing.T) {
	f := newSeedingRemovalFixture(t, "success", false)
	target := f.freeze(t)
	before := f.reads.Load()
	for _, kind := range []string{"hash", "tag", "path", "bytes"} {
		bad := target
		switch kind {
		case "hash":
			bad.InfoHash = "all"
		case "tag":
			bad.OperationTag = "pulsemedia"
		case "path":
			bad.SavePath = "../escape"
		case "bytes":
			bad.TotalBytes = 0
		}
		if _, err := f.client.SeedingRemover(bad, nil); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid binding accepted", kind, err)
		}
	}
	cap := f.cap(t, target)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := cap.RemoveRetainingFiles(ctx)
	if out != ports.SeedingRemovalNotDispatched || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled permit dispatched", out, err)
	}
	if _, err = cap.RemoveRetainingFiles(t.Context()); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("cancelled object reused")
	}
	if f.reads.Load() != before || f.removes.Load() != 0 {
		t.Fatal("invalid or cancelled request performed I/O")
	}
}
func TestSeedingRemovalConcurrentSingleWrite(t *testing.T) {
	f := newSeedingRemovalFixture(t, "success", false)
	cap := f.cap(t, f.freeze(t))
	var ack atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			out, err := cap.RemoveRetainingFiles(t.Context())
			if err == nil && out == ports.SeedingRemovalAcknowledged {
				ack.Add(1)
			} else if !errors.Is(err, ports.ErrRejected) {
				t.Error("unexpected competing result", out, err)
			}
		})
	}
	wg.Wait()
	if ack.Load() != 1 || f.removes.Load() != 1 {
		t.Fatal("more than one mutation", ack.Load(), f.removes.Load())
	}
}
func TestSeedingRemovalCancellationAfterDispatch(t *testing.T) {
	f := newSeedingRemovalFixture(t, "blocked", false)
	cap := f.cap(t, f.freeze(t))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := cap.RemoveRetainingFiles(ctx)
		if out != ports.SeedingRemovalUnknown || err == nil {
			t.Error("dispatched cancellation did not retain unknown", out, err)
		}
	}()
	select {
	case <-f.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("native mutation never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("native cancellation did not return")
	}
	if _, err := cap.RemoveRetainingFiles(t.Context()); !errors.Is(err, ports.ErrRejected) || f.removes.Load() != 1 {
		t.Fatal("cancelled mutation replayed")
	}
}
