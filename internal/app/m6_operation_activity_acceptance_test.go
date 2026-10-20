package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

const m6OperationBase = "/api/v1/operations"
const m6OperationPassword = "Owned-M6-Operation-123!"

// SQL prepares UI/read fixtures, not acquisition or completed files. The shared
// fixture establishes static media/root data; App.Run and workers never start.
type m6OperationFixture struct {
	f                  *organizationFixture
	router             http.Handler
	admin, member      *http.Cookie
	adminID, memberID  uuid.UUID
	read, write, empty string
	calls              atomic.Int32
	rate               atomic.Int64
	failed             atomic.Bool
	upstream           *httptest.Server
	logs               bytes.Buffer
}

func newM6OperationFixture(t *testing.T) *m6OperationFixture {
	t.Helper()
	if !strings.HasPrefix(filepath.Clean(os.TempDir()), "/data/temp/") || os.Getenv("TMP") != os.TempDir() || os.Getenv("TEMP") != os.TempDir() {
		t.Fatal("owned TMPDIR/TMP/TEMP under /data/temp required")
	}
	f := organizationSetup(t)
	var version int
	if err := f.pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version != 180006 {
		t.Fatal("real PostgreSQL 18.6 required", err)
	}
	h := &m6OperationFixture{f: f, read: "owned-operation-read", write: "owned-operation-write", empty: "owned-operation-empty"}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(&h.logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
		h.redacted(t, h.logs.String())
	})
	service := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	admin, _, err := service.SetupAdmin(t.Context(), "operation-admin", m6OperationPassword)
	if err != nil {
		t.Fatal(err)
	}
	h.adminID, h.memberID = admin.ID, uuid.New()
	h.exec(t, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'operation-member',password_hash,'member',true FROM app_user WHERE id=$2`, h.memberID, h.adminID)
	h.exec(t, `UPDATE download_requirement SET actor_id=$2 WHERE operation_id=$1`, f.op, h.memberID.String())
	for _, token := range []struct{ raw, scopes string }{{h.read, `["read"]`}, {h.write, `["write"]`}, {h.empty, `[]`}} {
		h.exec(t, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'operation acceptance',$3,$4::jsonb)`, uuid.New(), h.memberID, crypto.HashToken(token.raw), token.scopes)
	}
	handler := httpapi.NewServerHandler(a, service, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.router = httpapi.NewRouter(a, handler, service, nil)
	login := func(name string) *http.Cookie {
		body, err := json.Marshal(map[string]string{"username": name, "password": m6OperationPassword})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body)).WithContext(t.Context())
		req.Header.Set("Content-Type", "application/json")
		req.Host, req.RemoteAddr = "localhost", "127.0.0.1:1234"
		reply := httptest.NewRecorder()
		h.router.ServeHTTP(reply, req)
		result := reply.Result()
		defer func() { _ = result.Body.Close() }()
		for _, cookie := range result.Cookies() {
			if reply.Code == 200 && cookie.Name == "pulsemedia_session" && cookie.Value != "" && cookie.HttpOnly && cookie.SameSite == http.SameSiteStrictMode {
				return cookie
			}
		}
		t.Fatal("real auth login did not issue session Cookie", reply.Code)
		return nil
	}
	h.admin, h.member = login("operation-admin"), login("operation-member")
	h.exec(t, `UPDATE download_task SET info_hash=$2 WHERE id=$1`, f.taskID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.calls.Add(1)
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.Form.Get("username") != "operation-native-user" || r.Form.Get("password") != "operation-native-password" {
				t.Error("native factory did not decrypt current configured credentials")
			}
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			if r.Method != http.MethodGet || r.URL.Query().Get("hashes") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Error("unexpected native observation request")
			}
			if h.failed.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "operation-private-error")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","save_path":"/remote/downloads","content_path":"/remote/downloads/movie.mkv","progress":0.25,"size":100,"downloaded":25,"state":"downloading","dlspeed":%d}]`, h.rate.Load())
		default:
			t.Errorf("unexpected native endpoint %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(h.upstream.Close)
	cipher, err := crypto.Encrypt(make([]byte, 32), []byte("operation-native-password"))
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `UPDATE download_client SET url=$2,username='operation-native-user',password_encrypted=$3 WHERE id=$1`, f.client, h.upstream.URL, cipher)
	return h
}

func (h *m6OperationFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (h *m6OperationFixture) redacted(t *testing.T, body string) {
	t.Helper()
	for _, private := range []string{"/remote/downloads", h.f.source, h.f.target, "operation-native-password", "operation-native-user", "operation-private-error", "operation-private-payload", "operation-private-cipher", "postgres://", "http://", "https://", "source_path", "target_path", "save_path", "content_path", "password_encrypted", "client_stamp", "task_stamp"} {
		if strings.Contains(body, private) {
			t.Fatalf("private storage/native data exposed: %q", private)
		}
	}
}

// Only GETs are inside these snapshots. Fixture transitions, observations and
// fault injection are deliberately outside; full row hashes also detect updates.
func (h *m6OperationFixture) get(t *testing.T, path string, cookie *http.Cookie, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	before, err := m6DashboardSnapshot(t.Context(), h.f)
	if err != nil {
		t.Fatal(err)
	}
	calls := h.calls.Load()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(t.Context())
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	reply := httptest.NewRecorder()
	h.router.ServeHTTP(reply, req)
	after, err := m6DashboardSnapshot(t.Context(), h.f)
	if err != nil || !reflect.DeepEqual(before, after) || h.calls.Load() != calls {
		t.Fatal("GET changed business rows/schema or performed native I/O", err)
	}
	h.redacted(t, reply.Body.String())
	if reply.Code != want {
		t.Fatalf("GET %s: status=%d want=%d body=%s", path, reply.Code, want, reply.Body.String())
	}
	if want >= 400 {
		var problem struct{ Code, Message, RequestID string }
		if json.Unmarshal(reply.Body.Bytes(), &problem) != nil || problem.Code == "" || problem.Message == "" {
			t.Fatal("missing sanitized public error contract")
		}
	}
	return reply
}

func m6OperationDecode[T any](t *testing.T, r *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *m6OperationFixture) telemetry(t *testing.T) *httpapi.OperationTelemetry {
	t.Helper()
	detail := m6OperationDecode[httpapi.OperationDetails](t, h.get(t, m6OperationBase+"/"+h.f.op.String(), h.member, "", 200))
	page := m6OperationDecode[httpapi.OperationPage](t, h.get(t, m6OperationBase+"?view=all", h.member, "", 200))
	if len(page.Items) != 1 || page.Items[0].OperationId != h.f.op || detail.OperationId != h.f.op || detail.Status != page.Items[0].Status || detail.Telemetry == nil || !reflect.DeepEqual(detail.Telemetry, page.Items[0].Telemetry) {
		t.Fatal("public list/detail disagree on persisted telemetry")
	}
	return detail.Telemetry
}

func (h *m6OperationFixture) operation(t *testing.T, owner uuid.UUID) uuid.UUID {
	t.Helper()
	op := uuid.New()
	h.exec(t, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id) VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), op, h.f.unit, h.f.root, owner.String())
	return op
}

func (h *m6OperationFixture) plan(t *testing.T, op uuid.UUID, steps int) uuid.UUID {
	t.Helper()
	plan := uuid.New()
	h.exec(t, `INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy) VALUES($1,$2,$3,'created','hardlink')`, plan, op, h.f.root)
	for i := 0; i < steps; i++ {
		h.exec(t, `INSERT INTO transfer_step(id,plan_id,unit_id,source_path,target_path,source_rel,target_rel,action,state,step_order,source_size,source_mtime,error) VALUES($1,$2,$3,'/operation-private-payload/source','/operation-private-payload/target','private/source.mkv','private/movie.mkv','copy','pending',$4,123,456,'operation-private-error')`, uuid.New(), plan, h.f.unit, i)
	}
	return plan
}

func (h *m6OperationFixture) timeline(t *testing.T, op uuid.UUID, query string) httpapi.OperationTimelinePage {
	t.Helper()
	return m6OperationDecode[httpapi.OperationTimelinePage](t, h.get(t, m6OperationBase+"/"+op.String()+"/timeline"+query, h.member, "", 200))
}

func TestM6OperationNativeTelemetryHTTPPG(t *testing.T) {
	h := newM6OperationFixture(t)
	repo := downloadstore.NewRepositoryWithDownloadRates(h.f.pool, h.f.gen, make([]byte, 32))
	repo.SetClientFactory(NativeDownloadClient)
	for _, rate := range []int64{9223372036854775807, 0} {
		h.rate.Store(rate)
		calls := h.calls.Load()
		if task, err := repo.ObserveTask(t.Context(), h.f.op); err != nil || task == nil || task.State != "tracking" {
			t.Fatal("real native ordinary observation", err)
		}
		if h.calls.Load() != calls+2 {
			t.Fatal("expected one native login and one observation; no Add/files")
		}
		got := h.telemetry(t)
		var taskAt, rateAt time.Time
		var persistedRate string
		if err := h.f.pool.QueryRow(t.Context(), `SELECT t.updated_at,r.observed_at,r.download_rate_bytes_per_second::text FROM download_task t JOIN download_rate_observation r ON r.task_id=t.id WHERE t.id=$1 AND r.source='ordinary'`, h.f.taskID).Scan(&taskAt, &rateAt, &persistedRate); err != nil {
			t.Fatal(err)
		}
		if got.Progress == nil || *got.Progress != 0.25 || got.ProgressSource == nil || *got.ProgressSource != "native_percent" || got.ProgressObservedAt == nil || !got.ProgressObservedAt.Equal(taskAt) || got.RateStatus != "current" || got.DownloadRateBytesPerSecond == nil || *got.DownloadRateBytesPerSecond != fmt.Sprint(rate) || persistedRate != fmt.Sprint(rate) || got.RateObservedAt == nil || !got.RateObservedAt.Equal(rateAt) {
			t.Fatalf("native exact rate/progress/time lost: %+v", got)
		}
	}
	// Controlled aging affects only the display sample, before the next GET.
	h.exec(t, `UPDATE download_rate_observation SET observed_at=clock_timestamp()-interval '2 minutes' WHERE task_id=$1`, h.f.taskID)
	if got := h.telemetry(t); got.RateStatus != "stale" || got.DownloadRateBytesPerSecond == nil || *got.DownloadRateBytesPerSecond != "0" || got.RateObservedAt == nil {
		t.Fatal("stale zero must remain an exact stale sample")
	}
	h.failed.Store(true)
	if _, err := repo.ObserveTask(t.Context(), h.f.op); err == nil {
		t.Fatal("native failure unexpectedly succeeded")
	}
	if got := h.telemetry(t); got.RateStatus != "unknown" || got.DownloadRateBytesPerSecond != nil || got.RateObservedAt == nil || got.Progress == nil || *got.Progress != 0.25 {
		t.Fatal("latest failed callback revived old speed or discarded valid task progress")
	}
	h.exec(t, `UPDATE download_client SET username='changed-current-config' WHERE id=$1`, h.f.client)
	if got := h.telemetry(t); got.RateStatus != "unknown" || got.DownloadRateBytesPerSecond != nil || got.RateObservedAt != nil {
		t.Fatal("changed current source reused old sample")
	}
	t.Log("M6_OPERATION_AUTOTV_NOT_COVERED: legitimate execution fixture is postgres-private; independent publisher acceptance owns automatic-TV/complete-TV/upgrade positives")
}

func TestM6OperationTimelineHTTPPG(t *testing.T) {
	h := newM6OperationFixture(t)
	if page := h.timeline(t, h.f.op, ""); page.Items == nil || len(page.Items) != 0 || page.PlanId != nil || page.NextCursor != nil || page.ObservedAt.IsZero() {
		t.Fatal("no-plan timeline must be honest non-null empty page")
	}
	plan := h.plan(t, h.f.op, 107)
	h.exec(t, `UPDATE download_task SET transfer_plan_id=$2 WHERE operation_id=$1`, h.f.op, plan)
	retired := h.plan(t, h.f.op, 1)
	for _, query := range []string{"", "?limit=1", "?limit=100"} {
		page := h.timeline(t, h.f.op, query)
		want := 50
		switch query {
		case "?limit=1":
			want = 1
		case "?limit=100":
			want = 100
		}
		if len(page.Items) != want || page.PlanId == nil || *page.PlanId != plan || page.NextCursor == nil || page.PlanStatus == nil || *page.PlanStatus != "created" {
			t.Fatal("default/bounded page or current-plan preference failed")
		}
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for pageNo := 0; ; pageNo++ {
		if pageNo > 2 {
			t.Fatal("unbounded or repeated timeline page")
		}
		page := h.timeline(t, h.f.op, "?limit=100&cursor="+url.QueryEscape(cursor))
		for _, step := range page.Items {
			if seen[step.StepId] || step.Order != int32(len(seen)) || step.FileName != "movie.mkv" || step.Action != "copy" || step.State != "pending" || step.UpdatedAt.IsZero() || step.FinishedAt != nil {
				t.Fatal("timeline lost order, duplicated step, leaked path or invented completion")
			}
			seen[step.StepId] = true
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == "" || *page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 107 {
		t.Fatal("timeline did not enumerate all persisted steps", len(seen))
	}
	first := h.timeline(t, h.f.op, "?limit=1")
	base := m6OperationBase + "/" + h.f.op.String() + "/timeline"
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=nope", "?cursor=invalid", "?cursor=" + strings.Repeat("a", 1025)} {
		h.get(t, base+query, h.member, "", 400)
	}
	other := h.operation(t, h.memberID)
	h.get(t, m6OperationBase+"/"+other.String()+"/timeline?cursor="+url.QueryEscape(*first.NextCursor), h.member, "", 400)
	h.get(t, base+"?cursor="+url.QueryEscape(*first.NextCursor), h.admin, "", 400)
	h.exec(t, `UPDATE download_task SET transfer_plan_id=$2 WHERE operation_id=$1`, h.f.op, retired)
	h.get(t, base+"?cursor="+url.QueryEscape(*first.NextCursor), h.member, "", 400)
	// Unsafe frozen relative labels are suppressed, never opened as files.
	for _, label := range []string{"/secret/root/movie.mkv", "../escape.mkv", "https://private.invalid/file?password=secret", "folder\\secret.mkv", "folder/line\nsecret.mkv"} {
		h.exec(t, `UPDATE transfer_step SET target_rel=$2 WHERE plan_id=$1`, retired, label)
		if page := h.timeline(t, h.f.op, ""); len(page.Items) != 1 || page.Items[0].FileName != "" {
			t.Fatal("unsafe file label exposed")
		}
	}
	h.exec(t, `UPDATE transfer_step SET target_rel=NULL,source_rel='nested/fallback.mkv' WHERE plan_id=$1`, retired)
	if page := h.timeline(t, h.f.op, ""); page.Items[0].FileName != "fallback.mkv" {
		t.Fatal("safe source basename fallback lost")
	}
}

func TestM6OperationAuthorizationHTTPPG(t *testing.T) {
	h := newM6OperationFixture(t)
	hidden := h.operation(t, h.adminID)
	paths := []string{m6OperationBase + "?view=all", m6OperationBase + "/" + h.f.op.String(), m6OperationBase + "/" + h.f.op.String() + "/timeline"}
	for _, path := range paths {
		h.get(t, path, nil, "", 401)
		h.get(t, path, h.member, "", 200)
		h.get(t, path, nil, h.read, 200)
		for _, token := range []string{h.write, h.empty} {
			h.get(t, path, nil, token, 403)
		}
	}
	page := m6OperationDecode[httpapi.OperationPage](t, h.get(t, paths[0], nil, h.read, 200))
	if len(page.Items) != 1 || page.Items[0].OperationId != h.f.op {
		t.Fatal("read token broadened object scope")
	}
	for _, suffix := range []string{"", "/timeline"} {
		h.get(t, m6OperationBase+"/"+hidden.String()+suffix, h.member, "", 403)
		h.get(t, m6OperationBase+"/"+hidden.String()+suffix, h.admin, "", 200)
		h.get(t, m6OperationBase+"/"+uuid.NewString()+suffix, h.member, "", 404)
	}
	for _, query := range []string{"?limit=0", "?limit=51", "?limit=101", "?cursor=invalid", "?view=invalid"} {
		h.get(t, m6OperationBase+query, h.member, "", 400)
	}
	list := m6OperationDecode[httpapi.OperationPage](t, h.get(t, m6OperationBase+"?view=all&limit=1", h.admin, "", 200))
	if list.NextCursor == nil {
		t.Fatal("admin list omitted next cursor")
	}
	h.get(t, m6OperationBase+"?view=all&limit=1&cursor="+url.QueryEscape(*list.NextCursor), h.member, "", 400)
	h.get(t, m6OperationBase+"?view=active&cursor="+url.QueryEscape(*list.NextCursor), h.admin, "", 400)
	h.plan(t, h.f.op, 2)
	timeline := h.timeline(t, h.f.op, "?limit=1")
	h.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, h.memberID)
	h.get(t, paths[2]+"?cursor="+url.QueryEscape(*timeline.NextCursor), h.member, "", 400)
	h.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, h.memberID)
	h.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, h.adminID)
	h.get(t, m6OperationBase+"/"+h.f.op.String(), h.admin, "", 403)
	h.get(t, m6OperationBase+"/"+h.f.op.String()+"/timeline", h.admin, "", 403)
	h.exec(t, `UPDATE api_token SET scopes='["read","admin","system"]' WHERE token_hash=$1`, crypto.HashToken(h.read))
	h.get(t, m6OperationBase+"/"+hidden.String(), nil, h.read, 403)
	h.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, h.memberID)
	for _, path := range paths {
		h.get(t, path, h.member, "", 401)
		h.get(t, path, nil, h.read, 401)
	}
}

func TestM6OperationStorageFaultHTTPPG(t *testing.T) {
	h := newM6OperationFixture(t)
	h.plan(t, h.f.op, 1)
	for _, fault := range []struct{ table, path string }{
		{"download_rate_observation", m6OperationBase + "?view=all"},
		{"download_rate_observation", m6OperationBase + "/" + h.f.op.String()},
		{"transfer_step", m6OperationBase + "/" + h.f.op.String() + "/timeline"},
	} {
		t.Run(fault.table+fault.path, func(t *testing.T) {
			// This DB belongs to pgtest.NewTestDB. Always restore even on Fatal.
			h.exec(t, "ALTER TABLE "+fault.table+" RENAME TO m6_operation_fault_table")
			restored := false
			defer func() {
				if !restored {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if _, err := h.f.pool.Exec(ctx, "ALTER TABLE m6_operation_fault_table RENAME TO "+fault.table); err != nil {
						t.Error("restore owned read table", err)
					}
				}
			}()
			h.get(t, fault.path, h.member, "", 503)
			h.exec(t, "ALTER TABLE m6_operation_fault_table RENAME TO "+fault.table)
			restored = true
			h.get(t, fault.path, h.member, "", 200)
		})
	}
}
