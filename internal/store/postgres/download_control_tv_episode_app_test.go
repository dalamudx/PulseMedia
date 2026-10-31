package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTVEpisodeUpgradeControlCookieAppPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	var paused, loseACK atomic.Bool
	var sends, reads atomic.Int32
	var hash, save, tag string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			if c, e := r.Cookie("SID"); e != nil || c.Value != "mp27-native" {
				t.Error("missing encrypted native login")
				w.WriteHeader(401)
				return
			}
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("username") != "fixture" || r.Form.Get("password") != "mp27-native-password" {
				t.Error("original credential binding failed")
				w.WriteHeader(401)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "mp27-native", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			_, _ = io.WriteString(w, "5.1.2")
		case "/api/v2/torrents/info":
			reads.Add(1)
			if r.URL.Query().Get("hashes") != hash {
				t.Error("HTTP control changed hash")
			}
			state := "downloading"
			if paused.Load() {
				state = "stoppedDL"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "save_path": save, "tags": tag, "state": state, "progress": 0.5, "size": 1234, "downloaded": 617}})
		case "/api/v2/torrents/stop", "/api/v2/torrents/start":
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("hashes") != hash {
				t.Error("HTTP command changed original identity")
			}
			sends.Add(1)
			paused.Store(strings.HasSuffix(r.URL.Path, "/stop"))
			if loseACK.Swap(false) {
				c, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
				} else {
					_ = c.Close()
				}
			}
		default:
			t.Error("unexpected control request", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer peer.Close()
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	cipher, e := codec.EncryptSecret([]byte("mp27-native-password"))
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE download_client SET url=$2,password_encrypted=$3 WHERE id=$1`, f.client, peer.URL, cipher)
	password := "MP27-owned-admin-password-42!"
	passwordHash, e := crypto.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE app_user SET password_hash=$2 WHERE id=$1`, f.actor.ID, passwordHash)
	p := f.prepared(t)
	f.command(t, "send", p.OperationID, p.Version, "send")
	hash = f.native.proof.InfoHash()
	save = filepath.Join(f.rootPath, ".pulsemedia-upgrade", p.OperationID.String())
	tag = "pulsemedia:op:" + p.OperationID.String()
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	base, stop := startTVEpisodeUpgradeHTTPApp(t, ctx, f.pool.Config().ConnConfig.ConnString(), filepath.Join(t.TempDir(), "app-data"))
	defer stop()
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	call := func(method, path, key string, body any, origin bool, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			input = bytes.NewReader(raw)
		}
		request, e := http.NewRequestWithContext(ctx, method, base+path, input)
		if e != nil {
			t.Fatal(e)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if origin {
			request.Header.Set("Origin", base)
		}
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, e := client.Do(request)
		if e != nil {
			t.Fatal(method, path, e)
		}
		defer func() { _ = response.Body.Close() }()
		raw, e := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s got=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
		}
		if strings.Contains(path, "download-control") && response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("control response cacheable")
		}
		return raw
	}
	path := "/api/v1/operations/" + p.OperationID.String() + "/download-control"
	body := map[string]any{"action": "pause", "confirmed": true, "affect_shared": false}
	call("POST", path, "no-session", body, true, 401)
	call("POST", "/api/v1/auth/login", "", map[string]string{"username": "creation-owner", "password": password}, true, 200)
	call("POST", path, "no-origin", body, false, 403)
	decode := func(raw []byte, state string) {
		t.Helper()
		var got ports.DownloadControlReceipt
		if e := json.Unmarshal(raw, &got); e != nil || got.OperationID != p.OperationID || got.Action != "pause" || got.State != state {
			t.Fatalf("bad original control receipt: %+v %v", got, e)
		}
	}
	loseACK.Store(true)
	decode(call("POST", path, "cookie-pause", body, true, 200), "unknown")
	nativeReads := reads.Load()
	for range 2 {
		decode(call("GET", path, "cookie-pause", nil, false, 200), "unknown")
		decode(call("POST", path, "cookie-pause", body, true, 200), "unknown")
	}
	if sends.Load() != 1 || reads.Load() != nativeReads {
		t.Fatal("Cookie receipt/replay retried native call")
	}
	call("POST", path, "replacement", map[string]any{"action": "resume", "confirmed": true}, true, 409)
	decode(call("POST", path+"/observations", "cookie-pause", nil, true, 200), "confirmed")
	if sends.Load() != 1 {
		t.Fatal("explicit Cookie observation resent native pause")
	}
	execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
	call("GET", path, "cookie-pause", nil, false, 401)
	if sends.Load() != 1 {
		t.Fatal("revocation sent native command")
	}
}
