package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

func organizationPublicRouter(f *completeOrganizationFixture, configured bool) http.Handler {
	a := f.n.f.a
	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	var svc *transfer.Service
	if configured {
		svc = f.worker
	}
	h := httpapi.NewServerHandler(a, authSvc, nil, nil, svc, nil, nil, nil, nil, nil, nil, nil, nil)
	return httpapi.NewRouter(a, h, authSvc, nil)
}
func newOrganizationPublicWorker(a *app.App) *transfer.Service {
	gen := idgen.New()
	return transfer.NewService(transferstore.NewRepository(a.DBPool(), gen, outbox.New(a.DBPool(), gen, clock.New()), rootfs.NewFilesystem()))
}
func organizationPublicBody(f *completeOrganizationFixture) string {
	raw, _ := json.Marshal(map[string]any{"operation_id": f.command.OperationID, "tracking_job_id": f.command.TrackingJobID})
	return string(raw)
}
func organizationPublicCall(h http.Handler, f *completeOrganizationFixture, body, key, cookie string, edit func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/download-tasks/"+f.command.TaskID.String()+"/complete-tv-organizations", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+r.Host)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
	}
	if edit != nil {
		edit(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func organizationPublicReceipt(t *testing.T, w *httptest.ResponseRecorder) ports.CompleteTVOrganizationReceipt {
	t.Helper()
	var r ports.CompleteTVOrganizationReceipt
	if w.Code != 202 || w.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.TaskID == uuid.Nil || r.PlanID == uuid.Nil || r.JobID == uuid.Nil {
		t.Fatalf("receipt %d %s", w.Code, w.Body.String())
	}
	return r
}
func organizationPublicCounts(t *testing.T, f *completeOrganizationFixture) [10]int {
	t.Helper()
	var n [10]int
	err := f.n.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM transfer_plan),(SELECT count(*) FROM transfer_step),(SELECT count(*) FROM complete_tv_transfer_evidence),(SELECT count(*) FROM download_file_inventory),(SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'complete-tv.organize:%'),(SELECT count(*) FROM audit_log WHERE action='complete_tv.organization.confirm'),(SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.organization.confirmed'),(SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM download_task WHERE transfer_plan_id IS NOT NULL)`).Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6], &n[7], &n[8], &n[9])
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestCompleteTVOrganizationPublicPGStrictCookie(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPublicRouter(f, true)
	raw := organizationPublicBody(f)
	session := f.n.f.admin
	cases := []struct {
		name, body, key, cookie string
		edit                    func(*http.Request)
		want                    int
	}{
		{"anonymous", raw, "anon", "", nil, 401}, {"member", raw, "member", f.n.f.member, nil, 403},
		{"csrf", raw, "csrf", session, func(r *http.Request) { r.Header.Set("Origin", "http://cross.invalid") }, 403},
		{"missing-origin", raw, "origin", session, func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"no-key", raw, "", session, nil, 400}, {"comma-key", raw, "a,b", session, nil, 400}, {"long-key", raw, strings.Repeat("x", 121), session, nil, 400},
		{"duplicate-key", raw, "dup", session, func(r *http.Request) { r.Header.Add("Idempotency-Key", "other") }, 400},
		{"duplicate-content-type", raw, "type", session, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, 400},
		{"duplicate-cookie", raw, "cookie", session, func(r *http.Request) { r.Header.Add("Cookie", r.Header.Get("Cookie")) }, 400},
		{"duplicate-origin", raw, "origins", session, func(r *http.Request) { r.Header.Add("Origin", r.Header.Get("Origin")) }, 403},
		{"content-type", raw, "ct", session, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 400},
		{"path-uuid", raw, "path", session, func(r *http.Request) {
			r.URL.Path = strings.Replace(r.URL.Path, f.command.TaskID.String(), "malformed", 1)
		}, 400},
	}
	for _, body := range []string{"", "null", "[]", "{}", raw + " {}", raw + strings.Repeat(" ", 4097), strings.Replace(raw, f.command.OperationID.String(), "bad-uuid", 1), strings.Replace(raw, f.command.OperationID.String(), uuid.Nil.String(), 1), strings.Replace(raw, f.command.OperationID.String(), strings.ToUpper(f.command.OperationID.String()), 1)} {
		cases = append(cases, struct {
			name, body, key, cookie string
			edit                    func(*http.Request)
			want                    int
		}{"invalid-body", body, "wire", session, nil, 400})
	}
	for _, field := range []string{"actor", "task_id", "material", "inventory", "baseline", "expected_evidence_stamp", "confirmed", "Operation_id", "execution_allowed"} {
		cases = append(cases, struct {
			name, body, key, cookie string
			edit                    func(*http.Request)
			want                    int
		}{"unknown-" + field, raw[:len(raw)-1] + `,"` + field + `":{"x":1,"x":2}}`, "unknown", session, nil, 400})
	}
	for _, field := range []string{"operation_id", "tracking_job_id"} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &fields)
		for _, value := range []string{"null", `{"id":1,"id":2}`, `[]`, `1`} {
			fields[field] = json.RawMessage(value)
			b, _ := json.Marshal(fields)
			cases = append(cases, struct {
				name, body, key, cookie string
				edit                    func(*http.Request)
				want                    int
			}{"scalar-" + field, string(b), "scalar", session, nil, 400})
		}
		cases = append(cases, struct {
			name, body, key, cookie string
			edit                    func(*http.Request)
			want                    int
		}{"duplicate-" + field, raw[:len(raw)-1] + `,"` + field + `":"` + uuid.NewString() + `"}`, "dup-field", session, nil, 400})
	}
	cases = append(cases, struct {
		name, body, key, cookie string
		edit                    func(*http.Request)
		want                    int
	}{"escaped-duplicate", raw[:len(raw)-1] + `,"operation_\u0069d":"` + f.command.OperationID.String() + `"}`, "escape", session, nil, 400})
	before := organizationPublicCounts(t, f)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			completePublicProblem(t, organizationPublicCall(h, f, tc.body, tc.key, tc.cookie, tc.edit), tc.want)
			if organizationPublicCounts(t, f) != before {
				t.Fatal("rejection wrote partial state")
			}
		})
	}
	completePublicProblem(t, organizationPublicCall(organizationPublicRouter(f, false), f, raw, "unconfigured", session, nil), 503)
}

func TestCompleteTVOrganizationPublicPGConcurrentReceiptAndHistory(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPublicRouter(f, true)
	raw := organizationPublicBody(f)
	session := f.n.f.admin
	var results [8]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = organizationPublicCall(h, f, raw, "lost", session, nil) }(i)
	}
	wg.Wait()
	first := organizationPublicReceipt(t, results[0])
	if first.State != "queued" {
		t.Fatal(first)
	}
	for _, w := range results {
		r := organizationPublicReceipt(t, w)
		if r != first || !bytes.Equal(w.Body.Bytes(), results[0].Body.Bytes()) {
			t.Fatal("split receipt")
		}
	}
	if got := organizationPublicCounts(t, f); got != [10]int{1, 2, 1, 1, 1, 1, 1, 1, 0, 1} {
		t.Fatal("non-atomic", got)
	}
	for _, field := range []string{"operation_id", "tracking_job_id", "task_id"} {
		t.Run("intent-"+field, func(t *testing.T) {
			body := raw
			var edit func(*http.Request)
			if field == "task_id" {
				edit = func(r *http.Request) {
					r.URL.Path = strings.Replace(r.URL.Path, f.command.TaskID.String(), uuid.NewString(), 1)
				}
			} else {
				var fields map[string]any
				_ = json.Unmarshal([]byte(raw), &fields)
				fields[field] = uuid.NewString()
				b, _ := json.Marshal(fields)
				body = string(b)
			}
			completePublicProblem(t, organizationPublicCall(h, f, body, "lost", session, edit), 409)
		})
	}
	if err := f.run(t, &first); err != nil {
		t.Fatal(err)
	}
	f.assertImported(t, &first)
	f.n.exec(t, `UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1`, f.n.candidate)
	w := organizationPublicCall(h, f, raw, "lost", session, nil)
	if organizationPublicReceipt(t, w) != first {
		t.Fatal("historical state rewritten")
	}
	completePublicProblem(t, organizationPublicCall(h, f, raw, "new", session, nil), 409)
	cfg := f.n.f.a.Config()
	if err := f.n.f.a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	f.n.f.a = restarted
	// The repository and explicit service are reconstructed, not retained across restart.
	f.worker = newOrganizationPublicWorker(restarted)
	replay := organizationPublicCall(organizationPublicRouter(f, true), f, raw, "lost", session, nil)
	if organizationPublicReceipt(t, replay) != first || !bytes.Equal(replay.Body.Bytes(), results[0].Body.Bytes()) {
		t.Fatal("restart receipt changed")
	}
}

func TestCompleteTVOrganizationPublicPGCurrentAccountAndOwnership(t *testing.T) {
	for _, mode := range []string{"disabled", "demoted", "owner", "revoked-session"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			h := organizationPublicRouter(f, true)
			raw := organizationPublicBody(f)
			session := f.n.f.admin
			organizationPublicReceipt(t, organizationPublicCall(h, f, raw, "cached", session, nil))
			before := organizationPublicCounts(t, f)
			switch mode {
			case "disabled", "demoted":
				tx, err := f.n.f.a.DBPool().Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				sql := `UPDATE app_user SET is_active=false WHERE id=$1`
				if mode == "demoted" {
					sql = `UPDATE app_user SET role='member' WHERE id=$1`
				}
				if _, err = tx.Exec(context.Background(), sql, f.n.actor.ID); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- organizationPublicCall(h, f, raw, "cached", session, nil) }()
				waitCompleteTVLock(t, f.n.completeTVFixture, "app_user")
				if err = tx.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
				completePublicProblem(t, <-done, 403)
			case "owner":
				other := uuid.New()
				f.n.exec(t, `INSERT INTO app_user(id,username,password_hash,role) SELECT $1::uuid,$1::uuid::text,password_hash,'admin' FROM app_user WHERE id=$2`, other, f.n.actor.ID)
				f.n.exec(t, `UPDATE subscription SET user_id=$1 WHERE id=$2`, other, f.n.f.sub.Id)
				completePublicProblem(t, organizationPublicCall(h, f, raw, "cached", session, nil), 403)
			case "revoked-session":
				f.n.exec(t, `DELETE FROM app_session WHERE token_hash=$1`, crypto.HashToken(session))
				completePublicProblem(t, organizationPublicCall(h, f, raw, "cached", session, nil), 401)
			}
			if organizationPublicCounts(t, f) != before {
				t.Fatal("revocation wrote state")
			}
		})
	}
}

func TestCompleteTVOrganizationPublicPGTokenBeforeBodyAndReceipt(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPublicRouter(f, true)
	session := f.n.f.admin
	organizationPublicReceipt(t, organizationPublicCall(h, f, organizationPublicBody(f), "cached", session, nil))
	svc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.n.f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, f.n.f.a.Clock())
	_, secret, err := svc.Create(context.Background(), f.n.actor, ports.TokenCommand{Name: "organization read"}, "token")
	if err != nil {
		t.Fatal(err)
	}
	before := organizationPublicCounts(t, f)
	// A broken receipt table proves that token rejection happens before receipt access.
	f.n.exec(t, `ALTER TABLE idempotency_record RENAME TO private_organization_receipts`)
	defer f.n.exec(t, `ALTER TABLE private_organization_receipts RENAME TO idempotency_record`)
	for _, body := range []string{organizationPublicBody(f), `{"operation_id":{"x":1,"x":2}}`} {
		completePublicProblem(t, organizationPublicCall(h, f, body, "cached", "", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+secret)
			r.Body = organizationPanicBody{}
		}), 403)
	}
	f.n.exec(t, `ALTER TABLE private_organization_receipts RENAME TO idempotency_record`)
	if organizationPublicCounts(t, f) != before {
		t.Fatal("token accessed command")
	}
	f.n.exec(t, `ALTER TABLE idempotency_record RENAME TO private_organization_receipts`)
}

type organizationPanicBody struct{}

func (organizationPanicBody) Read([]byte) (int, error) { panic("token body was read") }
func (organizationPanicBody) Close() error             { return nil }

func TestCompleteTVOrganizationPublicPGRollbackAndSafeUnavailable(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPublicRouter(f, true)
	raw := organizationPublicBody(f)
	session := f.n.f.admin
	before := organizationPublicCounts(t, f)
	for _, table := range []string{"job", "audit_log", "outbox_event", "idempotency_record"} {
		t.Run("tail-"+table, func(t *testing.T) {
			f.n.exec(t, `CREATE FUNCTION reject_public_organization() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'private receipt tail DSN credential';END$$`)
			f.n.exec(t, "CREATE TRIGGER reject_public_organization BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_public_organization()")
			w := organizationPublicCall(h, f, raw, "rollback", session, nil)
			completePublicProblem(t, w, 503)
			if strings.Contains(w.Body.String(), "credential") {
				t.Fatal("raw DB error leaked")
			}
			f.n.exec(t, "DROP TRIGGER reject_public_organization ON "+table)
			f.n.exec(t, `DROP FUNCTION reject_public_organization()`)
			if organizationPublicCounts(t, f) != before {
				t.Fatal("tail left partial state")
			}
		})
	}
	t.Run("policy-lock-timeout", func(t *testing.T) {
		tx, err := f.n.f.a.DBPool().Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err = tx.Exec(context.Background(), `SELECT p.id FROM policy p JOIN subscription s ON s.quality_policy_id=p.id WHERE s.id=$1 FOR UPDATE OF p`, f.n.f.sub.Id); err != nil {
			t.Fatal(err)
		}
		completePublicProblem(t, organizationPublicCall(h, f, raw, "locked", session, nil), 503)
		if organizationPublicCounts(t, f) != before {
			t.Fatal("lock failure left state")
		}
	})
	t.Run("database-table-unavailable", func(t *testing.T) {
		f.n.exec(t, `ALTER TABLE complete_tv_execution_authority RENAME TO private_organization_authority`)
		w := organizationPublicCall(h, f, raw, "unavailable", session, nil)
		f.n.exec(t, `ALTER TABLE private_organization_authority RENAME TO complete_tv_execution_authority`)
		completePublicProblem(t, w, 503)
		if strings.Contains(w.Body.String(), "private_organization") {
			t.Fatal("SQL leaked")
		}
		if organizationPublicCounts(t, f) != before {
			t.Fatal("outage left state")
		}
	})
	t.Run("closed-command-database", func(t *testing.T) {
		a := f.n.f.a
		pool, err := postgres.NewPool(context.Background(), a.Config().Database.URL, postgres.DefaultPoolConfig())
		if err != nil {
			t.Fatal(err)
		}
		pool.Close()
		gen := idgen.New()
		svc := transfer.NewService(transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem()))
		authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
		handler := httpapi.NewServerHandler(a, authSvc, nil, nil, svc, nil, nil, nil, nil, nil, nil, nil, nil)
		router := httpapi.NewRouter(a, handler, authSvc, nil)
		w := organizationPublicCall(router, f, raw, "db-outage", session, nil)
		completePublicProblem(t, w, 503)
		if strings.Contains(w.Body.String(), "closed") || strings.Contains(w.Body.String(), "postgres") {
			t.Fatal("database error leaked")
		}
		if organizationPublicCounts(t, f) != before {
			t.Fatal("database outage wrote partial state")
		}
	})
	t.Run("canceled-repository", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := f.worker.ConfirmCompleteTVOrganization(ctx, f.n.actor, ports.CompleteTVOrganizationReferences{TaskID: f.command.TaskID, OperationID: f.command.OperationID, TrackingJobID: f.command.TrackingJobID}, "canceled")
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	organizationPublicReceipt(t, organizationPublicCall(h, f, raw, "rollback", session, nil))
}

func TestCompleteTVOrganizationPublicPGAppWorkerAndRestartReceipt(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	raw := organizationPublicBody(f)
	session := f.n.f.admin
	cfg := *f.n.f.a.Config()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	_ = listener.Close()
	// End only fixture-owned running observation leases; completed tracking evidence remains original.
	f.n.exec(t, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE state='running'`)
	if err = f.n.f.a.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 4 * time.Second}
	base := "http://" + cfg.Server.HTTPAddr
	start := func() func() {
		a, err := app.New(&cfg, app.WithLogger(logging.NewLogger(io.Discard, "error")))
		if err != nil {
			t.Fatal(err)
		}
		f.n.f.a = a
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(35 * time.Second):
					t.Error("App failed to drain")
				}
				_ = a.Close()
			})
		}
		t.Cleanup(stop)
		runtimeTVWait(t, func() bool {
			select {
			case err := <-done:
				t.Fatalf("App exited before health: %v", err)
			default:
			}
			r, err := client.Get(base + "/healthz")
			if err != nil {
				return false
			}
			_ = r.Body.Close()
			return r.StatusCode == 200
		})
		return stop
	}
	call := func() ([]byte, ports.CompleteTVOrganizationReceipt) {
		req, err := http.NewRequest("POST", base+"/api/v1/download-tasks/"+f.command.TaskID.String()+"/complete-tv-organizations", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		req.Header.Set("Idempotency-Key", "app-lost")
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		b, err := io.ReadAll(r.Body)
		var receipt ports.CompleteTVOrganizationReceipt
		if err != nil || r.StatusCode != 202 || json.Unmarshal(b, &receipt) != nil {
			t.Fatalf("formal App command: %d %s %v", r.StatusCode, b, err)
		}
		return b, receipt
	}
	stop := start()
	first, r := call()
	runtimeTVWait(t, func() bool {
		task, err := f.n.f.a.Queries().GetDownloadTaskByOpID(context.Background(), f.command.OperationID)
		return err == nil && task.OrganizationState == "completed"
	})
	f.assertImported(t, &r)
	stop()
	stop = start()
	replay, next := call()
	if !bytes.Equal(first, replay) || next != r || next.State != "queued" {
		t.Fatal("restart did not recover original lost receipt", next)
	}
	f.assertImported(t, &next)
	stop()
	client.CloseIdleConnections()
}
