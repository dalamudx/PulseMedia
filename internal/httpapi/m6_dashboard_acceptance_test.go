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
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	m6Overview = "/api/v1/library/overview"
	m6Activity = "/api/v1/dashboard/activity"
	m6Password = "OwnedDashboardFixture123!"
)

// SQL prepares registered facts only. No App.Run, transfer, physical sampler or
// ingestion is exercised here; those have separate acceptance owners.
type m6DashboardFixture struct {
	app                     *app.App
	base, admin, member     string
	adminID, memberID       uuid.UUID
	root, client, candidate uuid.UUID
	unit                    uuid.UUID
	at                      time.Time
	probes                  atomic.Int32
}

type m6DashboardResponse struct {
	status int
	body   []byte
	header http.Header
}

func newM6DashboardFixture(t *testing.T) *m6DashboardFixture {
	t.Helper()
	if os.Getenv("PULSE_M6_DASHBOARD_ACCEPTANCE") != "1" {
		t.Skip("set PULSE_M6_DASHBOARD_ACCEPTANCE=1 through scripts/verify_m6_dashboard_api.sh")
	}
	tmp := os.Getenv("TMPDIR")
	if !strings.HasPrefix(tmp, "/data/temp/") || os.Getenv("TMP") != tmp || os.Getenv("TEMP") != tmp {
		t.Fatal("owned TMPDIR/TMP/TEMP under /data/temp required")
	}
	a, authSvc, _ := setupAuthHTTP(t)
	f := &m6DashboardFixture{app: a, root: uuid.New(), client: uuid.New(), candidate: uuid.New()}
	var version int
	if err := a.DBPool().QueryRow(t.Context(), "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil || version != 180006 {
		t.Fatalf("private PostgreSQL 18.6 required: version=%d error=%v", version, err)
	}
	if postgres.CurrentMigrationVersion != 92 {
		t.Fatalf("review acceptance fixtures for schema %d; expected 92", postgres.CurrentMigrationVersion)
	}
	if err := a.DBPool().QueryRow(t.Context(), "SELECT transaction_timestamp()").Scan(&f.at); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock()))
	server := httptest.NewServer(httpapi.NewRouter(a, handler, authSvc, nil))
	t.Cleanup(server.Close)
	f.base = server.URL
	setup := f.request(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{"username": "dashboard-admin", "password": m6Password}, "", "")
	m6Status(t, setup, 200)
	f.admin = m6Cookie(t, setup)
	var identity httpapi.AuthResponse
	m6Decode(t, setup, &identity)
	f.adminID = identity.User.Id
	f.memberID = uuid.New()
	f.exec(t, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'dashboard-member',password_hash,'member',true FROM app_user WHERE id=$2`, f.memberID, f.adminID)
	login := f.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "dashboard-member", "password": m6Password}, "", "")
	m6Status(t, login, 200)
	f.member = m6Cookie(t, login)
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type) VALUES($1,$2,'Dashboard shared','movie')`, f.root, filepath.Join(tmp, "m6-never-scanned-"+f.root.String()))
	// An unexpected downloader/indexer request is observable on loopback only.
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.probes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(sink.Close)
	source := uuid.New()
	f.exec(t, `INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'Dashboard source',$2,'m6-private-cipher')`, source, sink.URL)
	f.exec(t, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'Dashboard release','m6-private-guid','m6-private-link',100)`, f.candidate, source)
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted) VALUES($1,'Dashboard client','qbittorrent',$2,'m6-private-user','m6-private-cipher')`, f.client, sink.URL)
	_, f.unit = f.media(t, "movie", "Activity work")
	return f
}

func (f *m6DashboardFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.app.DBPool().Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// Include every business table, including empty jobs, all facts, scan observations,
// outbox, receipts and audit. Authentication bookkeeping is outside this ledger.
func (f *m6DashboardFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.app.DBPool().Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('goose_db_version','app_user','app_session','api_token') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var queries []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		queries = append(queries, fmt.Sprintf("SELECT '%s',md5(coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text) FROM %s r", table, pgx.Identifier{table}.Sanitize()))
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows, err = f.app.DBPool().Query(t.Context(), strings.Join(queries, " UNION ALL "))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var table, digest string
		if err = rows.Scan(&table, &digest); err != nil {
			t.Fatal(err)
		}
		out[table] = digest
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *m6DashboardFixture) request(t *testing.T, method, path string, body any, session, token string) m6DashboardResponse {
	t.Helper()
	var before map[string]string
	if method == http.MethodGet {
		before = f.snapshot(t)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, f.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("construct public request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.base)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("public loopback HTTP failed")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal("public response read failed")
	}
	if method == http.MethodGet {
		if after := f.snapshot(t); !reflect.DeepEqual(before, after) {
			for table, digest := range before {
				if after[table] != digest {
					t.Errorf("GET %s changed business table %s", path, table)
				}
			}
			t.Fatal("dashboard GET changed the business ledger")
		}
		if f.probes.Load() != 0 {
			t.Fatal("dashboard GET performed a network probe")
		}
		for _, private := range []string{"postgres://", "postgresql://", "/data/", "m6-private", `"path"`, `"rel_path"`, `"root_device"`, `"root_inode"`, `"scan_token"`, `"generation"`, `"password_encrypted"`, `"api_key_encrypted"`, `"link_encrypted"`, `"content_path"`, `"save_path"`, `"info_hash"`, "6d362d707269766174652d636970686572"} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatalf("GET %s exposed private configuration or storage identity", path)
			}
		}
	}
	return m6DashboardResponse{response.StatusCode, raw, response.Header}
}

func m6Status(t *testing.T, response m6DashboardResponse, want int) {
	t.Helper()
	if response.status != want {
		t.Fatalf("public HTTP status=%d want=%d body=%s", response.status, want, response.body)
	}
	if want >= 400 {
		var problem struct {
			Status int    `json:"status"`
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		m6Decode(t, response, &problem)
		if !strings.HasPrefix(response.header.Get("Content-Type"), "application/problem+json") || problem.Status != want || problem.Code == "" || problem.Detail == "" {
			t.Fatalf("unsafe/incomplete problem response: %s", response.body)
		}
	}
}

func m6Decode(t *testing.T, response m6DashboardResponse, out any) {
	t.Helper()
	if err := json.Unmarshal(response.body, out); err != nil {
		t.Fatal("invalid public JSON", err)
	}
}

func m6Cookie(t *testing.T, response m6DashboardResponse) string {
	t.Helper()
	for _, cookie := range (&http.Response{Header: response.header}).Cookies() {
		if cookie.Name == "pulsemedia_session" && cookie.Value != "" && cookie.HttpOnly {
			return cookie.Value
		}
	}
	t.Fatal("public setup/login did not issue a real HttpOnly Cookie")
	return ""
}

func (f *m6DashboardFixture) overview(t *testing.T, suffix string) ports.LibraryOverview {
	t.Helper()
	r := f.request(t, http.MethodGet, m6Overview+suffix, nil, f.member, "")
	m6Status(t, r, 200)
	var out ports.LibraryOverview
	m6Decode(t, r, &out)
	if out.ObservedAt.IsZero() || out.Roots == nil || out.Recent == nil {
		t.Fatal("missing snapshot time or non-null public arrays")
	}
	return out
}

func (f *m6DashboardFixture) activity(t *testing.T, suffix, session, token string) ports.DashboardActivityPage {
	t.Helper()
	r := f.request(t, http.MethodGet, m6Activity+suffix, nil, session, token)
	m6Status(t, r, 200)
	var out ports.DashboardActivityPage
	m6Decode(t, r, &out)
	if out.ObservedAt.IsZero() || out.Items == nil {
		t.Fatal("missing activity snapshot time or non-null array")
	}
	return out
}

func (f *m6DashboardFixture) media(t *testing.T, kind, title string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	m, u := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO media(id,title,media_type,year) VALUES($1,$2,$3,2026)`, m, title, kind)
	key := "movie"
	if kind == "tv" {
		key = "S01E01"
	}
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,$3)`, u, m, key)
	return m, u
}

func (f *m6DashboardFixture) file(t *testing.T, root, unit uuid.UUID, stage string, at time.Time) uuid.UUID {
	t.Helper()
	file := uuid.New()
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,42,1)`, file, root, "m6-private-"+file.String()+".mkv")
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, unit)
	f.fact(t, file, root, unit, stage, at)
	return file
}

func (f *m6DashboardFixture) fact(t *testing.T, file, root, unit uuid.UUID, stage string, at time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO unit_fact(id,source_file_id,root_id,unit_id,stage,created_at) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), file, root, unit, stage, at)
}

func TestM6DashboardLibraryHTTPPG(t *testing.T) {
	f := newM6DashboardFixture(t)
	var recentMovie uuid.UUID
	for i := range 26 {
		m, u := f.media(t, "movie", fmt.Sprintf("Imported movie %02d", i))
		f.file(t, f.root, u, "imported", f.at.Add(-time.Duration(i+1)*time.Hour))
		if i == 0 {
			recentMovie = m
		}
	}
	show, first := f.media(t, "tv", "Multi-unit show")
	second := uuid.New()
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'S01E02')`, second, show)
	multi := f.file(t, f.root, first, "imported", f.at.Add(-2*time.Hour))
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, multi, second)
	f.fact(t, multi, f.root, second, "imported", f.at)
	other := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type,health) VALUES($1,$2,'Offline duplicate','tv','offline')`, other, filepath.Join(os.Getenv("TMPDIR"), "m6-never-scanned-"+other.String()))
	f.file(t, other, first, "imported", f.at.Add(-3*time.Hour))
	for _, mode := range []string{"subscription", "confirmed", "revoked", "expired", "file_retired", "mapping_retired", "unit_retired"} {
		m, u := f.media(t, "movie", "Excluded "+mode)
		if mode == "subscription" {
			f.exec(t, `INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.memberID, m, f.root)
			continue
		}
		stage := "imported"
		if mode == "confirmed" {
			stage = "confirmed"
		}
		file := f.file(t, f.root, u, stage, f.at)
		switch mode {
		case "revoked":
			f.exec(t, `UPDATE unit_fact SET revoked_at=transaction_timestamp() WHERE source_file_id=$1`, file)
		case "expired":
			f.exec(t, `UPDATE unit_fact SET expires_at=transaction_timestamp()-interval '1 second' WHERE source_file_id=$1`, file)
			f.fact(t, file, f.root, u, "confirmed", f.at)
		case "file_retired":
			f.exec(t, `UPDATE library_file SET retired_at=transaction_timestamp() WHERE id=$1`, file)
		case "mapping_retired":
			f.exec(t, `UPDATE library_file_unit SET retired_at=transaction_timestamp() WHERE file_id=$1`, file)
		case "unit_retired":
			f.exec(t, `UPDATE media_unit SET retired=true WHERE id=$1`, u)
		}
	}
	want := ports.LibraryOverviewCounts{Movies: "26", TVShows: "1", Episodes: "2", Files: "28"}
	for limit := 1; limit <= 24; limit++ {
		out := f.overview(t, fmt.Sprintf("?recent_limit=%d", limit))
		if out.Counts != want || len(out.Recent) != limit || out.Recent[0].MediaID != recentMovie {
			t.Fatalf("aggregate/limit=%d: %+v recent=%d", limit, out.Counts, len(out.Recent))
		}
		for _, item := range out.Recent {
			if item.MediaID == show && !item.ImportedAt.Equal(f.at.Add(-2*time.Hour)) {
				t.Fatal("multi-unit fact reset genuine file arrival")
			}
		}
	}
	if out := f.overview(t, ""); len(out.Recent) != 12 || out.Counts != want {
		t.Fatal("omitted recent_limit must default to 12")
	}
	// Correct an older physical file to a different work; its first import remains old.
	oldMedia, oldUnit := f.media(t, "movie", "Incorrect old mapping")
	file := f.file(t, f.root, oldUnit, "imported", f.at.Add(-4*time.Hour-30*time.Minute))
	before := f.overview(t, "?recent_limit=24")
	corrected, correctedUnit := f.media(t, "movie", "Corrected mapping")
	f.exec(t, `UPDATE unit_fact SET revoked_at=transaction_timestamp() WHERE source_file_id=$1`, file)
	f.exec(t, `UPDATE library_file_unit SET retired_at=transaction_timestamp() WHERE file_id=$1`, file)
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, correctedUnit)
	f.fact(t, file, f.root, correctedUnit, "imported", f.at)
	after := f.overview(t, "?recent_limit=24")
	for i, item := range before.Recent {
		if item.MediaID == oldMedia {
			item.MediaID, item.Title = corrected, "Corrected mapping"
		}
		if !reflect.DeepEqual(item, after.Recent[i]) {
			t.Fatalf("mapping correction reordered recent slot %d", i)
		}
	}
	// Poster is a version/identity-bound display value, with no fetch by GET.
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','550')`, uuid.New(), recentMovie)
	f.exec(t, `INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb','tmdb.movie','550',metadata_version,'/TrustedPoster.jpg' FROM media WHERE id=$1`, recentMovie)
	out := f.overview(t, "?recent_limit=1")
	if out.Recent[0].PosterURL == nil || *out.Recent[0].PosterURL != "https://image.tmdb.org/t/p/w342/TrustedPoster.jpg" {
		t.Fatal("trusted versioned poster absent")
	}
	f.exec(t, `UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1`, recentMovie)
	if f.overview(t, "?recent_limit=1").Recent[0].PosterURL != nil {
		t.Fatal("stale artwork survived metadata version change")
	}
}

func (f *m6DashboardFixture) scan(t *testing.T, at time.Time, free int64) uuid.UUID {
	t.Helper()
	run := uuid.New()
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',$3,$3 FROM library_root WHERE id=$2`, run, f.root, at)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,99,true,$3 FROM library_root WHERE id=$2`, run, f.root, at)
	f.exec(t, `UPDATE library_root SET baseline_completed=true WHERE id=$1`, f.root)
	f.exec(t, `INSERT INTO root_capacity_observation(scan_run_id,total_bytes,available_bytes,sampled_at) VALUES($1,9007199254740993,$2,$3)`, run, free, at)
	return run
}

func TestM6DashboardCapacityHTTPPG(t *testing.T) {
	f := newM6DashboardFixture(t)
	f.file(t, f.root, f.unit, "imported", f.at.Add(-2*time.Hour))
	f.scan(t, f.at.Add(-time.Hour), 4007199254740993)
	latest := f.scan(t, f.at.Add(-time.Minute), 0)
	counts := ports.LibraryOverviewCounts{Movies: "1", TVShows: "0", Episodes: "0", Files: "1"}
	check := func(state, available string, free *string) {
		t.Helper()
		out := f.overview(t, "")
		if len(out.Roots) != 1 || out.Counts != counts || len(out.Recent) != 1 {
			t.Fatal("root availability erased registered library facts")
		}
		r := out.Roots[0]
		if r.CapacityState != state || r.Availability != available || !reflect.DeepEqual(r.AvailableBytes, free) {
			t.Fatalf("capacity state: %+v", r)
		}
		if state == "unknown" {
			if r.TotalBytes != nil || r.SampledAt != nil {
				t.Fatal("unknown capacity fabricated bytes or sample time")
			}
		} else if r.TotalBytes == nil || *r.TotalBytes != "9007199254740993" || r.SampledAt == nil {
			t.Fatal("capacity lost decimal precision or persisted sample time")
		}
	}
	zero := "0"
	check("fresh", "available", &zero)
	f.exec(t, `UPDATE root_capacity_observation SET sampled_at=$2 WHERE scan_run_id=$1`, latest, f.at.Add(-31*time.Minute))
	check("stale", "available", &zero)
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	check("unknown", "unknown", nil)
	f.exec(t, `UPDATE library_root SET health='online' WHERE id=$1`, f.root)
	f.exec(t, `DELETE FROM root_capacity_observation WHERE scan_run_id=$1`, latest)
	check("unknown", "available", nil)
	// A newer failed/partial scan cannot revive the earlier successful sample.
	for i, state := range []string{"failed", "partial", "running"} {
		f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at) SELECT $1,id,scan_token,generation,mutation_seq,$3,$4 FROM library_root WHERE id=$2`, uuid.New(), f.root, state, f.at.Add(time.Duration(i)*time.Second))
		check("unknown", "unknown", nil)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("TMPDIR"), "m6-never-scanned-"+f.root.String())); !os.IsNotExist(err) {
		t.Fatal("GET created or touched fixture root instead of reading persisted samples")
	}
}

func (f *m6DashboardFixture) operation(t *testing.T, owner uuid.UUID, state, organization string, at time.Time) uuid.UUID {
	t.Helper()
	op := uuid.New()
	f.exec(t, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,$6)`, uuid.New(), op, f.unit, f.root, owner.String(), at)
	f.exec(t, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,organization_state,save_path,content_path,progress,total_bytes,downloaded_bytes,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'/m6-private-save','/m6-private-content',0.25,100,500,$8)`, uuid.New(), op, f.client, f.candidate, strings.Repeat("a", 40), state, organization, at)
	return op
}

func TestM6DashboardActivityHTTPPG(t *testing.T) {
	f := newM6DashboardFixture(t)
	own := make([]uuid.UUID, 15)
	for i := range own {
		own[i] = f.operation(t, f.memberID, "tracking", "pending", f.at.Add(-time.Duration(i+1)*time.Hour))
	}
	hidden := f.operation(t, f.adminID, "tracking", "pending", f.at)
	for limit := 1; limit <= 12; limit++ {
		out := f.activity(t, fmt.Sprintf("?limit=%d", limit), f.member, "")
		if len(out.Items) != limit || !out.More {
			t.Fatalf("activity limit=%d len=%d more=%t", limit, len(out.Items), out.More)
		}
		for i, item := range out.Items {
			if item.OperationID != own[i] || item.Status != "tracking" || item.Progress == nil || *item.Progress != 0.25 || item.ProgressSource != "native_percent" || item.UpdatedAt == nil || !item.UpdatedAt.Equal(f.at.Add(-time.Duration(i+1)*time.Hour)) {
				t.Fatalf("native percentage/ownership/honest task time: %+v", item)
			}
		}
	}
	if out := f.activity(t, "", f.member, ""); len(out.Items) != 6 || !out.More {
		t.Fatal("omitted activity limit must default to six")
	}
	if out := f.activity(t, "?limit=12", f.admin, ""); len(out.Items) != 12 || out.Items[0].OperationID != hidden {
		t.Fatal("admin must see other owners' operations")
	}
	for _, tc := range []struct{ name, update, status string }{
		{"unknown", "state='unknown'", "unknown"},
		{"review", "state='completed',organization_state='review_required'", "review_required"},
		{"organizing", "state='completed',organization_state='queued'", "organizing"},
		{"missing-path", "content_path=NULL", "tracking"},
		{"missing-size", "total_bytes=0", "tracking"},
		{"errored-evidence", "error='m6-private-native-error'", "tracking"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.exec(t, `UPDATE download_task SET state='tracking',organization_state='pending',content_path='/m6-private-content',total_bytes=100,error=NULL WHERE operation_id=$1`, own[0])
			f.exec(t, "UPDATE download_task SET "+tc.update+" WHERE operation_id=$1", own[0])
			item := f.activity(t, "?limit=1", f.member, "").Items[0]
			if item.OperationID != own[0] || item.Status != tc.status || item.Progress != nil || item.ProgressSource != "" || item.UpdatedAt != nil {
				t.Fatalf("unsupported telemetry must remain unknown: %+v", item)
			}
		})
	}
	f.exec(t, `UPDATE download_task SET state='completed',organization_state='completed' WHERE operation_id=$1`, own[0])
	if f.activity(t, "?limit=1", f.member, "").Items[0].OperationID == own[0] {
		t.Fatal("terminal operation retained as active")
	}
	t.Log("M6_DASHBOARD_AUTOTV_NOT_COVERED: valid public automatic-TV fixture requires App.Run/transfers; independent store acceptance covers verified_bytes")
}

func TestM6DashboardAuthorizationAndFaultHTTPPG(t *testing.T) {
	f := newM6DashboardFixture(t)
	f.file(t, f.root, f.unit, "imported", f.at.Add(-time.Hour))
	own := f.operation(t, f.memberID, "tracking", "pending", f.at.Add(-time.Hour))
	hidden := f.operation(t, f.adminID, "tracking", "pending", f.at)
	for _, endpoint := range []struct {
		path, query string
		max         int
	}{{m6Overview, "recent_limit", 24}, {m6Activity, "limit", 12}} {
		m6Status(t, f.request(t, http.MethodGet, endpoint.path, nil, "", ""), 401)
		for _, invalid := range []string{"0", "-1", fmt.Sprint(endpoint.max + 1), "9999999999999999999999999999999999999", "1.5", "nope"} {
			m6Status(t, f.request(t, http.MethodGet, endpoint.path+"?"+endpoint.query+"="+invalid, nil, f.member, ""), 400)
		}
	}
	// Issue a genuine read token over Cookie HTTP; changing scopes must take effect.
	r := f.request(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "dashboard-read"}, f.member, "")
	m6Status(t, r, 200)
	var issued struct {
		Token string `json:"secret"`
	}
	m6Decode(t, r, &issued)
	if issued.Token == "" {
		t.Fatal("public token issuance omitted secret")
	}
	if page := f.activity(t, "?limit=12", "", issued.Token); len(page.Items) != 1 || page.More || page.Items[0].OperationID != own {
		t.Fatal("member read token expanded owner scope")
	}
	m6Status(t, f.request(t, http.MethodGet, m6Overview, nil, "", issued.Token), 200)
	for _, scopes := range []string{`[]`, `["write"]`, `["admin","system","*"]`} {
		f.exec(t, `UPDATE api_token SET scopes=$2::jsonb WHERE token_hash=$1`, crypto.HashToken(issued.Token), scopes)
		for _, path := range []string{m6Overview, m6Activity} {
			m6Status(t, f.request(t, http.MethodGet, path, nil, "", issued.Token), 403)
		}
	}
	f.exec(t, `UPDATE api_token SET scopes='["read","admin","system"]' WHERE token_hash=$1`, crypto.HashToken(issued.Token))
	if page := f.activity(t, "", "", issued.Token); len(page.Items) != 1 || page.Items[0].OperationID != own {
		t.Fatal("role-named token scopes elevated member visibility")
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.adminID)
	if page := f.activity(t, "", f.admin, ""); len(page.Items) != 1 || page.Items[0].OperationID != hidden {
		t.Fatal("existing admin Cookie retained authority after downgrade")
	}
	m6Status(t, f.request(t, http.MethodGet, m6Overview, nil, f.admin, ""), 200)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.memberID)
	for _, path := range []string{m6Overview, m6Activity} {
		m6Status(t, f.request(t, http.MethodGet, path, nil, f.member, ""), 401)
		m6Status(t, f.request(t, http.MethodGet, path, nil, "", issued.Token), 401)
	}
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, f.memberID)
	for _, tc := range []struct{ table, path string }{{"library_file", m6Overview}, {"download_task", m6Activity}} {
		t.Run("fault-"+tc.table, func(t *testing.T) {
			f.exec(t, "ALTER TABLE "+tc.table+" RENAME TO m6_fault_"+tc.table)
			restored := false
			defer func() {
				if !restored {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if _, err := f.app.DBPool().Exec(ctx, "ALTER TABLE m6_fault_"+tc.table+" RENAME TO "+tc.table); err != nil {
						t.Error("restore owned table", err)
					}
				}
			}()
			m6Status(t, f.request(t, http.MethodGet, tc.path, nil, f.member, ""), 503)
			f.exec(t, "ALTER TABLE m6_fault_"+tc.table+" RENAME TO "+tc.table)
			restored = true
			m6Status(t, f.request(t, http.MethodGet, tc.path, nil, f.member, ""), 200)
		})
	}
	if f.overview(t, "").Counts.Files != "1" || len(f.activity(t, "", f.member, "").Items) != 1 {
		t.Fatal("restored reads returned false empty success")
	}
}
