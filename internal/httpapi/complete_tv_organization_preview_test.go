package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func previewRepo(f *completeOrganizationFixture) *transferstore.Repository {
	return transferstore.NewRepository(f.n.f.a.DBPool(), idgen.New(), nil, nil, transfer.ExecutePreparedPlan)
}
func organizationPreviewRouter(f *completeOrganizationFixture) http.Handler {
	a := f.n.f.a
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	h := httpapi.NewServerHandler(a, as, nil, nil, f.worker, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetCompleteTVOrganizationPreviewService(transfer.NewCompleteTVOrganizationPreviewService(previewRepo(f)))
	return httpapi.NewRouter(a, h, as, nil)
}
func previewCall(h http.Handler, f *completeOrganizationFixture, cookie, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://pulse.local/api/v1/operations/"+f.command.OperationID.String()+"/complete-tv-organizations", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: cookie})
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestCompleteTVOrganizationPreviewPGSafeExactReadOnly(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPreviewRouter(f)
	before := organizationPublicCounts(t, f)
	w := previewCall(h, f, f.n.f.admin, "")
	var page ports.CompleteTVOrganizationPreviewPage
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	p := page.Items[0]
	if p.TaskID != f.command.TaskID || p.OperationID != f.command.OperationID || p.TrackingJobID != f.command.TrackingJobID || p.Season != 1 || p.AttachmentCount != 1 || !p.CanConfirm || len(p.Videos) != 2 {
		t.Fatal("wrong persisted references/coverage", p)
	}
	expected := []string{"S01E01", "S01E02"}
	for i, v := range p.Videos {
		if v.UnitKey != expected[i] || v.SourceRelative != "Show/Show."+expected[i]+".mkv" || !strings.Contains(v.TargetRelative, "Season 01/") || !strings.Contains(v.TargetRelative, expected[i]) || v.SizeBytes != 512 {
			t.Fatal("wrong video", v)
		}
	}
	for _, private := range []string{f.source, f.target, "remote_path_map", "password", "api_key", "inode", "baseline"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private evidence leaked", private)
		}
	}
	if organizationPublicCounts(t, f) != before {
		t.Fatal("GET produced execution state")
	}
	// Missing filesystem support proves preview cannot touch a filesystem adapter.
	if _, err := previewRepo(f).ReadCompleteTVOrganizations(context.Background(), f.n.actor, f.command.OperationID); err != nil {
		t.Fatal(err)
	}
}
func TestCompleteTVOrganizationPreviewPGCurrentScopeBeforeEvidence(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	h := organizationPreviewRouter(f)
	completePublicProblem(t, previewCall(h, f, "", ""), 401)
	completePublicProblem(t, previewCall(h, f, f.n.f.member, ""), 403)
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.n.f.a.DBPool(), idgen.New()), crypto.ReadTokenIssuer{}, clock.New())
	_, secret, err := tokens.Create(context.Background(), f.n.actor, ports.TokenCommand{Name: "preview read"}, "preview-token")
	if err != nil {
		t.Fatal(err)
	}
	f.n.exec(t, `ALTER TABLE complete_tv_submission_preparation RENAME TO unavailable_preparation`)
	defer f.n.exec(t, `ALTER TABLE unavailable_preparation RENAME TO complete_tv_submission_preparation`)
	t.Run("token-before-evidence", func(t *testing.T) { completePublicProblem(t, previewCall(h, f, "", secret), 403) })
	other := uuid.New()
	f.n.exec(t, `INSERT INTO app_user(id,username,password_hash,role) SELECT $1::uuid,$1::uuid::text,password_hash,'admin' FROM app_user WHERE id=$2`, other, f.n.actor.ID)
	f.n.exec(t, `UPDATE subscription SET user_id=$1 WHERE id=$2`, other, f.n.f.sub.Id)
	t.Run("current-object-before-evidence", func(t *testing.T) { completePublicProblem(t, previewCall(h, f, f.n.f.admin, ""), 403) })
	f.n.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.n.actor.ID)
	t.Run("current-account-before-evidence", func(t *testing.T) {
		if _, err := previewRepo(f).ReadCompleteTVOrganizations(context.Background(), f.n.actor, f.command.OperationID); !errors.Is(err, ports.ErrForbidden) {
			t.Fatal(err)
		}
	})
}
func TestCompleteTVOrganizationPreviewPGWaitCancellationAndUnavailable(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	repo := previewRepo(f)
	ctx := context.Background()
	before := organizationPublicCounts(t, f)
	tx, err := f.n.f.a.DBPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.n.actor.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := repo.ReadCompleteTVOrganizations(ctx, f.n.actor, f.command.OperationID); done <- e }()
	waitCompleteTVLock(t, f.n.completeTVFixture, "app_user")
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, ports.ErrUnavailable) && !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("account lock wait exposed evidence", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("account lock wait hung")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = repo.ReadCompleteTVOrganizations(canceled, f.n.actor, f.command.OperationID); !errors.Is(err, context.Canceled) {
		t.Fatal("context lost", err)
	}
	f.n.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, f.n.actor.ID)
	f.n.exec(t, `ALTER TABLE complete_tv_submission_preparation RENAME TO unavailable_preparation`)
	completePublicProblem(t, previewCall(organizationPreviewRouter(f), f, f.n.f.admin, ""), 503)
	f.n.exec(t, `ALTER TABLE unavailable_preparation RENAME TO complete_tv_submission_preparation`)
	if organizationPublicCounts(t, f) != before {
		t.Fatal("failed reads wrote")
	}
	w := previewCall(organizationPreviewRouter(f), f, f.n.f.admin, "")
	if w.Code != 200 {
		t.Fatalf("rollback locks not released %d %s", w.Code, w.Body.String())
	}
	// Terminate the physical reader during a real root-lock wait. No preview may
	// survive the connection loss/failed rollback, and a subsequent read must work.
	blocked, e := f.n.f.a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = blocked.Rollback(ctx) }()
	if _, e = blocked.Exec(ctx, `SELECT id FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1) FOR UPDATE`, f.n.f.sub.Id); e != nil {
		t.Fatal(e)
	}
	type result struct {
		page *ports.CompleteTVOrganizationPreviewPage
		err  error
	}
	lost := make(chan result, 1)
	go func() {
		p, e := repo.ReadCompleteTVOrganizations(ctx, f.n.actor, f.command.OperationID)
		lost <- result{p, e}
	}()
	waitCompleteTVLock(t, f.n.completeTVFixture, "library_root")
	var pid int
	var killed bool
	if e = f.n.f.a.DBPool().QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%library_root%' AND pid<>pg_backend_pid() LIMIT 1`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if e = f.n.f.a.DBPool().QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); e != nil || !killed {
		t.Fatal("reader was not terminated", e)
	}
	select {
	case got := <-lost:
		if got.page != nil || !errors.Is(got.err, ports.ErrUnavailable) {
			t.Fatal("connection loss exposed evidence", got)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("lost reader hung")
	}
	if e = blocked.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if w = previewCall(organizationPreviewRouter(f), f, f.n.f.admin, ""); w.Code != 200 {
		t.Fatalf("connection loss left locks: %d %s", w.Code, w.Body.String())
	}
}
func TestCompleteTVOrganizationPreviewPGEvidenceFailures(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"unit-version", `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1)`},
		{"root-generation", `UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`},
		{"source-version", `UPDATE indexer SET config_version=config_version+1 WHERE $1::uuid IS NOT NULL`},
		{"tracking-proof", `UPDATE job SET result=jsonb_set(result,'{files}','[]') WHERE kind='complete_tv_coverage_tracking' AND payload->>'operation_id'=(SELECT operation_id::text FROM download_task LIMIT 1) AND $1::uuid IS NOT NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			before := organizationPublicCounts(t, f)
			f.n.exec(t, tc.sql, f.n.f.sub.Id)
			completePublicProblem(t, previewCall(organizationPreviewRouter(f), f, f.n.f.admin, ""), 409)
			if organizationPublicCounts(t, f) != before {
				t.Fatal("invalid evidence scheduled work")
			}
		})
	}
}
