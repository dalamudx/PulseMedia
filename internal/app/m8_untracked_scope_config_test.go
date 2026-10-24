package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
)

type m8ScopeConfigFixture struct {
	*m7ScrapeFixture
	scope, client, root uuid.UUID
	source, target      string
	input               ports.UntrackedScopeInput
	calls               atomic.Int32
}

func m8NewScopeConfigFixture(t *testing.T) *m8ScopeConfigFixture {
	t.Helper()
	f := &m8ScopeConfigFixture{m7ScrapeFixture: m7NewScrapeFixture(t), scope: uuid.New(), client: uuid.New(), root: uuid.New(), source: t.TempDir(), target: t.TempDir()}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(remote.Close)
	secret := crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32))
	cipher, err := secret.EncryptSecret([]byte("owned-scope-password"))
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := json.Marshal(map[string]string{"/remote": f.source})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'scope-owned','qbittorrent',$2,'owned-user',$3,$4)`, f.client, remote.URL, cipher, mapping)
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type,health,baseline_completed,marker_filename) VALUES($1,$2,'scope-target','movie','online',true,'.scope-marker')`, f.root, f.target)
	for path, body := range map[string]string{filepath.Join(f.target, ".scope-marker"): "owned", filepath.Join(f.source, "Movie.2026.mkv"): "user source remains", filepath.Join(f.target, "user.nfo"): "user sidecar remains"} {
		if err = os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := rootfs.NewFilesystem().OpenRoot(t.Context(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := root.Identity(t.Context())
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	scan := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root, identity.Device, identity.Inode)
	f.input = ports.UntrackedScopeInput{Enabled: true, ClientID: f.client, RemotePrefix: "/remote", TargetRootID: f.root}
	if err = f.pool.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.input.ExpectedClientVersion, &f.input.ExpectedRootGeneration); err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetUntrackedScopeService(library.NewUntrackedScopeService(postgres.NewUntrackedScopeStore(f.pool, f.app.IDGen(), f.app.Clock()), rootfs.NewFilesystem()))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	return f
}
func (f *m8ScopeConfigFixture) path() string {
	return "/api/v1/library/untracked-scopes/" + f.scope.String()
}
func (f *m8ScopeConfigFixture) body(t *testing.T, version int64, enabled bool) string {
	t.Helper()
	in := f.input
	in.ExpectedVersion = version
	in.Enabled = enabled
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (f *m8ScopeConfigFixture) records(t *testing.T) string {
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_directory_scope", "untracked_directory_scope_receipt", "audit_log", "outbox_event", "idempotency_record")
}
func (f *m8ScopeConfigFixture) protected(t *testing.T) string {
	return m8ConfigProtected(t, f.m7ScrapeFixture) + m8ConfigSnapshot(t, f.m7ScrapeFixture, "download_task", "download_client", "library_root", "scan_run", "scan_finalization")
}
func m8ScopeReceipt(t *testing.T, w *httptest.ResponseRecorder) ports.UntrackedScopeReceipt {
	t.Helper()
	m8ConfigCode(t, w, http.StatusOK)
	var r ports.UntrackedScopeReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.CommandKey == "" || r.Scope.Version < 1 || r.ExecutionAllowed {
		t.Fatal("invalid configuration-only receipt", err)
	}
	if strings.Contains(w.Body.String(), "owned-scope-password") || strings.Contains(w.Body.String(), "http://") {
		t.Fatal("source credentials exposed")
	}
	return r
}

func TestM8UntrackedScopeConfigCookiePG(t *testing.T) {
	f := m8NewScopeConfigFixture(t)
	ctx := t.Context()
	protected := f.protected(t)
	m8ConfigCode(t, f.request(ctx, "GET", f.path(), "", "", nil, ""), http.StatusUnauthorized)
	first := m8ScopeReceipt(t, f.request(ctx, "PUT", f.path(), f.body(t, 0, true), "original-key", f.cookie, ""))
	if first.Scope.OwnerID != f.admin || first.Scope.ID != f.scope || first.Scope.LocalRoot != f.source || first.Scope.TargetRootPath != f.target || first.Scope.SourceIdentity.Inode == first.Scope.TargetIdentity.Inode {
		t.Fatal("scope lost explicit independent source/target identity")
	}
	before := f.records(t)
	if err := os.Rename(f.source, f.source+"-moved"); err != nil {
		t.Fatal(err)
	}
	// No new root probe is needed to recover the original committed receipt.
	for _, w := range []*httptest.ResponseRecorder{f.request(ctx, "GET", f.path()+"/commands/original-key", "", "", f.cookie, ""), f.request(ctx, "PUT", f.path(), f.body(t, 0, true), "original-key", f.cookie, "")} {
		r := m8ScopeReceipt(t, w)
		if mustM8ConfigJSON(t, r) != mustM8ConfigJSON(t, first) {
			t.Fatal("original receipt changed after source disappeared")
		}
	}
	m8ConfigCode(t, f.request(ctx, "PUT", f.path(), f.body(t, 0, true), "conflict-key", f.cookie, ""), http.StatusConflict)
	if before != f.records(t) {
		t.Fatal("read/replay/conflict published new configuration records")
	}
	disabled := m8ScopeReceipt(t, f.request(ctx, "PUT", f.path(), f.body(t, 1, false), "disable-key", f.cookie, ""))
	if disabled.Scope.Enabled || disabled.Scope.Version != 2 || disabled.Scope.TargetIdentity != first.Scope.TargetIdentity {
		t.Fatal("offline disable changed frozen target baseline")
	}
	source, err := os.ReadFile(filepath.Join(f.source+"-moved", "Movie.2026.mkv"))
	if err != nil || string(source) != "user source remains" {
		t.Fatal("configuration touched source file", err)
	}
	sidecar, err := os.ReadFile(filepath.Join(f.target, "user.nfo"))
	if err != nil || string(sidecar) != "user sidecar remains" {
		t.Fatal("configuration touched target file", err)
	}
	if protected != f.protected(t) || f.calls.Load() != 0 {
		t.Fatal("configuration created tasks/facts or called downloader")
	}
	t.Log("M8_UNTRACKED_SCOPE_COOKIE_PG_PASS rootfs=REAL source_independent=true original_receipt_without_source=true offline_disable=true execution_allowed=false downloader_calls=0 protected_graph_unchanged=true")
}

func TestM8UntrackedScopeConfigPermissionsPG(t *testing.T) {
	f := m8NewScopeConfigFixture(t)
	ctx := t.Context()
	m8ScopeReceipt(t, f.request(ctx, "PUT", f.path(), f.body(t, 0, true), "original-key", f.cookie, ""))
	actor, err := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.pool, f.app.IDGen()), crypto.ReadTokenIssuer{}, f.app.Clock())
	_, bearer, err := tokens.Create(ctx, actor, ports.TokenCommand{Name: "Owned scope read token"}, "scope-read-token")
	if err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	f.exec(t, `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::text,'username','scope-other','role','member'))).* FROM app_user u WHERE id=$2`, other, f.admin)
	member := f.login(t, "scope-other")
	before := f.records(t)
	for _, identity := range []struct {
		cookie *http.Cookie
		token  string
	}{{nil, bearer}, {member, ""}} {
		m8ConfigCode(t, f.request(ctx, "GET", f.path(), "", "", identity.cookie, identity.token), http.StatusForbidden)
		m8ConfigCode(t, f.request(ctx, "GET", f.path()+"/commands/original-key", "", "", identity.cookie, identity.token), http.StatusForbidden)
		m8ConfigCode(t, f.request(ctx, "PUT", f.path(), f.body(t, 1, false), "forbidden-key", identity.cookie, identity.token), http.StatusForbidden)
	}
	f.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, other)
	m8ConfigCode(t, f.request(ctx, "GET", f.path(), "", "", member, ""), http.StatusForbidden)
	m8ConfigCode(t, f.request(ctx, "GET", f.path()+"/commands/original-key", "", "", member, ""), http.StatusForbidden)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, other)
	m8ConfigCode(t, f.request(ctx, "GET", f.path(), "", "", member, ""), http.StatusUnauthorized)
	if before != f.records(t) || f.calls.Load() != 0 {
		t.Fatal("denied identity read or wrote another owner's settings")
	}
	t.Log("M8_UNTRACKED_SCOPE_PERMISSIONS_PG_PASS real_cookie=true token_member_denied=true foreign_admin_hidden=true revoked_session=true downloader_calls=0")
}

func TestM8UntrackedScopeConfigFailurePG(t *testing.T) {
	f := m8NewScopeConfigFixture(t)
	ctx := t.Context()
	m8ScopeReceipt(t, f.request(ctx, "PUT", f.path(), f.body(t, 0, true), "original-key", f.cookie, ""))
	before, protected := f.records(t), f.protected(t)
	f.exec(t, `ALTER TABLE untracked_directory_scope RENAME TO owned_scope_unavailable`)
	m8ConfigCode(t, f.request(ctx, "GET", f.path(), "", "", f.cookie, ""), http.StatusServiceUnavailable)
	f.exec(t, `ALTER TABLE owned_scope_unavailable RENAME TO untracked_directory_scope`)
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
	m8ConfigCode(t, f.request(ctx, "PUT", f.path(), f.body(t, 1, false), "isolated-key", f.cookie, ""), http.StatusServiceUnavailable)
	if before != f.records(t) || protected != f.protected(t) || f.calls.Load() != 0 {
		t.Fatal("read failure or isolation published settings or altered protected graph")
	}
	t.Log("M8_UNTRACKED_SCOPE_FAILURE_PG_PASS actual_503=true recovery_isolation=true protected_graph_unchanged=true downloader_calls=0")
}
