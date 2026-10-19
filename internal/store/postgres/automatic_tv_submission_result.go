package postgres

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// This observes the original Add. Current business permission and first-send TTL
// cannot erase a side effect that already happened; ingestion needs its own gate.
func (s *AutomaticTVSubmissionStore) RecordAutomaticTVSubmissionResult(ctx context.Context, c ports.AutomaticTVSubmissionResultCommand) (out *ports.AutomaticTVSubmissionResultReceipt, err error) {
	defer func() {
		err = automaticTVSubmissionError(ctx, err, true)
		if err != nil {
			out = nil
		}
	}()
	stale := ports.ErrAutomaticTVResultStale
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if !automaticTVSendRequestValid(c.Request) || c.TaskID == uuid.Nil || c.PhysicalAttemptID == uuid.Nil || !c.Metainfo.ValidateBinding() {
		return nil, stale
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	a, e := automaticTVSubmissionAuthority(ctx, q, c.Request.OperationID)
	if e != nil {
		return nil, e
	}
	// Original job precedes the frozen unit, consistently with Prepare. No lock
	// on current owner/policy/source/client is needed to record a physical fact.
	if _, e = q.LockJobForWrite(ctx, c.Request.Lease.ID); e != nil {
		return nil, e
	}
	if _, e = q.LockAcquisitionUnit(ctx, a.Target.UnitID); e != nil {
		return nil, e
	}
	if e = q.LockDownloadAcquisition(ctx, a.OperationID); e != nil {
		return nil, e
	}
	task, e := q.LockDownloadTask(ctx, c.TaskID)
	if e != nil {
		return nil, e
	}
	if task.OperationID != a.OperationID || task.ClientID != a.Target.ClientID || task.CandidateID != a.CandidateID || task.InfoHash != c.Metainfo.InfoHash() || task.State != "submitting" {
		return nil, stale
	}
	prepared, e := q.AutomaticTVSubmissionPreparation(ctx, a.OperationID)
	if e != nil {
		return nil, e
	}
	digest := c.Metainfo.SHA256()
	if prepared.TaskID != task.ID || prepared.PhysicalAttemptID != c.PhysicalAttemptID || prepared.JobID != c.Request.Lease.ID || prepared.LeaseOwner != c.Request.Lease.Owner || prepared.LeaseToken != c.Request.Lease.Token || prepared.AuthorityHash != a.DecisionStamp || prepared.InfoHash != task.InfoHash || !bytes.Equal(prepared.TorrentSha256, digest[:]) || prepared.ProofHash != completeTVJSONDigest(prepared.Proof) {
		return nil, stale
	}
	proof, e := automaticTVMakePrepared(a, task.SavePath, c.Metainfo)
	if e != nil {
		return nil, stale
	}
	if completeTVJSONDigest(mustAutomaticTVJSON(proof)) != prepared.ProofHash {
		return nil, stale
	}
	if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionProtections(ctx, tx, q, a, task.ID); e != nil {
		return nil, e
	}
	// Losing the advisory connection does not invalidate this original result.
	// The persistent exclusive nonce still has to be active and exact.
	if e = automaticTVSubmissionPhysical(ctx, tx, q, c.Request, c.PhysicalAttemptID, false); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionTail(ctx, tx, q, c.Request, a, false); e != nil {
		return nil, e
	}
	originalJob, e := q.GetJobByID(ctx, c.Request.Lease.ID)
	if e != nil {
		return nil, e
	}
	state := "submission_unknown"
	if c.Acknowledged && c.AddError == nil {
		task, e = q.TransitionTaskToTracking(ctx, task.ID)
		state = "tracking"
	} else {
		task, e = q.MarkSubmissionOutcomeUnknown(ctx, task.ID)
	}
	if e != nil {
		return nil, e
	}
	if e = q.AutomaticTVInsertSubmissionResult(ctx, db.AutomaticTVInsertSubmissionResultParams{OperationID: a.OperationID, TaskID: task.ID, PhysicalAttemptID: c.PhysicalAttemptID, JobID: c.Request.Lease.ID, LeaseOwner: c.Request.Lease.Owner, LeaseToken: c.Request.Lease.Token, State: state}); e != nil {
		return nil, e
	}
	out = &ports.AutomaticTVSubmissionResultReceipt{TaskID: task.ID, State: state}
	safe := mustAutomaticTVJSON(out)
	if e = s.automaticTVSubmissionEvent(ctx, q, a, "automatic_tv.submission_observed", task.ID, safe); e != nil {
		return nil, e
	}
	if e = automaticTVSubmissionJob(ctx, q, c.Request, a); e != nil {
		return nil, e
	}
	finished, e := q.FinishJob(ctx, db.FinishJobParams{Column1: c.Request.Lease.ID, Column2: c.Request.Lease.Owner, Column3: c.Request.Lease.Token, Column4: safe})
	if e != nil {
		return nil, e
	}
	if finished.ID != prepared.JobID || finished.State != "succeeded" || finished.LeaseToken != c.Request.Lease.Token {
		return nil, stale
	}
	// FinishJob can itself fire a slow trigger. Check its retained finishing token
	// and the original lease timestamp AFTER the last SQL write.
	var valid bool
	e = tx.QueryRow(ctx, `SELECT state='succeeded' AND lease_token=$2 AND lease_until IS NULL AND lease_owner IS NULL AND $3::timestamptz>clock_timestamp() AND result=$4::jsonb AND kind='automatic_tv_download_submission' AND dedupe_key=$5::uuid::text AND payload=$6::jsonb FROM job WHERE id=$1`, finished.ID, c.Request.Lease.Token, originalJob.LeaseUntil, json.RawMessage(safe), a.OperationID, originalJob.Payload).Scan(&valid)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, stale
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
