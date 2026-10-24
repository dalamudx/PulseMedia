package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
)

type nativeObserverFixture struct {
	*observationFixture
	manager                *download.Manager
	key                    []byte
	server                 *httptest.Server
	lookups, adds, invalid atomic.Int32
	body                   func() string
	hook                   func()
	status                 int
}

func newNativeObserverFixture(t *testing.T) *nativeObserverFixture {
	t.Helper()
	n := &nativeObserverFixture{key: bytes.Repeat([]byte{42}, 32), status: 200}
	n.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Method != "POST" || r.Form.Get("username") != "native-observer" || r.Form.Get("password") != "native-password" {
				n.invalid.Add(1)
			}
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			n.lookups.Add(1)
			if r.Method != "GET" || len(r.URL.Query()) != 1 || r.URL.Query().Get("tag") != "pulsemedia:op:"+n.request.OperationID.String() {
				n.invalid.Add(1)
			}
			if n.hook != nil {
				n.hook()
			}
			w.WriteHeader(n.status)
			_, _ = io.WriteString(w, n.body())
		case "/api/v2/torrents/add":
			n.adds.Add(1)
			w.WriteHeader(500)
		default:
			n.invalid.Add(1)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(n.server.Close)
	n.observationFixture = newObservationFixture(t, "unknown", func(f *completeTVFixture) {
		encrypted, e := crypto.Encrypt(n.key, []byte("native-password"))
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE download_client SET url=$1,username='native-observer',password_encrypted=$2`, n.server.URL, encrypted)
	})
	n.body = func() string { b, _ := json.Marshal([]any{n.row()}); return string(b) }
	n.manager = n.newManager()
	// Drain the protocol handler before the App cleanup closes its database pool.
	t.Cleanup(n.server.Close)
	return n
}
func (n *nativeObserverFixture) row() map[string]any {
	s := n.loadSnapshot()
	return map[string]any{"hash": s.InfoHash, "save_path": s.SavePath, "size": s.TotalBytes, "downloaded": 0, "progress": 0, "state": "downloading", "tags": "other, pulsemedia, " + s.OperationTag + ", trailing"}
}
func (n *nativeObserverFixture) loadSnapshot() ports.CompleteTVObservationSnapshot {
	// Used before I/O by test setup only, never while network or commit is blocked.
	var s ports.CompleteTVObservationSnapshot
	s.Request = n.request
	s.OperationTag = "pulsemedia:op:" + n.request.OperationID.String()
	s.InfoHash = "d497dcf93e2dbd4b917ba68788ba7d42590cf594"
	s.TotalBytes = 1024
	// The original task path is deliberately used even if current roots change.
	_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT save_path FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&s.SavePath)
	return s
}
func (n *nativeObserverFixture) newManager() *download.Manager {
	m := download.NewManager(n.f.a.DBPool(), idgen.New(), n.key)
	m.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		if kind != "qbittorrent" || url != n.server.URL || user != "native-observer" || password != "native-password" {
			n.invalid.Add(1)
		}
		return qbittorrent.NewClient(url, user, password, nil)
	})
	m.SetCompleteTVServices(n.store, n.store, torznab.CompleteTVFetcher{}, torrent.Inspector{})
	return m
}
func (n *nativeObserverFixture) observe(ctx context.Context) (*ports.CompleteTVObservationReceipt, error) {
	return n.manager.ObserveCompleteTV(ctx, n.observer, n.request)
}
func (n *nativeObserverFixture) wire(t *testing.T, count int32) {
	t.Helper()
	if n.adds.Load() != 0 || n.invalid.Load() != 0 || n.lookups.Load() != count {
		t.Fatal("native wire", n.adds.Load(), n.invalid.Load(), n.lookups.Load())
	}
}

func TestCompleteTVNativeObserverNativePGRecovery(t *testing.T) {
	for _, state := range []string{"unknown", "submitting"} {
		t.Run(state, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			n.exec(t, `UPDATE download_task SET state=$1`, state)
			n.exec(t, `UPDATE app_user SET is_active=false`)
			// Cached clients must never be used by this physical observation.
			stale, _ := qbittorrent.NewClient("http://127.0.0.1:1", "old", "old", nil)
			s := n.load(t)
			n.manager.RegisterClient(s.ClientID, stale)
			n.hook = func() {
				tx, e := n.f.a.DBPool().Begin(context.Background())
				if e != nil {
					n.invalid.Add(1)
					return
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				if _, e = tx.Exec(context.Background(), `SELECT id FROM download_task FOR UPDATE NOWAIT`); e != nil {
					n.invalid.Add(1)
				}
			}
			r, e := n.observe(context.Background())
			if e != nil || r == nil || r.State != "tracking" {
				t.Fatal(r, e)
			}
			n.assertRecovery(t, "tracking", false)
			n.wire(t, 1)
			before := n.snapshot(t)
			r, e = n.observe(context.Background())
			if r != nil || !errors.Is(e, ports.ErrCompleteTVObservationStale) || n.snapshot(t) != before {
				t.Fatal("tracking must be deferred", r, e)
			}
			n.wire(t, 1)
		})
	}
}
func TestCompleteTVNativeObserverNativePGReview(t *testing.T) {
	for _, kind := range []string{"missing", "ambiguous", "duplicate", "hash", "path", "tag", "tag-prefix", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			row := n.row()
			rows := []any{row}
			switch kind {
			case "missing":
				rows = []any{}
			case "ambiguous":
				other := n.row()
				other["hash"] = strings.Repeat("f", 40)
				rows = append(rows, other)
			case "duplicate":
				rows = append(rows, row)
			case "hash":
				row["hash"] = strings.Repeat("f", 40)
			case "path":
				row["save_path"] = row["save_path"].(string) + "/other"
			case "tag":
				row["tags"] = "other"
			case "tag-prefix":
				row["tags"] = "pulsemedia:op:" + n.request.OperationID.String() + "-other"
			case "bytes":
				row["size"] = 1025
			}
			b, _ := json.Marshal(rows)
			n.body = func() string { return string(b) }
			r, e := n.observe(context.Background())
			if e != nil || r == nil || r.State != "unknown" || !r.ReviewRequired {
				t.Fatal(r, e)
			}
			n.assertRecovery(t, "unknown", true)
			n.wire(t, 1)
		})
	}
}
func TestCompleteTVNativeObserverNativePGUnavailable(t *testing.T) {
	for _, kind := range []string{"null", "truncated", "trailing", "missing-tags", "null-tags", "invalid-progress", "missing-size", "nil-row", "offline", "oversize", "transport"} {
		t.Run(kind, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			row := n.row()
			switch kind {
			case "missing-tags":
				delete(row, "tags")
			case "null-tags":
				row["tags"] = nil
			case "invalid-progress":
				row["progress"] = 2
			case "missing-size":
				delete(row, "size")
			}
			b, _ := json.Marshal([]any{row})
			body := string(b)
			switch kind {
			case "null":
				body = "null"
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body += " []"
			case "nil-row":
				body = "[null]"
			case "offline":
				n.status = 503
			case "oversize":
				body = strings.Repeat(" ", 11<<20) + "[]"
			case "transport":
				n.server.Close()
			}
			n.body = func() string { return body }
			before := n.snapshot(t)
			r, e := n.observe(context.Background())
			if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
				t.Fatal("unavailable wrote", r, e)
			}
			count := int32(1)
			if kind == "transport" {
				count = 0
			}
			n.wire(t, count)
		})
	}
}
func TestCompleteTVNativeObserverNativePGStale(t *testing.T) {
	for _, kind := range []string{"rotation-before", "rotation-during", "lease-during", "token-during", "lost-session"} {
		t.Run(kind, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			mutate := func() {
				var e error
				switch kind {
				case "rotation-before", "rotation-during":
					_, e = n.f.a.DBPool().Exec(context.Background(), `UPDATE download_client SET username='rotated'`)
				case "lease-during":
					_, e = n.f.a.DBPool().Exec(context.Background(), `UPDATE job SET lease_until=clock_timestamp() WHERE id=$1`, n.request.Lease.ID)
				case "token-during":
					_, e = n.f.a.DBPool().Exec(context.Background(), `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`, n.request.Lease.ID)
				case "lost-session":
					_, e = n.f.a.DBPool().Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid()`)
				}
				if e != nil {
					n.invalid.Add(1)
				}
			}
			if kind == "rotation-before" {
				mutate()
			} else {
				n.hook = mutate
			}
			r, e := n.observe(context.Background())
			want := ports.ErrCompleteTVObservationStale
			if kind == "lost-session" {
				want = ports.ErrUnavailable
			}
			if r != nil || !errors.Is(e, want) {
				t.Fatal(r, e)
			}
			var audits int
			if e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='complete_tv.recovery_observed'`).Scan(&audits); e != nil || audits != 0 {
				t.Fatal("stale observation wrote", e, audits)
			}
			var state string
			_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task`).Scan(&state)
			if state != "unknown" {
				t.Fatal(state)
			}
			count := int32(1)
			if kind == "rotation-before" {
				count = 0
			}
			n.wire(t, count)
		})
	}
}

type observationBarrier struct {
	ports.CompleteTVObservationRepository
	entered chan struct{}
	release chan struct{}
}

func (b *observationBarrier) CommitCompleteTVObservation(ctx context.Context, s ports.CompleteTVObservationSnapshot, r ports.CompleteTVRemoteObservation) (*ports.CompleteTVObservationReceipt, error) {
	close(b.entered)
	<-b.release
	return b.CompleteTVObservationRepository.CommitCompleteTVObservation(ctx, s, r)
}
func TestCompleteTVNativeObserverNativePGPhysicalLockThroughCommit(t *testing.T) {
	n := newNativeObserverFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	barrier := &observationBarrier{n.observer, entered, release}
	done := make(chan error, 1)
	go func() { _, e := n.manager.ObserveCompleteTV(context.Background(), barrier, n.request); done <- e }()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("observer not joined")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no commit")
	}
	other := n.newManager()
	if r, e := other.ObserveCompleteTV(context.Background(), n.observer, n.request); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("observer overlap", r, e)
	}
	if r, e := other.SendCompleteTV(context.Background(), n.c.Lease, n.request.OperationID); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("sender overlap", r, e)
	}
	n.wire(t, 1)
}

type observerBlockingClient struct {
	ports.DownloadClient
	entered, testEntered chan struct{}
	release              chan struct{}
	items                []*ports.TorrentInfo
}

func (c *observerBlockingClient) CompleteTVObserver() ports.DownloadClient { return c }
func (c *observerBlockingClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	close(c.entered)
	<-c.release
	return c.items, nil
}
func (c *observerBlockingClient) Test(context.Context) error {
	c.testEntered <- struct{}{}
	<-c.release
	return nil
}
func TestCompleteTVNativeObserverNativePGIgnoredCancelBudget(t *testing.T) {
	n := newNativeObserverFixture(t)
	s := n.load(t)
	c := &observerBlockingClient{entered: make(chan struct{}), testEntered: make(chan struct{}, 2), release: make(chan struct{}), items: []*ports.TorrentInfo{{InfoHash: s.InfoHash, SavePath: s.SavePath, TotalBytes: s.TotalBytes, Tags: s.OperationTag}}}
	var once sync.Once
	unblock := func() { once.Do(func() { close(c.release) }) }
	n.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return c, nil })
	n.manager.RegisterClient(s.ClientID, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, e := n.observe(ctx)
		if e == nil && (r == nil || r.State != "tracking") {
			e = errors.New("no tracking")
		}
		done <- e
	}()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("observer not joined")
		}
	}()
	select {
	case <-c.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no native lookup")
	}
	testDone := make(chan error, 1)
	go func() { _, _, e := n.manager.TestClient(context.Background(), s.ClientID); testDone <- e }()
	defer func() {
		unblock()
		select {
		case e := <-testDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("test not joined")
		}
	}()
	select {
	case <-c.testEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("no second slot")
	}
	cancel()
	short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, _, e := n.manager.TestClient(short, s.ClientID); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("budget released before network return", e)
	}
	if r, e := n.newManager().ObserveCompleteTV(context.Background(), n.observer, n.request); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("canceled physical overlap", r, e)
	}
	select {
	case <-done:
		t.Fatal("returned before physical I/O")
	default:
	}
	n.wire(t, 0)
}
func TestCompleteTVNativeObserverNativePGRejectGenericAndNil(t *testing.T) {
	for _, kind := range []string{"generic", "nil-list", "nil-row"} {
		t.Run(kind, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			before := n.snapshot(t)
			switch kind {
			case "generic":
				n.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return &sharedCompleteTVBudgetClient{}, nil })
			default:
				c := &observerBlockingClient{entered: make(chan struct{}), release: make(chan struct{})}
				close(c.release)
				if kind == "nil-row" {
					c.items = []*ports.TorrentInfo{nil}
				}
				n.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return c, nil })
			}
			r, e := n.observe(context.Background())
			if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
				t.Fatal(r, e)
			}
			n.wire(t, 0)
		})
	}
}
