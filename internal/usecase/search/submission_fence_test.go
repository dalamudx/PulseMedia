package search_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestSubmissionRequiresCurrentDemand(t *testing.T) {
	for _, change := range []string{"valid", "pause", "skip", "intent", "root", "metadata", "source", "source-freshloaded", "old-token", "old-lease", "legacy", "audit-fault", "cancel-event-fault"} {
		t.Run(change, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			round := f.collect(t)
			decisionJob := f.finalJob(t, round)
			result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(decisionJob))
			if e != nil || result.OperationID == uuid.Nil {
				t.Fatal(result, e)
			}
			job, e := f.queue.Claim(ctx, []string{"download_submission"}, "submission-owner", 5*time.Minute)
			if e != nil || job == nil {
				t.Fatal(job, e)
			}
			var payload struct {
				CandidateID uuid.UUID `json:"candidate_id"`
			}
			if json.Unmarshal(job.Payload, &payload) != nil {
				t.Fatal("payload")
			}
			candidate, e := f.q.GetCandidateByID(ctx, payload.CandidateID)
			if e != nil {
				t.Fatal(e)
			}
			root, e := f.q.GetLibraryRootByID(ctx, f.root)
			if e != nil {
				t.Fatal(e)
			}
			cmd := ports.DownloadSubmissionCommand{Lease: ports.JobLease{ID: job.ID, Owner: "submission-owner", Token: job.LeaseToken}, TaskID: uuid.New(), OperationID: result.OperationID, ClientID: f.downloader, CandidateID: candidate.ID, InfoHash: candidate.InfoHash.String, SavePath: root.Path, CipherHash: sha256.Sum256(candidate.LinkEncrypted)}
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, e := f.pool.Exec(ctx, sql, args...); e != nil {
					t.Fatal(e)
				}
			}
			switch change {
			case "pause":
				exec("UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", f.sub)
			case "skip":
				exec("UPDATE wanted_item SET is_skipped=true WHERE id=$1", f.wanted)
			case "intent":
				exec("UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.sub)
			case "root":
				exec("UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root)
			case "metadata":
				exec("UPDATE media_unit SET air_date=CURRENT_DATE+1 WHERE id=(SELECT unit_id FROM wanted_item WHERE id=$1)", f.wanted)
			case "source":
				exec("UPDATE release_candidate SET link_encrypted=decode('0001','hex') WHERE id=$1", candidate.ID)
			case "source-freshloaded":
				exec("UPDATE release_candidate SET link_encrypted=decode('0001','hex') WHERE id=$1", candidate.ID)
				cmd.CipherHash = sha256.Sum256([]byte{0, 1})
			case "old-token":
				cmd.Lease.Token++
			case "old-lease":
				exec("UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID)
			case "legacy":
				exec("UPDATE download_requirement SET submission_fence=NULL WHERE operation_id=$1", result.OperationID)
			case "cancel-event-fault":
				exec("UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", f.sub)
				exec("CREATE FUNCTION reject_control_cancel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='download.requirement_cancelled_before_send' THEN RAISE EXCEPTION 'fixture-secret'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_control_cancel BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_control_cancel()")
			case "audit-fault":
				exec("CREATE FUNCTION reject_prepared_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.submission_prepared' THEN RAISE EXCEPTION 'fixture-secret'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_prepared_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_prepared_audit()")
			}
			receipt, e := postgres.NewDownloadSubmissionStore(f.pool, idgen.New()).PrepareSubmission(ctx, cmd)
			switch change {
			case "valid":
				if e != nil || receipt.TaskID != cmd.TaskID {
					t.Fatal(receipt, e)
				}
			case "pause", "skip", "intent":
				if e != nil || receipt == nil || !receipt.Declined {
					t.Fatal("known unsent control cancellation not confirmed", receipt, e)
				}
				var tasks int
				if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE operation_id=$1", result.OperationID).Scan(&tasks); e != nil || tasks != 0 {
					t.Fatal("cancelled before send persisted task", tasks, e)
				}
			default:
				if e == nil {
					t.Fatal("stale/unproven demand authorized network", change)
				}
				if (change == "audit-fault" || change == "cancel-event-fault") && !errors.Is(e, ports.ErrUnavailable) {
					t.Fatal("raw audit error escaped", e)
				}
				var tasks int
				if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE operation_id=$1", result.OperationID).Scan(&tasks); e != nil || tasks != 0 {
					t.Fatal("failed prepare persisted task", tasks, e)
				}
			}
			var protected int
			expectedProtected := 1
			if change == "pause" || change == "skip" || change == "intent" {
				expectedProtected = 0
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched')", result.OperationID).Scan(&protected); e != nil || protected != expectedProtected {
				t.Fatal("refused submission released protection", protected, e)
			}
		})
	}
}
