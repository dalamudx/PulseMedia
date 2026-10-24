package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
	"time"
)

type organizationFixture struct {
	*trackingJobFixture
	organization ports.MovieUpgradeOrganizationRepository
	result       *ports.MovieUpgradeTrackingResult
}

func nativeOrganizationFixture(t *testing.T, mode string, beforeFreeze ...func(*frozenUpgradeFixture)) *organizationFixture {
	t.Helper()
	f := nativeTrackingJobFixture(t, beforeFreeze...)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, organization74MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
	if mode == "tracking" {
		f.complete.Store(false)
	}
	if mode == "manual_review" {
		f.wrongHash.Store(true)
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, f.claim(t, 2*time.Minute))
	if e != nil || out == nil || out.Outcome != mode {
		t.Fatal(out, e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationRepository(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	return &organizationFixture{f, r, out}
}
func (f *organizationFixture) command(t *testing.T) ports.AuthorizeMovieUpgradeOrganization {
	t.Helper()
	view, e := f.organization.ReadMovieUpgradeOrganization(context.Background(), f.actor, f.operation)
	if e != nil || view == nil || view.ExecutionAllowed || view.SourceSize < 1 || view.SourceRelativePath == "" || view.NewScore <= view.CurrentScore || view.ObservationID != f.result.ObservationID {
		t.Fatal(view, e)
	}
	return ports.AuthorizeMovieUpgradeOrganization{OperationID: f.operation, ObservationID: view.ObservationID, InputStamp: view.InputStamp, Confirmed: true}
}
func (f *organizationFixture) unchanged(t *testing.T, before string, authorities int) {
	t.Helper()
	if got := nativeProtectedGraph(t, f.preparedUpgradeFixture); got != before {
		t.Fatal("original task/protection changed")
	}
	var n int
	var files string
	if e := f.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_organization_authorization`).Scan(&n); e != nil || n != authorities {
		t.Fatal(n, e)
	}
	if e := f.p.QueryRow(context.Background(), `SELECT jsonb_build_object('retired',(SELECT count(*) FROM library_file WHERE retired_at IS NOT NULL),'plans',(SELECT count(*) FROM transfer_plan),'steps',(SELECT count(*) FROM transfer_step),'org_jobs',(SELECT count(*) FROM job WHERE kind='transfer.execute'))::text`).Scan(&files); e != nil {
		t.Fatal(e)
	}
	var counts map[string]int
	if e := json.Unmarshal([]byte(files), &counts); e != nil {
		t.Fatal(e)
	}
	for name, n := range counts {
		if n != 0 {
			t.Fatal("unexpected file/plan/job side effect", name, n)
		}
	}
	if f.adds.Load() != 1 || f.infos.Load() != 2 || f.exports.Load() != 1 || f.files.Load() != 1 {
		t.Fatal("extra native work", f.adds.Load(), f.infos.Load(), f.exports.Load(), f.files.Load())
	}
}
func TestMovieUpgradeOrganizationAuthorizePG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	defer organizationAssertFiles(t, f)()
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	c := f.command(t)
	f.unchanged(t, before, 0)
	var wg sync.WaitGroup
	results := make(chan *ports.MovieUpgradeOrganizationAuthorization, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "organization-original-key")
			if e != nil {
				t.Error(e)
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	var first *ports.MovieUpgradeOrganizationAuthorization
	for r := range results {
		if r == nil {
			t.Fatal("nil success")
		}
		if first == nil {
			first = r
		} else if !sameHistoricalJSON(r, first) {
			t.Fatal("receipt changed", r, first)
		}
	}
	if first == nil || first.Status != "organization_authorized_not_executable" || first.ExecutionAllowed {
		t.Fatal(first)
	}
	f.unchanged(t, before, 1)
	if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "different-confirmation"); e == nil {
		t.Fatal("second confirmation accepted")
	}
	// Recovery is permission-protected history, even after the current mode changes.
	if _, e := f.p.Exec(ctx, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub); e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationRepository(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := r.ReadMovieUpgradeOrganizationReceipt(ctx, f.actor, f.operation, "organization-original-key")
	if e != nil || replay == nil || !sameHistoricalJSON(replay, first) {
		t.Fatal(replay, e)
	}
	again, e := r.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "organization-original-key")
	if e != nil || again == nil || !sameHistoricalJSON(again, first) {
		t.Fatal(again, e)
	}
	c.ObservationID = uuid.New()
	if _, e = r.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "organization-original-key"); e == nil {
		t.Fatal("changed command recovered")
	}
	if _, e = r.ReadMovieUpgradeOrganization(ctx, f.actor, f.operation); e == nil {
		t.Fatal("disabled mode still eligible")
	}
}
func TestMovieUpgradeOrganizationAdmissionPG(t *testing.T) {
	for _, mode := range []string{"tracking", "manual_review"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeOrganizationFixture(t, mode)
			if _, e := f.organization.ReadMovieUpgradeOrganization(context.Background(), f.actor, f.operation); e == nil {
				t.Fatal("noncomplete observation admitted")
			}
		})
	}
	for _, which := range []string{"unconfirmed", "stale_stamp", "other_observation", "non_user", "skipped", "policy", "identity", "shared_file"} {
		t.Run(which, func(t *testing.T) {
			f := nativeOrganizationFixture(t, "completed")
			ctx := context.Background()
			c := f.command(t)
			actor := f.actor
			switch which {
			case "unconfirmed":
				c.Confirmed = false
			case "stale_stamp":
				c.InputStamp = strings.Repeat("1", 64)
			case "other_observation":
				c.ObservationID = uuid.New()
			case "non_user":
				copy := *f.actor
				copy.Type = ports.ActorTypeService
				actor = &copy
			case "skipped":
				if _, e := f.p.Exec(ctx, `UPDATE wanted_item SET is_skipped=true WHERE subscription_id=$1`, f.sub); e != nil {
					t.Fatal(e)
				}
			case "policy":
				if _, e := f.p.Exec(ctx, `WITH newer AS(INSERT INTO policy_revision SELECT x.* FROM policy_revision r CROSS JOIN LATERAL jsonb_populate_record(NULL::policy_revision,to_jsonb(r)||jsonb_build_object('id',gen_random_uuid(),'revision',r.revision+1)) x WHERE r.policy_id=(SELECT quality_policy_id FROM subscription WHERE id=$1) AND r.revision=(SELECT current_revision FROM policy WHERE id=r.policy_id) RETURNING policy_id,revision) UPDATE policy p SET current_revision=n.revision FROM newer n WHERE p.id=n.policy_id`, f.sub); e != nil {
					t.Fatal(e)
				}
			case "identity":
				if _, e := f.p.Exec(ctx, `UPDATE media_identity SET external_id=external_id||'9' WHERE media_id=(SELECT media_id FROM subscription WHERE id=$1)`, f.sub); e != nil {
					t.Fatal(e)
				}
			case "shared_file":
				otherMedia, otherUnit := uuid.New(), uuid.New()
				if _, e := f.p.Exec(ctx, `INSERT INTO media SELECT * FROM jsonb_populate_record(NULL::media,(SELECT to_jsonb(m)||jsonb_build_object('id',$2::uuid) FROM media m JOIN subscription s ON s.media_id=m.id WHERE s.id=$1))`, f.sub, otherMedia); e != nil {
					t.Fatal(e)
				}
				if _, e := f.p.Exec(ctx, `INSERT INTO media_unit SELECT * FROM jsonb_populate_record(NULL::media_unit,(SELECT to_jsonb(u)||jsonb_build_object('id',$2::uuid,'media_id',$3::uuid) FROM media_unit u JOIN movie_upgrade_authority a ON a.unit_id=u.id WHERE a.operation_id=$1))`, f.operation, otherUnit, otherMedia); e != nil {
					t.Fatal(e)
				}
				if _, e := f.p.Exec(ctx, `INSERT INTO library_file_unit(file_id,unit_id) SELECT old_file_id,$2 FROM movie_upgrade_authority WHERE operation_id=$1`, f.operation, otherUnit); e != nil {
					t.Fatal(e)
				}
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, actor, c, "rejected-organization"); e == nil {
				t.Fatal("unsafe confirmation accepted")
			}
			f.unchanged(t, before, 0)
		})
	}
}
func TestMovieUpgradeOrganizationTailRollbackPG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	ctx := context.Background()
	c := f.command(t)
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	if _, e := f.p.Exec(ctx, `CREATE FUNCTION pulse_test_org_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='upgrade.organization-authorize' THEN UPDATE download_requirement SET state='withdrawn' WHERE operation_id=(SELECT operation_id FROM movie_upgrade_organization_authorization WHERE id=NEW.aggregate_id::uuid);END IF;RETURN NEW;END $$;CREATE TRIGGER pulse_test_org_tail AFTER INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION pulse_test_org_tail()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "tail-key"); e == nil {
		t.Fatal("tail mutation accepted")
	}
	f.unchanged(t, before, 0)
	var n int
	if e := f.p.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='upgrade.organization-authorize'`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
func TestMovieUpgradeOrganizationForgedReceiptPG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	ctx := context.Background()
	c := f.command(t)
	out, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "forged-key")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.p.Exec(ctx, `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{execution_allowed}','true'::jsonb) WHERE idempotency_key='upgrade.organization-authorize:forged-key'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.organization.ReadMovieUpgradeOrganizationReceipt(ctx, f.actor, f.operation, "forged-key"); e == nil {
		t.Fatal("forged response accepted")
	}
	// Keep imports checked against the real public error and JSON response shape.
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "organization_authorized_not_executable") {
		t.Fatal(string(raw))
	}
	if _, e = f.organization.ReadMovieUpgradeOrganizationReceipt(ctx, f.actor, uuid.New(), "forged-key"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal(e)
	}
}
