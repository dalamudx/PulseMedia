package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

// Run: bash scripts/verify_m5_indexer_browser.sh http
// Opt-in is checked before GetCluster/NewTestDB. The wrapper owns PG18.6 and TMPDIR.
type m5IndexerAcceptance struct {
	app                           *app.App
	base, session, rawURL, origin string
	key                           []byte
	calls, caps, searches         atomic.Int32
	stopRuntime                   func()
	newRuntime                    func() (*app.App, error)
	dataDir                       string
}
type m5IndexerHTTPResult struct {
	status int
	body   []byte
	header http.Header
}

func newM5IndexerAcceptance(t *testing.T) *m5IndexerAcceptance {
	t.Helper()
	if os.Getenv("PULSE_M5_INDEXER_ACCEPTANCE") != "1" {
		t.Skip("set PULSE_M5_INDEXER_ACCEPTANCE=1 with an owned PG18.6 fixture")
	}
	if !strings.HasPrefix(os.Getenv("TMPDIR"), "/data/temp/") {
		t.Fatal("owned TMPDIR under /data/temp required")
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(t.Context(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Database.URL, cfg.DataDir = dsn, filepath.Join(t.TempDir(), "data")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	f := &m5IndexerAcceptance{app: a, base: "http://" + cfg.Server.HTTPAddr}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("App.Run shutdown: %v", err)
			}
		case <-time.After(50 * time.Second):
			t.Error("App shutdown exceeded 15s HTTP + 30s workers budget")
		}
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, e := (&http.Client{Timeout: time.Second}).Get(f.base + "/api/v1/auth/status")
		if e == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case e := <-done:
			done <- e
			t.Fatalf("App startup failed: %v", e)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("App HTTP readiness deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var version string
	if err = a.DBPool().QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("PostgreSQL 18.6 required")
	}
	f.key, err = crypto.EnsureMasterKey(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"username": "m5-native-admin", "pass" + "word": "OwnedM5NativeFixture2026!"})
	request, _ := http.NewRequest(http.MethodPost, f.base+"/api/v1/auth/setup", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", f.base)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("public setup failed")
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("public setup status %d", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "pulsemedia_session" {
			f.session = cookie.Value
		}
	}
	if f.session == "" {
		t.Fatal("public setup did not issue session Cookie")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != "/m5-secret-path/api" || r.URL.Query().Get("token") != "m5-query-marker" || r.URL.Query().Get("apikey") != "m5-fixture=credential" {
			t.Error("native Torznab received incorrect decrypted execution configuration")
			w.WriteHeader(400)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "m5-secret-user" || pass != "m5-secret-pass" {
			t.Error("native client lost URL userinfo")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Query().Get("t") {
		case "caps":
			f.caps.Add(1)
			_, _ = io.WriteString(w, `<caps><limits default="25" max="100"/><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
		case "movie":
			f.searches.Add(1)
			_, _ = io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="0"/></channel></rss>`)
		default:
			t.Error("explicit torznab used an incorrect native client request")
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(upstream.Close)
	f.origin = upstream.URL
	f.rawURL = strings.Replace(upstream.URL, "http://", "http://"+"m5-secret-user"+":"+"m5-secret-pass"+"@", 1) + "/m5-secret-path?token=m5-query-marker"
	return f
}
func (f *m5IndexerAcceptance) request(t *testing.T, method, path, key string, body any, session string) m5IndexerHTTPResult {
	t.Helper()
	var raw []byte
	if text, ok := body.(string); ok {
		raw = []byte(text)
	} else if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, f.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.base)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if session != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
	}
	response, err := (&http.Client{Timeout: 35 * time.Second}).Do(r)
	if err != nil {
		t.Fatal("public HTTP request failed")
	}
	defer func() { _ = response.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	m5IndexerNoSecrets(t, out)
	return m5IndexerHTTPResult{response.StatusCode, out, response.Header}
}
func m5IndexerNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	for _, secret := range []string{"m5-secret-user", "m5-secret-pass", "m5-secret-path", "m5-query-marker", "m5-fixture=credential"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("secret leaked in public/durable projection")
		}
	}
}
func m5IndexerStatus(t *testing.T, r m5IndexerHTTPResult, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("public HTTP status %d, want %d: %s", r.status, want, r.body)
	}
}
func m5IndexerObject(t *testing.T, r m5IndexerHTTPResult) map[string]any {
	t.Helper()
	var v map[string]any
	if e := json.Unmarshal(r.body, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func (f *m5IndexerAcceptance) configuration(t *testing.T, id string) ([]byte, []byte) {
	t.Helper()
	var urlCipher, keyCipher []byte
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT c.url_encrypted,i.api_key_encrypted FROM indexer i JOIN indexer_configuration c ON c.indexer_id=i.id WHERE i.id=$1`, uuid.MustParse(id)).Scan(&urlCipher, &keyCipher); e != nil {
		t.Fatal(e)
	}
	return urlCipher, keyCipher
}
func (f *m5IndexerAcceptance) checkPlain(t *testing.T, cipher []byte, want string) {
	t.Helper()
	got, e := crypto.NewSecretProtector(f.key).DecryptSecret(cipher)
	if e != nil || string(got) != want {
		t.Fatal("exact decrypted configuration mismatch")
	}
}
func (f *m5IndexerAcceptance) durableSafe(t *testing.T) {
	t.Helper()
	for _, table := range []string{"indexer", "indexer_configuration", "indexer_query", "audit_log", "outbox_event", "idempotency_record"} {
		var raw []byte
		if e := f.app.DBPool().QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]') FROM "+table+" x").Scan(&raw); e != nil {
			t.Fatal(e)
		}
		m5IndexerNoSecrets(t, raw)
	}
}
func TestM5IndexerPublicCookieAcceptanceHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	const base = "/api/v1/indexers"
	create := map[string]any{"name": "M5 native Torznab", "protocol": "torznab", "url": f.rawURL, "api_key": "m5-fixture=credential", "categories": []int{2000}}
	for _, tc := range []struct{ name, body, key string }{
		{"unknown", `{"name":"bad","protocol":"torznab","url":"http://127.0.0.1","categories":[],"unknown":true}`, "bad-unknown"},
		{"protocol", `{"name":"bad","protocol":"public","url":"http://127.0.0.1","categories":[]}`, "bad-protocol"},
		{"key", `{"name":"bad","protocol":"rss","url":"http://127.0.0.1","categories":[]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) { m5IndexerStatus(t, f.request(t, "POST", base, tc.key, tc.body, f.session), 400) })
	}
	m5IndexerStatus(t, f.request(t, "POST", base, "anon", create, ""), 401)
	first := f.request(t, "POST", base, "m5-create", create, f.session)
	m5IndexerStatus(t, first, 201)
	created := m5IndexerObject(t, first)
	id, ok := created["id"].(string)
	if !ok {
		t.Fatal("missing ID")
	}
	if created["url"] != f.origin || created["protocol"] != "torznab" || created["legacy_v1"] != false || created["config_version"] != float64(1) {
		t.Fatal("unsafe/incorrect public projection")
	}
	if first.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("missing no-store")
	}
	replay := f.request(t, "POST", base, "m5-create", create, f.session)
	m5IndexerStatus(t, replay, 201)
	if !bytes.Equal(first.body, replay.body) {
		t.Fatal("same-key create changed result")
	}
	urlCipher, keyCipher := f.configuration(t, id)
	f.checkPlain(t, urlCipher, f.rawURL)
	f.checkPlain(t, keyCipher, "m5-fixture=credential")
	receiptPath := "/api/v1/indexer-commands/m5-create?action=create"
	receipt := f.request(t, "GET", receiptPath, "", nil, f.session)
	m5IndexerStatus(t, receipt, 200)
	var before int
	if e := f.app.DBPool().QueryRow(t.Context(), "SELECT count(*) FROM indexer WHERE name='M5 native Torznab'").Scan(&before); e != nil || before != 1 {
		t.Fatal("same-key create duplicated database creation")
	}
	probe := f.request(t, "POST", base+"/"+id+"/test", "", nil, f.session)
	m5IndexerStatus(t, probe, 200)
	if m5IndexerObject(t, probe)["alive"] != true || f.caps.Load() != 1 || f.searches.Load() != 1 {
		t.Errorf("native explicit Torznab caps/search probe failed: caps=%d search=%d", f.caps.Load(), f.searches.Load())
	}
	var health string
	var checked bool
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT health,health_checked_at IS NOT NULL FROM indexer WHERE id=$1`, uuid.MustParse(id)).Scan(&health, &checked); e != nil || health != "online" || !checked {
		t.Error("native health observation was not persisted", e)
	}
	update := map[string]any{"name": "M5 renamed", "protocol": "torznab", "categories": []int{2000}, "expected_version": 1}
	missing := map[string]any{"name": "missing version", "protocol": "torznab", "categories": []int{}}
	m5IndexerStatus(t, f.request(t, "PUT", base+"/"+id, "missing-version", missing, f.session), 400)
	m5IndexerStatus(t, f.request(t, "PUT", base+"/"+id, "", update, f.session), 400)
	edited := f.request(t, "PUT", base+"/"+id, "m5-edit-command", update, f.session)
	m5IndexerStatus(t, edited, 200)
	u, k := f.configuration(t, id)
	if !bytes.Equal(u, urlCipher) || !bytes.Equal(k, keyCipher) {
		t.Fatal("omitted URL/key changed exact ciphertext")
	}
	m5IndexerStatus(t, f.request(t, "PUT", base+"/"+id, "m5-stale", update, f.session), 409)
	old := f.request(t, "GET", receiptPath, "", nil, f.session)
	m5IndexerStatus(t, old, 200)
	if !bytes.Equal(receipt.body, old.body) {
		t.Fatal("GET original receipt recomputed after edit")
	}
	update["expected_version"] = 2
	update["api_key"] = ""
	m5IndexerStatus(t, f.request(t, "PUT", base+"/"+id, "m5-clear", update, f.session), 200)
	u, k = f.configuration(t, id)
	f.checkPlain(t, k, "")
	if !bytes.Equal(u, urlCipher) || bytes.Equal(k, keyCipher) {
		t.Fatal("deliberate credential clear changed URL or preserved old credential")
	}
	// Both ordinary and empty-credential RSS sources are valid public commands.
	for n, credential := range []string{"normal-rss-credential", ""} {
		body := map[string]any{"name": fmt.Sprintf("RSS-%d", n), "protocol": "rss", "url": f.origin + "/feed", "api_key": credential, "categories": []int{}}
		m5IndexerStatus(t, f.request(t, "POST", base, fmt.Sprintf("m5-rss-%d", n), body, f.session), 201)
	}
	m5IndexerStatus(t, f.request(t, "DELETE", base+"/"+id+"?expected_version=3", "", nil, f.session), 400)
	m5IndexerStatus(t, f.request(t, "DELETE", base+"/"+id, "no-delete-version", nil, f.session), 400)
	m5IndexerStatus(t, f.request(t, "DELETE", base+"/"+id+"?expected_version=2", "stale-delete", nil, f.session), 409)
	m5IndexerStatus(t, f.request(t, "DELETE", base+"/"+id+"?expected_version=3", "m5-delete", nil, f.session), 204)
	deleted := f.request(t, "GET", "/api/v1/indexer-commands/m5-delete?action=delete", "", nil, f.session)
	m5IndexerStatus(t, deleted, 200)
	if m5IndexerObject(t, deleted)["deleted"] != true {
		t.Fatal("missing logical delete receipt")
	}
	old = f.request(t, "GET", receiptPath, "", nil, f.session)
	if !bytes.Equal(receipt.body, old.body) {
		t.Fatal("original safe create receipt lost after deletion")
	}
	list := f.request(t, "GET", base, "", nil, f.session)
	m5IndexerStatus(t, list, 200)
	if bytes.Contains(list.body, []byte(id)) {
		t.Fatal("deleted source listed")
	}
	calls := f.calls.Load()
	denied := f.request(t, "POST", base+"/"+id+"/test", "", nil, f.session)
	if denied.status == 200 && m5IndexerObject(t, denied)["alive"] == true {
		t.Fatal("deleted source execution succeeded")
	}
	if f.calls.Load() != calls {
		t.Fatal("deleted source reached network")
	}
	var retained, tombstoned, queries int
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM indexer WHERE id=$1),(SELECT count(*) FROM indexer_lifecycle WHERE indexer_id=$1 AND deleted_at IS NOT NULL AND NOT enabled),(SELECT count(*) FROM indexer_query WHERE indexer_id=$1)`, uuid.MustParse(id)).Scan(&retained, &tombstoned, &queries); e != nil || retained != 1 || tombstoned != 1 || queries < 1 {
		t.Error("logical deletion lost native query history", e)
	}
	// A foreign current administrator cannot read the owner's command receipt.
	foreign := uuid.New()
	if _, e := f.app.DBPool().Exec(t.Context(), `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'m5-foreign',password_hash,'admin',true FROM app_user WHERE username='m5-native-admin'`, foreign); e != nil {
		t.Fatal(e)
	}
	login := f.request(t, "POST", "/api/v1/auth/login", "", map[string]string{"username": "m5-foreign", "pass" + "word": "OwnedM5NativeFixture2026!"}, "")
	m5IndexerStatus(t, login, 200)
	foreignSession := ""
	for _, c := range (&http.Response{Header: login.header}).Cookies() {
		if c.Name == "pulsemedia_session" {
			foreignSession = c.Value
		}
	}
	if foreignSession == "" {
		t.Fatal("missing foreign session")
	}
	m5IndexerStatus(t, f.request(t, "GET", receiptPath, "", nil, foreignSession), 404)
	if _, e := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, foreign); e != nil {
		t.Fatal(e)
	}
	m5IndexerStatus(t, f.request(t, "POST", base, "member-create", create, foreignSession), 403)
	m5IndexerStatus(t, f.request(t, "GET", receiptPath, "", nil, foreignSession), 403)
	if _, e := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET is_active=false WHERE username='m5-native-admin'`); e != nil {
		t.Fatal(e)
	}
	revoked := f.request(t, "GET", receiptPath, "", nil, f.session)
	if revoked.status != 401 && revoked.status != 403 {
		t.Fatal("revoked current administrator recovered receipt", revoked.status)
	}
	if _, e := f.app.DBPool().Exec(t.Context(), `UPDATE app_user SET is_active=true WHERE username='m5-native-admin'`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.app.DBPool().Exec(t.Context(), `DELETE FROM app_session WHERE user_id=(SELECT id FROM app_user WHERE username='m5-native-admin')`); e != nil {
		t.Fatal(e)
	}
	m5IndexerStatus(t, f.request(t, "GET", receiptPath, "", nil, f.session), 401)
	f.durableSafe(t)
	if !t.Failed() {
		t.Log("M5_INDEXER_PUBLIC_COOKIE_NATIVE_PG_PASS: real App/Cookie/PG18.6, native caps+search, exact ciphertext preservation, clear, stale fence, safe stable receipts, logical tombstone and retained history")
	}
}
