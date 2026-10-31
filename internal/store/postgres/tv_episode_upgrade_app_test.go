package postgres_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Only protocol peers are simulated: App constructs its production repositories,
// native gateway, qB client, metainfo inspector, filesystem and event publisher.
func TestTVEpisodeUpgradeAppHTTPProductionRecoveryPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	master := bytes.Repeat([]byte{27}, 32)
	password := "MP22-owned-admin-password-42!"
	hash, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, "UPDATE app_user SET password_hash=$2 WHERE id=$1", f.actor.ID, hash)

	name := "Trusted.Native.Series.2024.S02E03.2160p.WEB-DL.H264.mkv"
	raw := []byte("d4:infod6:lengthi1234e4:name" + strconv.Itoa(len(name)) + ":" + name + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("0", 20) + "ee")
	proof := automaticTVSubmissionMetainfo(t, name)
	payload := bytes.Repeat([]byte("n"), 1234)
	var adds, logins, fetches, lists, manifests atomic.Int32
	var mu sync.Mutex
	save, tags := "", ""
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(message string) {
			t.Error(message)
			http.Error(w, "invalid owned native request", http.StatusBadRequest)
		}
		if r.URL.Path != "/api/v2/auth/login" {
			cookie, e := r.Cookie("SID")
			if e != nil || cookie.Value != "mp22-owned-qb-session" {
				fail("qB request has no native login cookie")
				return
			}
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("username") != "fixture" || r.Form.Get("password") != "mp22-qb-password" {
				fail("qB login did not use encrypted endpoint credentials")
				return
			}
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "mp22-owned-qb-session", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			if r.Method != http.MethodPost {
				fail("qB Add method")
				return
			}
			reader, e := r.MultipartReader()
			if e != nil {
				fail("qB Add must upload multipart metainfo")
				return
			}
			fields := map[string]string{}
			uploads := 0
			for {
				part, e := reader.NextPart()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					fail("qB multipart read")
					return
				}
				data, e := io.ReadAll(io.LimitReader(part, 1<<20))
				if e != nil {
					fail("qB multipart body")
					return
				}
				if part.FormName() == "torrents" {
					uploads++
					if part.FileName() == "" || !bytes.Equal(data, raw) {
						fail("qB Add changed original metainfo bytes")
						return
					}
				} else {
					fields[part.FormName()] = string(data)
				}
			}
			if uploads != 1 || fields["urls"] != "" || !strings.HasPrefix(fields["savepath"], filepath.Join(f.rootPath, ".pulsemedia-upgrade")+string(os.PathSeparator)) || !strings.Contains(fields["tags"], "pulsemedia:op:") {
				fail("qB Add missing frozen native authority")
				return
			}
			if e := os.MkdirAll(fields["savepath"], 0755); e != nil {
				fail("create native download directory")
				return
			}
			if e := os.WriteFile(filepath.Join(fields["savepath"], name), payload, 0600); e != nil {
				fail("materialize native downloaded file")
				return
			}
			mu.Lock()
			save, tags = fields["savepath"], fields["tags"]
			mu.Unlock()
			adds.Add(1)
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			if r.Method != http.MethodGet {
				fail("qB list method")
				return
			}
			lists.Add(1)
			mu.Lock()
			s, tag := save, tags
			mu.Unlock()
			if s == "" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": proof.InfoHash(), "name": name, "save_path": s, "content_path": filepath.Join(s, name), "progress": 1, "size": 1234, "downloaded": 1234, "state": "uploading", "tags": tag}})
		case "/api/v2/torrents/files":
			if r.Method != http.MethodGet || r.URL.Query().Get("hash") != proof.InfoHash() {
				fail("qB manifest identity")
				return
			}
			manifests.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"index": 0, "name": name, "size": 1234, "progress": 1}})
		default:
			fail("unexpected qB protocol request: " + r.URL.Path)
		}
	}))
	t.Cleanup(qb.Close)
	torznab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/torrent" || r.URL.Query().Get("apikey") != "mp22-indexer-key" {
			t.Error("unexpected Torznab request or missing decrypted API key")
			http.Error(w, "invalid owned Torznab request", http.StatusBadRequest)
			return
		}
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(torznab.Close)
	encrypt := func(text string) []byte {
		t.Helper()
		data, e := crypto.Encrypt(master, []byte(text))
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	execIntent(t, f.pool, "UPDATE download_client SET url=$2,password_encrypted=$3 WHERE id=$1", f.client, qb.URL, encrypt("mp22-qb-password"))
	execIntent(t, f.pool, "UPDATE indexer SET url=$2,api_key_encrypted=$3,config_version=config_version+1 WHERE id=$1", f.indexer, torznab.URL, encrypt("mp22-indexer-key"))
	execIntent(t, f.pool, "UPDATE indexer_configuration SET url_encrypted=$2,configuration_version=2 WHERE indexer_id=$1", f.indexer, encrypt(torznab.URL+"/api"))
	execIntent(t, f.pool, "UPDATE release_candidate SET link_encrypted=$2,source_config_version=2 WHERE id=$1", f.request.CandidateID, encrypt(torznab.URL+"/torrent?apikey=mp22-indexer-key"))

	execIntent(t, f.pool, "UPDATE indexer_query SET config_version=2 WHERE indexer_id=$1", f.indexer)
	data := filepath.Join(t.TempDir(), "app-data")
	// Reuse the fixture's isolated DB; never pass the parent cluster database to App.
	dsn := f.pool.Config().ConnConfig.ConnString()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	base, stop := startTVEpisodeUpgradeHTTPApp(t, ctx, dsn, data)
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
		req, e := http.NewRequestWithContext(ctx, method, base+path, input)
		if e != nil {
			t.Fatal(e)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin {
			req.Header.Set("Origin", base)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatalf("%s %s: %v", method, path, e)
		}
		defer func() { _ = res.Body.Close() }()
		raw, e := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.StatusCode, want, raw)
		}
		if strings.Contains(path, "tv-episode-upgrade") && want < 300 && res.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("upgrade receipt is cacheable")
		}
		return raw
	}
	decode := func(raw []byte) *ports.TVEpisodeUpgradeResult {
		t.Helper()
		var r ports.TVEpisodeUpgradeResult
		if e := json.Unmarshal(raw, &r); e != nil {
			t.Fatal(e)
		}
		if r.ExecutionAllowed || r.UnitID != f.unit || r.CurrentFileID != f.request.CurrentFileID || r.CandidateID != f.request.CandidateID || len(r.Version) != 64 {
			t.Fatalf("unsafe or unbound HTTP result: %+v", r)
		}
		return &r
	}
	call(http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "creation-owner", "password": password}, true, http.StatusOK)
	subscriptionPath := "/api/v1/subscriptions/" + f.sub.String()
	previewPath := subscriptionPath + "/tv-episode-upgrade-previews"
	commandPath := subscriptionPath + "/tv-episode-upgrade-commands"
	call(http.MethodPost, previewPath, "", f.request, false, http.StatusForbidden)
	preview := decode(call(http.MethodPost, previewPath, "", f.request, true, http.StatusOK))
	if !preview.Improvement || !preview.Comparable || preview.Reason != "" {
		t.Fatalf("preview did not authorize an improvement: %+v", preview)
	}
	receipts := map[string]*ports.TVEpisodeUpgradeResult{}
	reads := func(key string, want *ports.TVEpisodeUpgradeResult) {
		t.Helper()
		a, l, m, fe := adds.Load(), lists.Load(), manifests.Load(), fetches.Load()
		for range 2 {
			got := decode(call(http.MethodGet, commandPath+"/"+key, "", nil, false, http.StatusOK))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("GET recovery changed receipt %s: got=%+v want=%+v", key, got, want)
			}
		}
		if a != adds.Load() || l != lists.Load() || m != manifests.Load() || fe != fetches.Load() {
			t.Fatal("GET recovery invoked a native peer")
		}
	}
	command := func(action string, op uuid.UUID, version, status string) *ports.TVEpisodeUpgradeResult {
		t.Helper()
		body := map[string]any{"unit_id": f.unit, "current_file_id": f.request.CurrentFileID, "candidate_id": f.request.CandidateID, "action": action, "expected_version": version, "confirmed": true}
		if op != uuid.Nil {
			body["operation_id"] = op
		}
		key := "mp22-app-" + action
		got := decode(call(http.MethodPost, commandPath, key, body, true, http.StatusAccepted))
		if got.Status != status || got.OperationID == uuid.Nil || op != uuid.Nil && got.OperationID != op {
			t.Fatalf("%s: %+v", action, got)
		}
		if action == "organization-preview" || action == "retirement-preview" {
			// Preview commands intentionally persist no execution authority or receipt.
			for range 2 {
				call(http.MethodGet, commandPath+"/"+key, "", nil, false, http.StatusNotFound)
			}
		} else {
			receipts[key] = got
			reads(key, got)
			replay := decode(call(http.MethodPost, commandPath, key, body, true, http.StatusAccepted))
			if !reflect.DeepEqual(replay, got) {
				t.Fatalf("POST replay changed %s receipt", action)
			}
		}
		return got
	}
	restart := func() {
		t.Helper()
		stop()
		base, stop = startTVEpisodeUpgradeHTTPApp(t, ctx, dsn, data)
		// Cookie jars key by host, so the original session survives the new listener port.
		call(http.MethodGet, "/api/v1/auth/me", "", nil, false, http.StatusOK)
		for key, r := range receipts {
			reads(key, r)
		}
	}
	r := command("reserve", uuid.Nil, preview.Version, "reserved_not_executable")
	prepared := command("prepare", r.OperationID, r.Version, "prepared_not_sent")
	restart()
	command("send", r.OperationID, prepared.Version, "tracking")
	if adds.Load() != 1 {
		t.Fatal("send/replay did not issue exactly one qB Add", adds.Load())
	}
	restart()
	command("observe", r.OperationID, r.Version, "organization_review_required")
	review := command("organization-preview", r.OperationID, r.Version, "organization_preview_not_executable")
	confirmed := command("organization-confirm", r.OperationID, review.Version, "organization_confirmed_not_executable")
	restart()
	imported := command("organize", r.OperationID, confirmed.Version, "imported_pending_retirement")
	if imported.NewFileID == uuid.Nil {
		t.Fatal("production organize did not import a new file")
	}
	var oldRel, newRel string
	if e := f.pool.QueryRow(ctx, "SELECT rel_path FROM library_file WHERE id=$1", f.request.CurrentFileID).Scan(&oldRel); e != nil {
		t.Fatal(e)
	}
	if e := f.pool.QueryRow(ctx, "SELECT rel_path FROM library_file WHERE id=$1", imported.NewFileID).Scan(&newRel); e != nil {
		t.Fatal(e)
	}
	oldPath, newPath := filepath.Join(f.rootPath, oldRel), filepath.Join(f.rootPath, newRel)
	oldBytes, e := os.ReadFile(oldPath)
	if e != nil || !bytes.Equal(oldBytes, bytes.Repeat([]byte("o"), 100)) {
		t.Fatal("old video changed before explicit retirement", e)
	}
	mu.Lock()
	sourcePath := filepath.Join(save, name)
	mu.Unlock()
	assertNewFile := func() {
		t.Helper()
		source, e := os.Stat(sourcePath)
		if e != nil {
			t.Fatal(e)
		}
		target, e := os.Stat(newPath)
		if e != nil || !os.SameFile(source, target) {
			t.Fatal("production organize did not preserve the native file by hardlink", e)
		}
		content, e := os.ReadFile(newPath)
		if e != nil || !bytes.Equal(content, payload) {
			t.Fatal("imported bytes changed", e)
		}
	}
	assertNewFile()
	if n := intentCount(t, f.pool, "SELECT count(*) FROM unit_fact WHERE source_file_id=$1 AND stage='imported'", imported.NewFileID); n != 1 {
		t.Fatal("new imported fact missing or duplicated", n)
	}
	restart()
	retirement := command("retirement-preview", r.OperationID, imported.Version, "retirement_preview_not_executable")
	approved := command("retirement-confirm", r.OperationID, retirement.Version, "retirement_confirmed_not_executable")
	restart()
	command("retire", r.OperationID, approved.Version, "retired")
	restart()
	stop()
	if _, e := os.Stat(oldPath); !os.IsNotExist(e) {
		t.Fatal("old video was not retired", e)
	}
	recycled, e := os.ReadFile(filepath.Join(f.rootPath, ".pulsemedia_recycle", "tv-upgrade", r.OperationID.String(), filepath.Base(oldRel)))
	if e != nil || !bytes.Equal(recycled, oldBytes) {
		t.Fatal("retirement did not preserve old bytes in recycle", e)
	}
	assertNewFile()
	if adds.Load() != 1 || logins.Load() < 2 || fetches.Load() != 2 || manifests.Load() != 1 || lists.Load() != 2 {
		t.Fatalf("unexpected production protocol counts: add=%d login=%d fetch=%d manifest=%d list=%d", adds.Load(), logins.Load(), fetches.Load(), manifests.Load(), lists.Load())
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM library_file_unit WHERE unit_id=$1 AND retired_at IS NULL", f.unit); n != 1 {
		t.Fatal("retirement did not leave exactly one active mapping", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM movie_upgrade_authority"); n != 0 {
		t.Fatal("TV upgrade created unrelated movie authority", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM download_task"); n != 1 {
		t.Fatal("TV upgrade must retain exactly one dedicated control projection", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM download_task WHERE operation_id=$1 AND state='completed'", r.OperationID); n != 1 {
		t.Fatal("original dedicated control projection was not completed", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_authority"); n != 1 {
		t.Fatal("duplicate upgrade authority", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_permit WHERE operation_id=$1", r.OperationID); n != 4 {
		t.Fatal("physical permits duplicated or missing", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_phase WHERE operation_id=$1", r.OperationID); n != 8 {
		t.Fatal("durable phases duplicated or missing", n)
	}
	t.Log("MP22 production App HTTP: Cookie/Origin CSRF, native Torznab fetch and qB multipart Add/observation, hardlink import, explicit recycle retirement, repeated receipt GET/POST and six restarts passed")
}

func startTVEpisodeUpgradeHTTPApp(t *testing.T, parent context.Context, dsn, data string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Database.URL = dsn
	cfg.DataDir = data
	cfg.SecretKey = hex.EncodeToString(bytes.Repeat([]byte{27}, 32))
	cfg.Server.HTTPAddr = addr
	a, err := app.New(cfg, app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil && !errors.Is(e, context.Canceled) {
					t.Error("App.Run", e)
				}
			case <-time.After(15 * time.Second):
				t.Error("production App did not drain within 15 seconds")
			}
			if e := a.Close(); e != nil {
				t.Error("App.Close", e)
			}
		})
	}
	t.Cleanup(stop)
	probe := &http.Client{Timeout: time.Second}
	defer probe.CloseIdleConnections()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	ready, cancelReady := context.WithTimeout(parent, 12*time.Second)
	defer cancelReady()
	for {
		select {
		case e := <-done:
			done <- e
			t.Fatalf("production App stopped before readiness: %v", e)
		case <-ready.Done():
			t.Fatal("production App readiness", ready.Err())
		case <-ticker.C:
			req, e := http.NewRequestWithContext(ready, http.MethodGet, fmt.Sprintf("http://%s/healthz", addr), nil)
			if e != nil {
				t.Fatal(e)
			}
			res, e := probe.Do(req)
			if e == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return "http://" + addr, stop
				}
			}
		}
	}
}
