package postgres_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func preparedRestoreCheckin(t *testing.T) (*runtimeCheckinFixture, ports.SiteCheckinSendPermission, postgres.RestoreReconciliation) {
	t.Helper()
	f := runtimeCheckin(t)
	p, l := f.permit(t)
	grant := prepareCheckin(t, f, p)
	p.Close()
	// Simulate a stopped process whose job has been individually retained as dead.
	// The real dedicated store created the permission; no native POST is made here.
	f.exec(t, `UPDATE job SET state='dead',lease_until=NULL,lease_owner=NULL WHERE id=$1`, l.ID)
	restore := uuid.New()
	if err := postgres.ActivateRestoreIsolation(t.Context(), f.p, restore); err != nil {
		t.Fatal(err)
	}
	return f, grant, postgres.RestoreReconciliation{RestoreID: restore, Operator: "check-in restore operator", DownloaderEvidence: "downloader identities compared", FilesystemEvidence: "files compared", OrphanEvidence: "orphans reviewed"}
}

func restoreCheckinGraph(t *testing.T, f *runtimeCheckinFixture, id uuid.UUID) string {
	t.Helper()
	var raw string
	err := f.p.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'day',to_jsonb(d)-ARRAY['restore_id','restore_operator','restore_evidence','restore_recorded_at','restore_decision'],
 'execution',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM site_checkin_execution e WHERE e.day_id=d.id),
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM job j WHERE j.id IN(d.original_job_id,d.observation_job_id)),
 'source',(SELECT to_jsonb(i) FROM indexer i WHERE i.id=d.indexer_id),
 'config',(SELECT to_jsonb(c) FROM site_checkin_config c WHERE c.indexer_id=d.indexer_id),
 'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.version) FROM site_checkin_config_receipt r WHERE r.indexer_id=d.indexer_id))::text
 FROM site_checkin_day d WHERE d.id=$1`, id).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRestoreIsolationCheckinPermissionPG(t *testing.T) {
	f, grant, r := preparedRestoreCheckin(t)
	before := restoreCheckinGraph(t, f, grant.DayID)
	if err := postgres.ReconcileRestoreIsolation(t.Context(), f.p, r); err == nil || !strings.Contains(err.Error(), "unreconciled") {
		t.Fatal("unresolved daily send authority released", err)
	}
	status, err := postgres.RestoreStatus(t.Context(), f.p)
	if err != nil || !status.Isolated {
		t.Fatal("isolation lost", err)
	}
	f.count(t, `SELECT count(*) FROM restore_isolation_audit WHERE action='reconcile'`, 0)
	if restoreCheckinGraph(t, f, grant.DayID) != before {
		t.Fatal("refusal modified original send authority")
	}
	r.CheckinDecisions = []postgres.CheckinRestoreDecision{{DayID: grant.DayID, Evidence: "external outcome remains unknown; retain the original permission and manual review"}}
	if err = postgres.ReconcileRestoreIsolation(t.Context(), f.p, r); err != nil {
		t.Fatal(err)
	}
	if err = postgres.RequireRestoreReconciled(t.Context(), f.p); err != nil {
		t.Fatal(err)
	}
	f.count(t, `SELECT count(*) FROM site_checkin_day WHERE id=$1 AND restore_id=$2 AND restore_decision='retain_review' AND prepared_execution_id IS NOT NULL AND observed_at IS NULL`, 1, grant.DayID, r.RestoreID)
	f.count(t, `SELECT count(*) FROM restore_isolation_audit WHERE action='reconcile' AND evidence LIKE '%check-in decisions:%'`, 1)
	if err = f.s.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.s.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind IN ('site_checkin','site_checkin_observe')`, 1)
	if restoreCheckinGraph(t, f, grant.DayID) != before {
		t.Fatal("retained unknown changed or was automatically replayed")
	}
}

func TestRestoreIsolationCheckinDecisionRollbackPG(t *testing.T) {
	for _, mode := range []string{"pending work", "missing day", "duplicate", "empty evidence"} {
		t.Run(mode, func(t *testing.T) {
			f, grant, r := preparedRestoreCheckin(t)
			before := restoreCheckinGraph(t, f, grant.DayID)
			r.CheckinDecisions = []postgres.CheckinRestoreDecision{{DayID: grant.DayID, Evidence: "retain unknown original authority"}}
			switch mode {
			case "pending work":
				f.exec(t, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) VALUES($1::uuid,'fixture_pending',$1::uuid::text,'queued','{}',1)`, uuid.New())
			case "missing day":
				r.CheckinDecisions = append(r.CheckinDecisions, postgres.CheckinRestoreDecision{DayID: uuid.New(), Evidence: "missing identity must not succeed"})
			case "duplicate":
				r.CheckinDecisions = append(r.CheckinDecisions, r.CheckinDecisions[0])
			case "empty evidence":
				r.CheckinDecisions[0].Evidence = " "
			}
			if err := postgres.ReconcileRestoreIsolation(t.Context(), f.p, r); err == nil {
				t.Fatal("invalid or incomplete reconciliation succeeded")
			}
			status, err := postgres.RestoreStatus(t.Context(), f.p)
			if err != nil || !status.Isolated {
				t.Fatal("isolation lost", err)
			}
			f.count(t, `SELECT count(*) FROM site_checkin_day WHERE id=$1 AND restore_id IS NOT NULL`, 0, grant.DayID)
			f.count(t, `SELECT count(*) FROM restore_isolation_audit WHERE action='reconcile'`, 0)
			if restoreCheckinGraph(t, f, grant.DayID) != before {
				t.Fatal("failed reconciliation modified authority")
			}
		})
	}
}
