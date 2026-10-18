package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ ports.CompleteTVSubmissionResultRepository = (*CompleteTVSubmissionStore)(nil)

// RecordCompleteTVSubmissionResult persists an observation of the original Add.
// It deliberately does not reauthorize an external action: current account,
// selection, metadata, source and client configuration may change during HTTP.
// Immutable schema59/60 evidence and the original live lease are its authority.
func (s *CompleteTVSubmissionStore) RecordCompleteTVSubmissionResult(ctx context.Context, c ports.CompleteTVSubmissionResultCommand) (out *ports.CompleteTVSubmissionResultReceipt, err error) {
	stale := ports.ErrCompleteTVResultStale
	defer func() {
		if err != nil {
			out = nil
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if !errors.Is(err, stale) {
				err = ports.ErrUnavailable
			}
		}
	}()
	if c.Lease.ID == uuid.Nil || c.Lease.Owner == "" || c.OperationID == uuid.Nil || c.TaskID == uuid.Nil || !c.Metainfo.ValidateBinding() {
		return nil, stale
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	record, e := q.GetCompleteTVExecutionAuthority(ctx, c.OperationID)
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || frozen.Kind != "reviewed_complete_tv_coverage_v1" || frozen.ActorID != record.ActorID || frozen.ReviewID != record.ReviewID || frozen.SubscriptionID != record.SubscriptionID || frozen.RootID != record.RootID || frozen.CandidateID != record.CandidateID || frozen.ClientID != record.ClientID || len(frozen.Units) < 2 || len(frozen.Units) > 1000 {
		return nil, stale
	}
	units := append([]media.NumberingRow(nil), frozen.Units...)
	sort.Slice(units, func(i, j int) bool { return units[i].UnitID.String() < units[j].UnitID.String() })
	for i, u := range units {
		if u.UnitID == uuid.Nil || (i > 0 && units[i-1].UnitID == u.UnitID) {
			return nil, stale
		}
		// Use frozen UUID order, never a changed current selection or membership.
		if _, e = q.LockAcquisitionUnit(ctx, u.UnitID); e != nil {
			return nil, completeTVResultReadError(e)
		}
	}
	if e = q.LockDownloadAcquisition(ctx, c.OperationID); e != nil {
		return nil, e
	}
	task, e := q.LockDownloadTask(ctx, c.TaskID)
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	if task.OperationID != c.OperationID || task.CandidateID != frozen.CandidateID || task.ClientID != frozen.ClientID || task.InfoHash != c.Metainfo.InfoHash() || task.State != "submitting" {
		return nil, stale
	}
	if _, e = q.LockJobForWrite(ctx, c.Lease.ID); e != nil {
		return nil, completeTVResultReadError(e)
	}
	prepared, e := q.GetCompleteTVPreparation(ctx, c.OperationID)
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	digest := c.Metainfo.SHA256()
	if prepared.TaskID != task.ID || prepared.JobID != c.Lease.ID || prepared.LeaseOwner != c.Lease.Owner || prepared.LeaseToken != c.Lease.Token || prepared.InfoHash != task.InfoHash || !bytes.Equal(prepared.TorrentSha256, digest[:]) {
		return nil, stale
	}
	var proof completeTVPreparedProof
	if json.Unmarshal(prepared.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || proof.SavePath == "" || proof.SavePath != task.SavePath || proof.MaterialStamp != frozen.MaterialStamp || proof.InfoHash != task.InfoHash || proof.TorrentSHA256 != digest || proof.TotalBytes != c.Metainfo.TotalBytes() || proof.TotalBytes != frozen.CandidateSize || len(proof.Files) != len(units) {
		return nil, stale
	}
	sort.Slice(proof.Files, func(i, j int) bool { return proof.Files[i].Unit.UnitID.String() < proof.Files[j].Unit.UnitID.String() })
	preparedUnits, e := q.ListCompleteTVPreparedUnits(ctx, task.ID)
	if e != nil {
		return nil, e
	}
	if len(preparedUnits) != len(units) {
		return nil, stale
	}
	for i, u := range preparedUnits {
		f := proof.Files[i]
		if f.Unit != units[i] || u.UnitID != units[i].UnitID || u.UnitVersion != units[i].UnitVersion || u.ProviderEpisodeID != units[i].EpisodeID || u.UnitKey != units[i].UnitKey || u.VideoPath != f.Path || u.VideoSize != f.Size {
			return nil, stale
		}
	}
	// Lock demands as well as the physical protections. All writers for this
	// operation serialize through its advisory lock; no partial set is accepted.
	locked, e := tx.Query(ctx, `SELECT id FROM download_requirement WHERE operation_id=$1 ORDER BY unit_id,root_id,id FOR UPDATE`, c.OperationID)
	if e != nil {
		return nil, e
	}
	_, e = pgx.CollectRows(locked, pgx.RowTo[uuid.UUID])
	if e != nil {
		return nil, e
	}
	demands, e := q.ListRequirementsForOperation(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	lanes, e := q.ListCompleteTVPreparationLanes(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	reservations, e := q.ListCompleteTVPreparationReservations(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	if len(demands) != len(units) || len(lanes) != len(units) || len(reservations) != len(units) {
		return nil, stale
	}
	sort.Slice(demands, func(i, j int) bool { return demands[i].UnitID.String() < demands[j].UnitID.String() })
	for i, u := range units {
		d, l, r := demands[i], lanes[i], reservations[i]
		fence, _ := json.Marshal(ports.DownloadDemandFence{PolicyID: frozen.PolicyID, PolicyRevision: frozen.PolicyRevision, SubscriptionID: &frozen.SubscriptionID, IntentVersion: frozen.IntentVersion, RootGeneration: frozen.RootGeneration, UnitMetadataVersion: u.UnitVersion, MediaMetadataVersion: frozen.MediaVersion, CandidateID: frozen.CandidateID, RootID: frozen.RootID, UnitID: u.UnitID, MediaID: frozen.MediaID, ActorID: frozen.ActorID.String(), ActorType: ports.ActorTypeUser, SourceStamp: frozen.SourceStamp})
		if d.UnitID != u.UnitID || d.RootID != frozen.RootID || d.ActorID != frozen.ActorID.String() || d.ActorType != string(ports.ActorTypeUser) || d.State != "pending" || completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(fence) || l.UnitID != u.UnitID || l.AcquisitionLane != "default" || l.LegacyConflict || l.SourceRootID != frozen.RootID || l.SourceGeneration != frozen.RootGeneration || l.SourceStamp != frozen.SourceStamp || l.CandidateID != frozen.CandidateID || !l.TaskID.Valid || uuid.UUID(l.TaskID.Bytes) != task.ID || r.UnitID != u.UnitID || r.RootID != frozen.RootID || r.CandidateID != frozen.CandidateID || r.Purpose != "manual_acquisition" || r.State != "dispatched" {
			return nil, stale
		}
		// Expiry of a reservation does not erase an already sent physical Add.
		// Dispatched rows and lanes remain held for observation/recovery.
	}
	// Strict DB time is read only AFTER every potentially blocking protection
	// lock. A newly reclaimed lease cannot impersonate the preparing sender.
	auth := db.GetAuthorizedCompleteTVPreparationJobParams{ID: c.Lease.ID, Owner: c.Lease.Owner, Token: c.Lease.Token}
	job, e := q.GetAuthorizedCompleteTVPreparationJob(ctx, auth)
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	var payload completeTVPreparationPayload
	if json.Unmarshal(job.Payload, &payload) != nil || payload.OperationID != c.OperationID || payload.Authority != c.OperationID || payload.CandidateID != frozen.CandidateID || payload.RootID != frozen.RootID || payload.RootGeneration != frozen.RootGeneration || payload.ClientID != frozen.ClientID || payload.MaterialStamp != frozen.MaterialStamp || payload.ObservationStamp != frozen.ObservationStamp || len(payload.UnitIDs) != len(units) {
		return nil, stale
	}
	sort.Slice(payload.UnitIDs, func(i, j int) bool { return payload.UnitIDs[i].String() < payload.UnitIDs[j].String() })
	for i, u := range units {
		if payload.UnitIDs[i] != u.UnitID {
			return nil, stale
		}
	}
	if c.Acknowledged && c.AddError == nil {
		task, e = q.TransitionTaskToTracking(ctx, task.ID)
	} else {
		task, e = q.MarkSubmissionOutcomeUnknown(ctx, task.ID)
	}
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	out = &ports.CompleteTVSubmissionResultReceipt{TaskID: task.ID, State: task.State}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: c.Lease.Owner, Action: "complete_tv.submission_observed", TargetType: "download_task", TargetID: task.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", c.OperationID.String(), "complete_tv.submission_observed", map[string]any{"operation_id": c.OperationID, "task_id": task.ID, "state": task.State}); e != nil {
		return nil, e
	}
	// FinishJob locks/checks owner, token, running state and clock_timestamp()
	// and RETURNS a row. ErrNoRows is stale, never a successful no-op. The
	// dedicated kind/payload are stable under our job lock throughout.
	finished, e := q.FinishJob(ctx, db.FinishJobParams{Column1: c.Lease.ID, Column2: c.Lease.Owner, Column3: c.Lease.Token, Column4: safe})
	if e != nil {
		return nil, completeTVResultReadError(e)
	}
	if finished.ID != prepared.JobID || finished.State != "succeeded" {
		return nil, stale
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func completeTVResultReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrCompleteTVResultStale
	}
	return err
}
