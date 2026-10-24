package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestM10ThreeMediaServersNativeRuntimePG(t *testing.T)  { m10MediaServersRuntime(t, false) }
func TestM10EmbyRateLimitIndependentRuntimePG(t *testing.T) { m10MediaServersRuntime(t, true) }
func m10MediaServersRuntime(t *testing.T, limited bool) {
	t.Helper()
	f := organizationSetup(t)
	var mu sync.Mutex
	calls := map[string]int{}
	fail := limited
	bad := make(chan string, 16)
	remotePath := ""
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		kind := parts[0]
		suffix := strings.TrimPrefix(r.URL.Path, "/"+kind)
		valid := r.Header.Get("X-Emby-Token") == "private-server-key"
		if kind == "jellyfin" {
			valid = r.Header.Get("Authorization") == `MediaBrowser Token="private-server-key"`
		}
		if kind == "plex" {
			valid = r.Header.Get("X-Plex-Token") == "private-server-key"
		}
		if !valid || r.URL.Query().Has("api_key") {
			bad <- "wrong native authentication"
			w.WriteHeader(403)
			return
		}
		if suffix == "/System/Info" || suffix == "/identity" {
			_, _ = io.WriteString(w, `{"ServerName":"owned","Version":"4.8.0"}`)
			return
		}
		if suffix == "/library/sections" {
			_, _ = io.WriteString(w, `{"MediaContainer":{"Directory":[{"key":"1","type":"movie","Location":[{"path":"/movies"}]}]}}`)
			return
		}
		if kind == "plex" {
			if suffix != "/library/sections/1/refresh" || r.URL.Query().Get("path") != filepath.Dir(remotePath) {
				bad <- "broad or wrong Plex refresh"
				w.WriteHeader(400)
				return
			}
		} else {
			var p struct {
				Updates []struct{ Path, UpdateType string }
			}
			if r.Method != "POST" || suffix != "/Library/Media/Updated" || json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Updates) != 1 || p.Updates[0].Path != remotePath || p.Updates[0].UpdateType != "Created" {
				bad <- "broad or wrong path refresh"
				w.WriteHeader(400)
				return
			}
		}
		mu.Lock()
		calls[kind]++
		shouldFail := fail && kind == "emby"
		mu.Unlock()
		if shouldFail {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, "private-server-key-body")
			return
		}
		w.WriteHeader(204)
	}))
	defer native.Close()
	ids := map[string]uuid.UUID{}
	for _, kind := range []string{"jellyfin", "emby", "plex"} {
		cipher, e := crypto.Encrypt(outboxTestKey, []byte("private-server-key"))
		if e != nil {
			t.Fatal(e)
		}
		mapping, _ := json.Marshal(map[string]string{f.target: "/movies"})
		id := uuid.New()
		ids[kind] = id
		if _, e = f.q.CreateMediaServer(t.Context(), db.CreateMediaServerParams{ID: id, Name: kind, ServerType: kind, Url: native.URL + "/" + kind, ApiKeyEncrypted: cipher, PathMapping: mapping}); e != nil {
			t.Fatal(e)
		}
		client, e := NativeMediaServerClient(kind, native.URL+"/"+kind, "private-server-key")
		if e != nil {
			t.Fatal(e)
		}
		if e = client.Test(t.Context()); e != nil {
			t.Fatal("native probe", kind, e)
		}
	}
	importForOutbox(t, f)
	var rel string
	if e := f.pool.QueryRow(t.Context(), `SELECT rel_path FROM library_file WHERE root_id=$1 AND retired_at IS NULL`, f.root).Scan(&rel); e != nil {
		t.Fatal(e)
	}
	remotePath = "/movies/" + filepath.ToSlash(rel)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	pool, e := postgres.NewPool(t.Context(), cfg.Database.URL, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := New(cfg, WithDBPool(pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		pool.Close()
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	done := make(chan error, 1)
	joined := false
	go func() { done <- runtime.Run(ctx) }()
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var delivered int
		e := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery WHERE consumer LIKE 'mediaserver.refresh:%' AND state='delivered'`).Scan(&delivered)
		want := 3
		if limited {
			want = 2
		}
		return e == nil && delivered == want
	})
	if limited {
		waitOutboxCondition(t, ctx, func() bool {
			var until time.Time
			var state string
			e := f.pool.QueryRow(ctx, `SELECT not_before,state FROM outbox_delivery WHERE consumer=$1`, "mediaserver.refresh:"+ids["emby"].String()).Scan(&until, &state)
			return e == nil && state == "pending" && until.After(time.Now().Add(100*time.Second))
		})
		mu.Lock()
		if calls["emby"] != 1 {
			t.Error("ignored backoff")
		}
		fail = false
		mu.Unlock()
		if _, e = f.pool.Exec(ctx, `UPDATE outbox_delivery SET not_before=clock_timestamp()-interval '1 second' WHERE consumer=$1`, "mediaserver.refresh:"+ids["emby"].String()); e != nil {
			t.Fatal(e)
		}
		waitOutboxCondition(t, ctx, func() bool {
			var n int
			e := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery WHERE consumer LIKE 'mediaserver.refresh:%' AND state='delivered'`).Scan(&n)
			return e == nil && n == 3
		})
	}
	mu.Lock()
	for _, kind := range []string{"jellyfin", "emby", "plex"} {
		want := 1
		if limited && kind == "emby" {
			want = 2
		}
		if calls[kind] != want {
			t.Error("unexpected refresh count", kind, calls[kind])
		}
	}
	mu.Unlock()
	select {
	case failure := <-bad:
		t.Fatal(failure)
	default:
	}
	var confirmed, leaks int
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM unit_fact WHERE stage='confirmed'`).Scan(&confirmed); e != nil || confirmed != 0 {
		t.Fatal("refresh incorrectly confirmed", confirmed, e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery WHERE error LIKE '%private-%'`).Scan(&leaks); e != nil || leaks != 0 {
		t.Fatal("credential leak", e)
	}
	f.assertImported(t)
	cancel()
	e = <-done
	joined = true
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	t.Logf("M10_MEDIA_SERVERS_NATIVE_APP_PASS protocols=THREE probes=AUTHENTICATED import=REAL_HARDLINK refresh=EXACT_MAPPED_PATH global_scan=ZERO confirmed=ZERO limited=%v RetryAfter=PERSISTED original_event=UNCHANGED", limited)
}
