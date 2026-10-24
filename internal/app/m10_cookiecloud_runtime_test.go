package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	cookiecloudusecase "github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

const m10CloudConfigURL = "/api/v1/integrations/cookiecloud/config"
const m10CloudNativeKey = "owned-cloud-key"
const m10CloudNativePassword = "owned-cloud-password"

type m10CloudTransport struct {
	cloudHost, sourceHost string
	transport             http.RoundTripper
	ignoreCancellation    bool
}

func (r m10CloudTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || (req.URL.Host != r.cloudHost && req.URL.Host != r.sourceHost) {
		return nil, errors.New("unexpected owned CookieCloud destination")
	}
	if req.URL.Host == r.cloudHost && r.ignoreCancellation {
		req = req.Clone(context.WithoutCancel(req.Context()))
	}
	return r.transport.RoundTrip(req)
}

type m10CloudRuntime struct {
	*m7ScrapeFixture
	runtime                          *m8SubtitleRuntimeFixture
	ctx                              context.Context
	base                             string
	source                           ports.IndexerManagementView
	secret                           *crypto.SecretProtector
	entered, release                 chan struct{}
	enteredOnce, releaseOnce         sync.Once
	cloudCalls, statsCalls, badCalls atomic.Int32
	originalCredential               []byte
	configReceipt                    []byte
}

func m10EncryptedFixture(t *testing.T, host string) []byte {
	t.Helper()
	plain, err := json.Marshal(map[string]any{"cookie_data": map[string]any{"untrusted-group": []any{map[string]any{"name": "owned-account-fixture", "value": "synthetic", "domain": host, "path": "/", "hostOnly": true, "secure": false, "session": true}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	digest := md5.Sum([]byte(m10CloudNativeKey + "-" + m10CloudNativePassword))
	key := []byte(hex.EncodeToString(digest[:])[:16])
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	defer clear(padded)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(encrypted, padded)
	envelope, err := json.Marshal(map[string]string{"encrypted": base64.StdEncoding.EncodeToString(encrypted), "crypto_type": "aes-128-cbc-fixed"})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
func m10NewCloudRuntime(t *testing.T, blocked bool) *m10CloudRuntime {
	t.Helper()
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	f := m7NewScrapeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	t.Cleanup(cancel)
	v := &m10CloudRuntime{m7ScrapeFixture: f, ctx: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			v.badCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/pt/torrents.php":
			cookie := r.Header.Get("Cookie")
			if cookie != "owned-account-fixture=before-sync" && cookie != "owned-account-fixture=synthetic" {
				v.badCalls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `<!doctype html><html><body><table class="torrents"><tr><td>No torrents found</td></tr></table></body></html>`)
		case "/pt/index.php":
			v.statsCalls.Add(1)
			if r.Header.Get("Cookie") != "owned-account-fixture=synthetic" {
				v.badCalls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, m8StatsRuntimeHTML)
		default:
			v.badCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(source.Close)
	sourceURL, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	envelope := m10EncryptedFixture(t, sourceURL.Hostname())
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.cloudCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/private-cloud-prefix/get/"+m10CloudNativeKey || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.ContentLength > 0 {
			v.badCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		v.enteredOnce.Do(func() { close(v.entered) })
		if blocked {
			<-v.release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(envelope)
	}))
	t.Cleanup(cloud.Close)
	cfg := *f.app.Config()
	cfg.DataDir = t.TempDir()
	key, err := crypto.EnsureMasterKey(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	v.secret = crypto.NewSecretProtector(key)
	actor, err := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	manager := search.NewIndexerManagementService(postgres.NewIndexerManagementStore(f.pool, f.app.IDGen(), v.secret), v.secret)
	target, credential := source.URL+"/pt/", "owned-account-fixture=before-sync"
	receipt, err := manager.Create(ctx, actor, ports.IndexerManagementInput{Name: "Owned CookieCloud Nexus", Protocol: ports.IndexerProtocolNexusPHP, URL: &target, Credential: &credential, Categories: []int{}}, "cloud-native-site")
	if err != nil {
		t.Fatal(err)
	}
	v.source = receipt.Indexer
	if err = f.pool.QueryRow(ctx, "SELECT api_key_encrypted FROM indexer WHERE id=$1", v.source.ID).Scan(&v.originalCredential); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetCookieCloudConfigService(cookiecloudusecase.NewConfigService(postgres.NewCookieCloudConfigStore(f.pool, f.app.IDGen()), v.secret))
	f.router = httpapi.NewRouter(f.app, handler, f.auth, nil)
	body, _ := json.Marshal(map[string]any{"expected_version": 0, "enabled": true, "interval_seconds": 60, "connection": map[string]string{"server_url": cloud.URL + "/private-cloud-prefix", "user_key": m10CloudNativeKey, "password": m10CloudNativePassword}})
	saved := f.request(ctx, http.MethodPut, m10CloudConfigURL, string(body), "cloud-native-config", f.cookie, "")
	m7ScrapeCode(t, saved, http.StatusOK)
	v.configReceipt = append([]byte(nil), saved.Body.Bytes()...)
	var untouched bool
	if err = f.pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM cookiecloud_sync_attempt) AND NOT EXISTS(SELECT 1 FROM job WHERE kind='cookiecloud_sync')`).Scan(&untouched); err != nil || !untouched || v.cloudCalls.Load() != 0 {
		t.Fatal("saving config executed synchronization", err)
	}
	// A historical idle schedule is already due. The production interval stays 60s.
	if _, err = f.pool.Exec(ctx, `INSERT INTO cookiecloud_sync_schedule(owner_id,config_version,next_due_at,state) VALUES($1,1,clock_timestamp()-interval '1 second','idle')`, f.admin); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	v.base = "http://" + cfg.Server.HTTPAddr
	cloudURL, err := url.Parse(cloud.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	v.runtime = &m8SubtitleRuntimeFixture{pool: f.pool, cfg: &cfg, client: &http.Client{Transport: m10CloudTransport{cloudURL.Host, sourceURL.Host, transport, blocked}, Timeout: 15 * time.Second}}
	t.Cleanup(func() { v.unblock(); v.runtime.stop(t) })
	return v
}
func (v *m10CloudRuntime) unblock() { v.releaseOnce.Do(func() { close(v.release) }) }
func (v *m10CloudRuntime) predicate(query string, args ...any) bool {
	var ready bool
	return v.pool.QueryRow(v.ctx, query, args...).Scan(&ready) == nil && ready
}
func (v *m10CloudRuntime) wait(t *testing.T, phase string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-v.ctx.Done():
			t.Fatal("CookieCloud native App acceptance deadline", phase)
		case <-ticker.C:
		}
	}
}
func (v *m10CloudRuntime) start(t *testing.T) {
	t.Helper()
	v.runtime.start(t)
	v.wait(t, "App HTTP readiness", func() bool {
		req, _ := http.NewRequestWithContext(v.ctx, http.MethodGet, v.base+"/api/v1/auth/status", nil)
		res, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
}
func (v *m10CloudRuntime) requestHTTP(t *testing.T, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequestWithContext(v.ctx, method, v.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", v.base)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(v.cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("actual CookieCloud App HTTP", err)
	}
	defer func() { _ = response.Body.Close() }()
	w := httptest.NewRecorder()
	w.Code = response.StatusCode
	w.Header().Set("Cache-Control", response.Header.Get("Cache-Control"))
	if _, err = io.Copy(w.Body, response.Body); err != nil {
		t.Fatal(err)
	}
	return w
}
func (v *m10CloudRuntime) protected(t *testing.T) string {
	return m8ConfigSnapshot(t, v.m7ScrapeFixture, "media", "media_identity", "media_unit", "subscription", "wanted_item", "library_file", "library_file_unit", "unit_fact", "download_task", "download_requirement", "acquisition_reservation", "unit_reservation")
}
func (v *m10CloudRuntime) business(t *testing.T) string {
	return (&m8StatsRuntimeFixture{m7ScrapeFixture: v.m7ScrapeFixture, ctx: v.ctx}).businessSnapshot(t)
}
func (v *m10CloudRuntime) unchangedCredential(t *testing.T) {
	t.Helper()
	var version int64
	var cipher []byte
	if err := v.pool.QueryRow(v.ctx, "SELECT config_version,api_key_encrypted FROM indexer WHERE id=$1", v.source.ID).Scan(&version, &cipher); err != nil || version != 1 || !bytes.Equal(cipher, v.originalCredential) {
		t.Fatal("rejected native response changed source credential", err)
	}
	if !v.predicate("SELECT NOT EXISTS(SELECT 1 FROM indexer_cookiecloud_credential)") {
		t.Fatal("rejected native response published evidence")
	}
}
func (v *m10CloudRuntime) originalRead(t *testing.T) {
	t.Helper()
	v.runtime.stop(t)
	before := v.business(t)
	calls := v.cloudCalls.Load()
	receipt := v.request(v.ctx, http.MethodGet, m10CloudConfigURL+"/commands/cloud-native-config", "", "", v.cookie, "")
	m7ScrapeCode(t, receipt, http.StatusOK)
	if !bytes.Equal(receipt.Body.Bytes(), v.configReceipt) || before != v.business(t) || calls != v.cloudCalls.Load() {
		t.Fatal("original GET changed receipt, business facts or network count")
	}
}
func TestM10CookieCloudNativeSyncAndSourceUsePG(t *testing.T) {
	v := m10NewCloudRuntime(t, false)
	before := v.protected(t)
	v.start(t)
	v.wait(t, "one committed synchronization", func() bool {
		return v.predicate("SELECT count(*)=1 AND bool_and(state='succeeded') FROM cookiecloud_sync_attempt")
	})
	var version int64
	var raw []byte
	if err := v.pool.QueryRow(v.ctx, "SELECT config_version,api_key_encrypted FROM indexer WHERE id=$1", v.source.ID).Scan(&version, &raw); err != nil {
		t.Fatal(err)
	}
	plain, err := v.secret.DecryptSecret(raw)
	if err != nil || version != 2 || string(plain) != "owned-account-fixture=synthetic" || bytes.Equal(raw, v.originalCredential) {
		t.Fatal("App failed to replace encrypted site cookie", err)
	}
	clear(plain)
	if v.cloudCalls.Load() != 1 || !v.predicate(`SELECT count(*)=1 FROM indexer_cookiecloud_credential WHERE indexer_id=$1 AND source_version=2 AND valid_until IS NULL`, v.source.ID) {
		t.Fatal("App did not bind one encrypted backup to credential evidence")
	}
	path := "/api/v1/indexers/" + v.source.ID.String() + "/account-stats"
	save := v.requestHTTP(t, http.MethodPut, path+"/config", `{"expected_account_id":"42","profile":"nexusphp_classic_info_block_v1","enabled":true,"interval_seconds":900,"expected_version":0,"expected_source_version":2}`, "after-cloud-native-account")
	m7ScrapeCode(t, save, http.StatusOK)
	v.wait(t, "native Nexus accepted synchronized Cookie", func() bool {
		return v.predicate("SELECT count(*)=1 AND bool_and(state='succeeded') FROM site_account_stats_attempt")
	})
	if v.statsCalls.Load() != 1 || v.badCalls.Load() != 0 || before != v.protected(t) {
		t.Fatal("native Cookie use or protected inventory contract failed")
	}
	v.originalRead(t)
	if !t.Failed() {
		t.Log("M10_COOKIECLOUD_APP_NATIVE_PASS Cookie=REAL backup=ENCRYPTED_GET_ONCE local_decrypt=REAL source=ENCRYPTED_VERSION_2 native_Nexus=REAL_COOKIE original=BYTE_IDENTICAL_GET protected=UNCHANGED private_tracker=NO")
	}
}
func TestM10CookieCloudDisabledLateResponsePG(t *testing.T) {
	v := m10NewCloudRuntime(t, true)
	before := v.protected(t)
	v.start(t)
	select {
	case <-v.entered:
	case <-v.ctx.Done():
		t.Fatal("native backup did not enter")
	}
	saved := v.requestHTTP(t, http.MethodPut, m10CloudConfigURL, `{"expected_version":1,"enabled":false,"interval_seconds":60}`, "disable-during-native-read")
	m7ScrapeCode(t, saved, http.StatusOK)
	v.unblock()
	v.wait(t, "late result safely terminated", func() bool {
		return v.predicate("SELECT count(*)=1 AND bool_and(state='dead') FROM job WHERE kind='cookiecloud_sync'")
	})
	v.runtime.stop(t)
	v.unchangedCredential(t)
	if v.cloudCalls.Load() != 1 || v.badCalls.Load() != 0 || before != v.protected(t) {
		t.Fatal("late deactivation repeated or published the native read")
	}
	v.originalRead(t)
}
func TestM10CookieCloudShutdownWaitsForReadPG(t *testing.T) {
	v := m10NewCloudRuntime(t, true)
	before := v.protected(t)
	v.start(t)
	select {
	case <-v.entered:
	case <-v.ctx.Done():
		t.Fatal("native backup did not enter")
	}
	v.runtime.cancel()
	select {
	case err := <-v.runtime.done:
		v.runtime.cancel = nil
		_ = v.runtime.app.Close()
		t.Fatal("App returned while native HTTP still executing", err)
	case <-time.After(150 * time.Millisecond):
	}
	v.unchangedCredential(t)
	conn, err := v.pool.Acquire(v.ctx)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := db.New(conn).TryCookieCloudSyncGate(v.ctx, v.admin)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if owned {
		_, _ = db.New(conn).ReleaseCookieCloudSyncGate(v.ctx, v.admin)
	}
	conn.Release()
	if owned {
		t.Fatal("cancellation released physical permit before native HTTP returned")
	}
	v.unblock()
	v.runtime.stop(t)
	v.unchangedCredential(t)
	repo := postgres.NewCookieCloudSyncStore(v.pool, v.app.IDGen(), v.secret, v.secret)
	if err = repo.RecoverExpired(v.ctx); err != nil {
		t.Fatal("post-cancel recovery", err)
	}
	if !v.predicate("SELECT count(*)=1 AND bool_and(state='review_required') FROM cookiecloud_sync_attempt") || v.cloudCalls.Load() != 1 || v.badCalls.Load() != 0 || before != v.protected(t) {
		t.Fatal("shutdown lost permanent attempt or replayed native backup")
	}
}
func TestM10CookieCloudWritebackFailureDoesNotReplayPG(t *testing.T) {
	v := m10NewCloudRuntime(t, false)
	before := v.protected(t)
	if _, err := v.pool.Exec(v.ctx, `CREATE FUNCTION owned_cloud_app_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned cloud application tail'; END $$; CREATE TRIGGER owned_cloud_app_tail BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='indexer.credentials_synchronized') EXECUTE FUNCTION owned_cloud_app_tail()`); err != nil {
		t.Fatal(err)
	}
	v.start(t)
	v.wait(t, "failed writeback review protection", func() bool {
		return v.predicate("SELECT count(*)=1 AND bool_and(state='review_required') FROM cookiecloud_sync_attempt")
	})
	v.runtime.stop(t)
	v.unchangedCredential(t)
	if v.cloudCalls.Load() != 1 || v.badCalls.Load() != 0 || before != v.protected(t) {
		t.Fatal("writeback fault repeated the native read or changed inventory")
	}
	v.originalRead(t)
}
