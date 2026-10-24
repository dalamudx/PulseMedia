package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	authusecase "github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/google/uuid"
)

// This fixture is shared with Main's browser acceptance. It uses real Cookie auth,
// current migrations and native clients; it does not register fake endpoint testers.
// Run through scripts/verify_m5_endpoint_configuration.sh public.
type m5EndpointAcceptance struct {
	app           *app.App
	base, session string
	key           []byte
	manager       *download.Manager
	media         *mediaserver.SyncService
}
type m5EndpointResult struct {
	status int
	body   []byte
	header http.Header
}

func newM5EndpointAcceptance(t *testing.T) *m5EndpointAcceptance {
	t.Helper()
	if os.Getenv("PULSE_M5_ENDPOINT_CONFIGURATION_ACCEPTANCE") != "1" {
		t.Skip("owned endpoint acceptance opt-in required before PG")
	}
	tmp := os.Getenv("TMPDIR")
	if !strings.HasPrefix(tmp, "/data/temp/") || os.Getenv("TMP") != tmp || os.Getenv("TEMP") != tmp {
		t.Fatal("owned TMPDIR/TMP/TEMP under /data/temp required")
	}
	a, auth, _ := setupAuthHTTP(t)
	var version string
	if err := a.DBPool().QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("private PostgreSQL 18.6 required")
	}
	f := &m5EndpointAcceptance{app: a, key: bytes.Repeat([]byte{71}, 32)}
	f.manager = download.NewManager(downloadstore.NewRepository(a.DBPool(), a.IDGen(), f.key))
	f.manager.SetClientFactory(app.NativeDownloadClient)
	f.media = mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(a.DBPool(), a.IDGen()))
	f.media.SetCredentialDecoder(crypto.NewSecretProtector(f.key))
	f.media.SetServerFactory(app.NativeMediaServerClient)
	handler := httpapi.NewServerHandler(a, auth, nil, nil, nil, nil, nil, nil, nil, nil, f.manager, f.media, f.key)
	handler.SetTokenManagementService(authusecase.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock()))
	server := httptest.NewServer(httpapi.NewRouter(a, handler, auth, nil))
	t.Cleanup(server.Close)
	f.base = server.URL
	response := f.request(t, "POST", "/api/v1/auth/setup", map[string]string{"username": "endpoint-admin", "password": "OwnedEndpointAcceptance2026!"})
	m5EndpointStatus(t, response, 200)
	for _, cookie := range (&http.Response{Header: response.header}).Cookies() {
		if cookie.Name == "pulsemedia_session" {
			f.session = cookie.Value
		}
	}
	if f.session == "" {
		t.Fatal("real public setup did not issue a Cookie")
	}
	return f
}
func (f *m5EndpointAcceptance) request(t *testing.T, method, path string, body any) m5EndpointResult {
	t.Helper()
	return f.withAuth(t, method, path, body, f.session, "")
}
func (f *m5EndpointAcceptance) withAuth(t *testing.T, method, path string, body any, session, token string) m5EndpointResult {
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
	r, err := http.NewRequestWithContext(t.Context(), method, f.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("public request construction failed")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.base)
	r.Header.Set("Idempotency-Key", uuid.NewString())
	if session != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(r)
	if err != nil {
		t.Fatal("public endpoint HTTP failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal("public response read failed")
	}
	m5EndpointPrivate(t, raw)
	return m5EndpointResult{response.StatusCode, raw, response.Header}
}
func m5EndpointStatus(t *testing.T, r m5EndpointResult, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("public status=%d want=%d body=%s", r.status, want, r.body)
	}
}
func m5EndpointObject(t *testing.T, r m5EndpointResult) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatal("invalid public JSON")
	}
	return v
}
func m5EndpointPrivate(t *testing.T, raw []byte) {
	t.Helper()
	for _, secret := range []string{"endpoint-secret-one", "endpoint-secret-two", "url-secret-user", "url-secret-pass", "owned-native-error-secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private native configuration leaked into public response/audit/event")
		}
	}
	for _, field := range []string{`"password"`, `"api_key"`, `"password_encrypted"`, `"api_key_encrypted"`} {
		if bytes.Contains(raw, []byte(field)) {
			t.Fatal("private credential field leaked")
		}
	}
}
func m5EndpointFamily(kind string) string {
	if kind == "qbittorrent" || kind == "transmission" {
		return "download-clients"
	}
	return "media-servers"
}
func m5EndpointInput(kind, raw, secret string) map[string]any {
	v := map[string]any{"name": "Owned " + kind, "url": raw}
	if m5EndpointFamily(kind) == "download-clients" {
		v["client_type"] = kind
		v["username"] = "native-user"
		v["password"] = secret
	} else {
		v["server_type"] = kind
		v["api_key"] = secret
	}
	return v
}
func (f *m5EndpointAcceptance) create(t *testing.T, kind, raw, secret string) (string, map[string]any) {
	t.Helper()
	r := f.request(t, "POST", "/api/v1/"+m5EndpointFamily(kind), m5EndpointInput(kind, raw, secret))
	m5EndpointStatus(t, r, 201)
	row := m5EndpointObject(t, r)
	id, ok := row["id"].(string)
	if !ok || id == "" {
		t.Fatal("create omitted identity")
	}
	return "/api/v1/" + m5EndpointFamily(kind) + "/" + id, row
}
func (f *m5EndpointAcceptance) cipher(t *testing.T, kind, path string) []byte {
	t.Helper()
	table, column := "download_client", "password_encrypted"
	if m5EndpointFamily(kind) == "media-servers" {
		table, column = "media_server", "api_key_encrypted"
	}
	var cipher []byte
	if err := f.app.DBPool().QueryRow(t.Context(), "SELECT "+column+" FROM "+table+" WHERE id=$1", path[strings.LastIndex(path, "/")+1:]).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	return cipher
}
func (f *m5EndpointAcceptance) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.app.DBPool().Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *m5EndpointAcceptance) audit(t *testing.T, path string) int {
	t.Helper()
	var n int
	if err := f.app.DBPool().QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE target_id=$1 AND action LIKE '%.probed'", path[strings.LastIndex(path, "/")+1:]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *m5EndpointAcceptance) privateRecords(t *testing.T) {
	t.Helper()
	rows, err := f.app.DBPool().Query(t.Context(), "SELECT payload::text FROM audit_log UNION ALL SELECT payload::text FROM outbox_event")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		m5EndpointPrivate(t, []byte(s))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

type m5EndpointNative struct {
	raw, safe string
	calls     atomic.Int32
	fail      atomic.Bool
	hold      atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newM5EndpointNative(t *testing.T, kind, secret string) *m5EndpointNative {
	t.Helper()
	n := &m5EndpointNative{entered: make(chan struct{}, 1), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.calls.Add(1)
		if r.URL.Path != "/private-endpoint/api/v2/auth/login" && r.URL.Path != "/private-endpoint/transmission/rpc" && r.URL.Path != "/private-endpoint/System/Info" {
			t.Error("native probe used wrong saved execution URL")
			w.WriteHeader(400)
			return
		}
		valid := false
		switch kind {
		case "qbittorrent":
			if err := r.ParseForm(); err == nil {
				valid = r.Method == "POST" && r.Form.Get("username") == "native-user" && r.Form.Get("password") == secret
			}
		case "transmission":
			u, p, ok := r.BasicAuth()
			var body struct {
				Method string `json:"method"`
			}
			err := json.NewDecoder(r.Body).Decode(&body)
			valid = ok && u == "native-user" && p == secret && err == nil && body.Method == "session-get"
		case "jellyfin":
			valid = r.Method == "GET" && r.Header.Get("Authorization") == fmt.Sprintf("MediaBrowser Token=%q", secret)
		case "emby":
			valid = r.Method == "GET" && r.Header.Get("X-Emby-Token") == secret
		}
		if !valid {
			t.Error("actual native adapter used incorrect protocol or decrypted credential")
			w.WriteHeader(401)
			return
		}
		if n.hold.Load() {
			select {
			case n.entered <- struct{}{}:
			default:
			}
			select {
			case <-n.release:
			case <-r.Context().Done():
				return
			}
		}
		if n.fail.Load() {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "owned-native-error-secret")
			return
		}
		switch kind {
		case "qbittorrent":
			_, _ = io.WriteString(w, "Ok.")
		case "transmission":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"result":"success","arguments":{"version":"4.0.6"}}`)
		default:
			_, _ = io.WriteString(w, `{"ServerName":"owned native server","Version":"4.8.0"}`)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { n.once.Do(func() { close(n.release) }) })
	n.safe = server.URL + "/private-endpoint"
	// qB uses form credentials; Transmission uses BasicAuth credentials. Keep their
	// explicit native authentication free of competing URL userinfo.
	n.raw = n.safe
	if kind == "jellyfin" || kind == "emby" {
		n.raw = strings.Replace(n.safe, "http://", "http://url-secret-user:url-secret-pass@", 1)
	}
	return n
}

func TestM5EndpointConfigurationNativeRoundTripHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	for _, kind := range []string{"qbittorrent", "transmission", "jellyfin", "emby"} {
		t.Run(kind, func(t *testing.T) {
			old := newM5EndpointNative(t, kind, "endpoint-secret-one")
			next := newM5EndpointNative(t, kind, "endpoint-secret-two")
			path, row := f.create(t, kind, old.raw, "endpoint-secret-one")
			if row["url"] != old.safe {
				t.Fatal("create URL was not projected safely")
			}
			cipher := f.cipher(t, kind, path)
			plain, err := crypto.Decrypt(f.key, cipher)
			if err != nil || string(plain) != "endpoint-secret-one" || bytes.Contains(cipher, plain) {
				t.Fatal("actual secret encryption failed")
			}
			m5EndpointStatus(t, f.request(t, "GET", "/api/v1/"+m5EndpointFamily(kind), nil), 200)
			probe := f.request(t, "POST", path+"/test", nil)
			m5EndpointStatus(t, probe, 200)
			if m5EndpointObject(t, probe)["alive"] != true || old.calls.Load() == 0 {
				t.Fatal("real native probe not executed")
			}
			before := old.calls.Load()
			edit := m5EndpointInput(kind, next.raw, "endpoint-secret-two")
			edit["expected_updated_at"] = row["updated_at"]
			saved := f.request(t, "PUT", path, edit)
			m5EndpointStatus(t, saved, 200)
			row = m5EndpointObject(t, saved)
			probe = f.request(t, "POST", path+"/test", nil)
			m5EndpointStatus(t, probe, 200)
			if m5EndpointObject(t, probe)["alive"] != true || next.calls.Load() == 0 || old.calls.Load() != before {
				t.Fatal("saved native URL and changed secret did not take immediate effect")
			}
			cipher = f.cipher(t, kind, path)
			edit = m5EndpointInput(kind, row["url"].(string), "")
			edit["expected_updated_at"] = row["updated_at"]
			m5EndpointStatus(t, f.request(t, "PUT", path, edit), 200)
			if !bytes.Equal(cipher, f.cipher(t, kind, path)) {
				t.Fatal("empty edit secret changed ciphertext bytes")
			}
			probe = f.request(t, "POST", path+"/test", nil)
			m5EndpointStatus(t, probe, 200)
			if m5EndpointObject(t, probe)["alive"] != true {
				t.Fatal("empty secret edit lost the saved native credential or full execution URL")
			}
			m5EndpointStatus(t, f.request(t, "DELETE", path, nil), 204)
			m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), 404)
		})
	}
	f.privateRecords(t)
}

func TestM5EndpointConfigurationStrictJSONAndVersionsHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	for _, kind := range []string{"qbittorrent", "jellyfin"} {
		t.Run(kind, func(t *testing.T) {
			native := newM5EndpointNative(t, kind, "endpoint-secret-one")
			path, row := f.create(t, kind, native.raw, "endpoint-secret-one")
			root := "/api/v1/" + m5EndpointFamily(kind)
			input := m5EndpointInput(kind, native.raw, "endpoint-secret-one")
			raw, _ := json.Marshal(input)
			base := string(raw)
			add := func(s string) string { return strings.TrimSuffix(base, "}") + "," + s + "}" }
			mapping := "remote_path_map"
			if kind == "jellyfin" {
				mapping = "path_mapping"
			}
			bad := []string{add(`"unknown":true`), strings.Replace(base, `"name"`, `"Name"`, 1), add(`"url":"http://127.0.0.1"`), add(`"\u0075rl":"http://127.0.0.1"`), base + ` {}`, `null`, strings.Replace(base, `"name":"Owned `+kind+`"`, `"name":null`, 1), add(`"` + mapping + `":null`), add(`"` + mapping + `":{true:"/local"}`), add(`"expected_updated_at":"2026-01-01T00:00:00Z"`)}
			for i, body := range bad {
				t.Run(fmt.Sprintf("invalid-%d", i), func(t *testing.T) { m5EndpointStatus(t, f.request(t, "POST", root, body), 400) })
			}
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 400)
			input["expected_updated_at"] = row["updated_at"]
			input[mapping] = map[string]string{"/remote": "/local"}
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 400)
			delete(input, mapping)
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 200)
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 409)
			input["expected_updated_at"] = "2000-01-01T00:00:00Z"
			input["url"] = native.raw
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 409)
			if native.calls.Load() != 0 {
				t.Fatal("invalid edits unexpectedly performed native I/O")
			}
		})
	}
}

func TestM5EndpointConfigurationNativeFailureClassesHTTPPG(t *testing.T) {
	for _, kind := range []string{"qbittorrent", "transmission", "jellyfin", "emby"} {
		t.Run(kind, func(t *testing.T) {
			f := newM5EndpointAcceptance(t)
			native := newM5EndpointNative(t, kind, "endpoint-secret-one")
			native.fail.Store(true)
			path, _ := f.create(t, kind, native.raw, "endpoint-secret-one")
			r := f.request(t, "POST", path+"/test", nil)
			m5EndpointStatus(t, r, 200)
			if m5EndpointObject(t, r)["alive"] != false || native.calls.Load() == 0 {
				t.Fatal("native failure was not a completed false probe")
			}
			before := native.calls.Load()
			table, col := "download_client", "password_encrypted"
			if m5EndpointFamily(kind) == "media-servers" {
				table, col = "media_server", "api_key_encrypted"
			}
			cipher := f.cipher(t, kind, path)
			id := path[strings.LastIndex(path, "/")+1:]
			f.exec(t, "UPDATE "+table+" SET "+col+"=$2 WHERE id=$1", id, []byte("malformed-owned-cipher"))
			m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), 503)
			if native.calls.Load() != before {
				t.Fatal("malformed ciphertext performed native I/O")
			}
			f.exec(t, "UPDATE "+table+" SET "+col+"=$2 WHERE id=$1", id, cipher)
			if table == "download_client" {
				f.manager.SetClientFactory(nil)
			} else {
				f.media.SetServerFactory(nil)
			}
			m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), 503)
			if native.calls.Load() != before {
				t.Fatal("missing native factory performed I/O")
			}
			m5EndpointStatus(t, f.request(t, "POST", "/api/v1/"+m5EndpointFamily(kind)+"/"+uuid.NewString()+"/test", nil), 404)
		})
	}
	f := newM5EndpointAcceptance(t)
	path, _ := f.create(t, "plex", "http://127.0.0.1:1/retained-plex", "endpoint-secret-one")
	m5EndpointStatus(t, f.request(t, "GET", "/api/v1/media-servers", nil), 200)
	f.media.SetServerFactory(nil)
	m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), 503)
	f.privateRecords(t)
}

func TestM5EndpointConfigurationPermissionsZeroIOHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	tokenResult := f.request(t, "POST", "/api/v1/auth/tokens", map[string]string{"name": "endpoint-read-scope"})
	m5EndpointStatus(t, tokenResult, 200)
	token, _ := m5EndpointObject(t, tokenResult)["secret"].(string)
	if token == "" {
		t.Fatal("actual token issuance missing")
	}
	for _, kind := range []string{"qbittorrent", "jellyfin"} {
		native := newM5EndpointNative(t, kind, "endpoint-secret-one")
		path, row := f.create(t, kind, native.raw, "endpoint-secret-one")
		input := m5EndpointInput(kind, native.raw, "")
		input["expected_updated_at"] = row["updated_at"]
		for _, op := range []struct {
			method, path string
			body         any
		}{{"POST", path + "/test", nil}, {"PUT", path, input}, {"DELETE", path, nil}, {"POST", "/api/v1/" + m5EndpointFamily(kind), m5EndpointInput(kind, native.raw, "")}} {
			m5EndpointStatus(t, f.withAuth(t, op.method, op.path, op.body, "", token), 403)
		}
		f.exec(t, "UPDATE app_user SET role='member' WHERE username='endpoint-admin'")
		for _, op := range []struct {
			method, path string
			body         any
		}{{"POST", path + "/test", nil}, {"PUT", path, input}, {"DELETE", path, nil}} {
			m5EndpointStatus(t, f.request(t, op.method, op.path, op.body), 403)
		}
		f.exec(t, "UPDATE app_user SET role='admin' WHERE username='endpoint-admin'")
		if native.calls.Load() != 0 {
			t.Fatal("member/token scope escalated into native I/O")
		}
	}
}

func TestM5EndpointConfigurationAccountLockFenceHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	for _, kind := range []string{"qbittorrent", "jellyfin"} {
		for _, change := range []string{"is_active=false", "role='member'"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				native := newM5EndpointNative(t, kind, "endpoint-secret-one")
				path, _ := f.create(t, kind, native.raw, "endpoint-secret-one")
				tx, err := f.app.DBPool().Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				if _, err = tx.Exec(t.Context(), "SELECT id FROM app_user WHERE username='endpoint-admin' FOR UPDATE"); err != nil {
					t.Fatal(err)
				}
				done := make(chan m5EndpointResult, 1)
				go func() { done <- f.request(t, "POST", path+"/test", nil) }()
				deadline := time.Now().Add(5 * time.Second)
				waiting := false
				for time.Now().Before(deadline) {
					if err = f.app.DBPool().QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockSettingsUser%')").Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("public request did not reach actual current-account lock")
				}
				if native.calls.Load() != 0 {
					t.Fatal("native I/O started before account lock")
				}
				if _, err = tx.Exec(t.Context(), "UPDATE app_user SET "+change+" WHERE username='endpoint-admin'"); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case r := <-done:
					m5EndpointStatus(t, r, 403)
				case <-time.After(10 * time.Second):
					t.Fatal("account-fenced request did not join")
				}
				if native.calls.Load() != 0 {
					t.Fatal("current-account fence allowed native I/O")
				}
				f.exec(t, "UPDATE app_user SET is_active=true,role='admin' WHERE username='endpoint-admin'")
			})
		}
	}
}

func TestM5EndpointConfigurationChangedDuringNativeProbeHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	for _, kind := range []string{"qbittorrent", "transmission", "jellyfin", "emby"} {
		t.Run(kind, func(t *testing.T) {
			native := newM5EndpointNative(t, kind, "endpoint-secret-one")
			native.hold.Store(true)
			path, row := f.create(t, kind, native.raw, "endpoint-secret-one")
			before := f.audit(t, path)
			done := make(chan m5EndpointResult, 1)
			go func() { done <- f.request(t, "POST", path+"/test", nil) }()
			select {
			case <-native.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native HTTP probe did not enter held server")
			}
			input := m5EndpointInput(kind, native.raw, "")
			input["name"] = "Changed during actual native Test"
			input["expected_updated_at"] = row["updated_at"]
			m5EndpointStatus(t, f.request(t, "PUT", path, input), 200)
			native.once.Do(func() { close(native.release) })
			select {
			case r := <-done:
				m5EndpointStatus(t, r, 409)
			case <-time.After(10 * time.Second):
				t.Fatal("held native probe did not join")
			}
			if f.audit(t, path) != before {
				t.Fatal("stale native probe wrote success audit")
			}
		})
	}
}

func TestM5EndpointConfigurationListStorageFailureRecoveryHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	for _, kind := range []string{"qbittorrent", "jellyfin"} {
		t.Run(kind, func(t *testing.T) {
			native := newM5EndpointNative(t, kind, "endpoint-secret-one")
			_, _ = f.create(t, kind, native.raw, "endpoint-secret-one")
			table := "download_client"
			if kind == "jellyfin" {
				table = "media_server"
			}
			root := "/api/v1/" + m5EndpointFamily(kind)
			f.exec(t, "ALTER TABLE "+table+" RENAME TO m5_owned_unavailable")
			restored := false
			defer func() {
				if !restored {
					f.exec(t, "ALTER TABLE m5_owned_unavailable RENAME TO "+table)
				}
			}()
			m5EndpointStatus(t, f.request(t, "GET", root, nil), 503)
			f.exec(t, "ALTER TABLE m5_owned_unavailable RENAME TO "+table)
			restored = true
			r := f.request(t, "GET", root, nil)
			m5EndpointStatus(t, r, 200)
			items, ok := m5EndpointObject(t, r)["items"].([]any)
			if !ok || len(items) == 0 {
				t.Fatal("actual database list did not recover")
			}
		})
	}
}

func TestM5EndpointConfigurationDeleteReferencesAndStorageFaultHTTPPG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	native := newM5EndpointNative(t, "qbittorrent", "endpoint-secret-one")
	path, _ := f.create(t, "qbittorrent", native.raw, "endpoint-secret-one")
	job := uuid.NewString()
	payload, _ := json.Marshal(map[string]any{"snapshot": map[string]any{"client_id": path[strings.LastIndex(path, "/")+1:]}})
	f.exec(t, "INSERT INTO job(id,kind,state,payload)VALUES($1,'download','queued',$2)", job, payload)
	denied := f.request(t, "DELETE", path, nil)
	m5EndpointStatus(t, denied, 409)
	code := m5EndpointObject(t, denied)["code"]
	if code != "ENDPOINT_IN_USE" && code != "CLIENT_IN_USE" {
		t.Fatal("missing referenced-client compatibility error")
	}
	f.exec(t, "DELETE FROM job WHERE id=$1", job)
	m5EndpointStatus(t, f.request(t, "DELETE", path, nil), 204)
	media := newM5EndpointNative(t, "jellyfin", "endpoint-secret-one")
	path, _ = f.create(t, "jellyfin", media.raw, "endpoint-secret-one")
	f.exec(t, `CREATE FUNCTION m5_owned_delete_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned-native-error-secret'; END $$`)
	f.exec(t, `CREATE TRIGGER m5_owned_delete_fault BEFORE DELETE ON media_server FOR EACH ROW EXECUTE FUNCTION m5_owned_delete_fault()`)
	m5EndpointStatus(t, f.request(t, "DELETE", path, nil), 503)
	f.exec(t, "DROP TRIGGER m5_owned_delete_fault ON media_server")
	f.exec(t, "DROP FUNCTION m5_owned_delete_fault()")
	m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), 200)
	m5EndpointStatus(t, f.request(t, "DELETE", path, nil), 204)
	for _, family := range []string{"download-clients", "media-servers"} {
		m5EndpointStatus(t, f.request(t, "DELETE", "/api/v1/"+family+"/"+uuid.NewString(), nil), 404)
	}
	f.privateRecords(t)
}
