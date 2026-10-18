package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"sort"
)

var _ ports.SubmissionRetryRepository = (*DownloadSubmissionStore)(nil)

func retryOwned(ctx context.Context, q *db.Queries, a *ports.Actor, op uuid.UUID) ([]db.DownloadRequirement, error) {
	rows, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	for _, r := range rows {
		if r.ActorType == string(a.Type) && r.ActorID == a.ID {
			return rows, nil
		}
	}
	return nil, ports.ErrForbidden
}
func retryHash(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func retryPreview(ctx context.Context, q *db.Queries, a *ports.Actor, op uuid.UUID, hints []db.DownloadRequirement) (*ports.SubmissionRetryPreview, *db.Job, error) {
	out := &ports.SubmissionRetryPreview{OperationID: op, HoldReason: "original_scope_unavailable"}
	roots := map[uuid.UUID]db.LibraryRoot{}
	units := map[uuid.UUID]db.MediaUnit{}
	for _, r := range hints {
		roots[r.RootID] = db.LibraryRoot{}
		units[r.UnitID] = db.MediaUnit{}
	}
	for _, id := range sortedUUIDKeys(roots) {
		v, e := q.LockAcquisitionRoot(ctx, id)
		if e != nil {
			return nil, nil, ports.ErrUnavailable
		}
		roots[id] = v
	}
	for _, id := range sortedUUIDKeys(units) {
		v, e := q.LockAcquisitionUnit(ctx, id)
		if e != nil {
			return nil, nil, ports.ErrUnavailable
		}
		units[id] = v
	}
	if e := q.LockDownloadAcquisition(ctx, op); e != nil {
		return nil, nil, ports.ErrUnavailable
	}
	rows, e := retryOwned(ctx, q, a, op)
	if e != nil {
		return nil, nil, e
	}
	if _, e = q.GetDownloadTaskByOpID(ctx, op); e == nil {
		out.HoldReason = "physical_task_exists"
		return out, nil, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, ports.ErrUnavailable
	}
	job, e := q.GetOriginalRetrySubmission(ctx, op.String())
	if errors.Is(e, pgx.ErrNoRows) {
		out.HoldReason = "no_dead_submission"
		return out, nil, nil
	}
	if e != nil {
		return nil, nil, ports.ErrUnavailable
	}
	if job.State != "dead" || job.Attempts < job.MaxAttempts || job.LeaseOwner.Valid || job.LeaseUntil.Valid {
		out.HoldReason = "no_dead_submission"
		return out, nil, nil
	}
	if !job.DedupeKey.Valid || job.DedupeKey.String != op.String() || job.Attempts == 2147483647 || job.LeaseToken == 9223372036854775807 {
		return out, nil, nil
	}
	var payload struct {
		OperationID    uuid.UUID  `json:"operation_id"`
		CandidateID    uuid.UUID  `json:"candidate_id"`
		RootID         uuid.UUID  `json:"root_id"`
		RootGeneration int64      `json:"root_generation"`
		ClientID       *uuid.UUID `json:"client_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || payload.OperationID != op || payload.ClientID == nil || *payload.ClientID == uuid.Nil || payload.CandidateID == uuid.Nil || payload.RootID == uuid.Nil {
		return out, nil, nil
	}
	root, known := roots[payload.RootID]
	if !known || root.Generation != payload.RootGeneration || root.Health != "online" {
		out.HoldReason = "inputs_changed"
		return out, nil, nil
	}
	client, e := q.GetDownloadClientByID(ctx, *payload.ClientID)
	if e != nil {
		return out, nil, nil
	}
	owned := false
	for _, r := range rows {
		u, knownUnit := units[r.UnitID]
		rr, knownRoot := roots[r.RootID]
		if !knownUnit || !knownRoot {
			out.HoldReason = "inputs_changed"
			return out, nil, nil
		}
		if r.State != "pending" {
			continue
		}
		var f ports.DownloadDemandFence
		if json.Unmarshal(r.SubmissionFence, &f) != nil || f.SubscriptionID != nil || u.UnitKey != "movie" || u.Retired || f.MediaID != u.MediaID || f.PolicyID == "" || f.PolicyRevision < 1 || f.SourceStamp == "" || f.RootID != r.RootID || f.UnitID != r.UnitID || f.ActorID != r.ActorID || string(f.ActorType) != r.ActorType || f.RootGeneration != rr.Generation || f.UnitMetadataVersion != u.MetadataVersion || f.CandidateID != payload.CandidateID {
			return out, nil, nil
		}
		if r.ActorID == a.ID && r.ActorType == string(a.Type) && r.RootID == payload.RootID {
			media, err := q.GetMediaByID(ctx, u.MediaID)
			if err != nil || media.MediaType != "movie" || media.MetadataVersion != f.MediaMetadataVersion {
				return out, nil, nil
			}
			out.TargetTitle = media.Title
			out.RootName = rr.Name
			out.DownloaderName = client.Name
			owned = true
		}
	}
	if !owned {
		out.HoldReason = "no_active_owned_demand"
		return out, nil, nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID.String() < rows[j].ID.String() })
	out.CanRetry = true
	out.HoldReason = ""
	out.JobID = &job.ID
	out.InputStamp = retryHash([]any{"submission.retry.preview.v1", a.ID, a.Role, job.ID, job.LeaseToken, job.Attempts, job.MaxAttempts, json.RawMessage(job.Payload), rows, roots, units, client})
	return out, &job, nil
}
func (s *DownloadSubmissionStore) PreviewSubmissionRetry(ctx context.Context, a *ports.Actor, op uuid.UUID) (*ports.SubmissionRetryPreview, error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	rows, e := retryOwned(ctx, q, a, op)
	if e != nil {
		return nil, e
	}
	out, _, e := retryPreview(ctx, q, a, op, rows)
	return out, e
}
func (s *DownloadSubmissionStore) RetrySubmission(ctx context.Context, a *ports.Actor, c ports.SubmissionRetryCommand) (*ports.SubmissionRetryReceipt, error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	rows, e := retryOwned(ctx, q, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "submission.retry.command", AggregateID: retryHash([]string{a.ID, c.Key})}); e != nil {
		return nil, ports.ErrUnavailable
	}
	hash := retryHash([]any{"submission.retry.command.v1", c.OperationID, c.JobID, c.ExpectedStamp, c.Acknowledged})
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.Key})
	if e == nil {
		var saved ports.SubmissionRetryReceipt
		if old.RequestHash != hash || old.ResponseCode != 202 || !old.ResourceID.Valid || old.ResourceID.String != c.OperationID.String() || json.Unmarshal(old.ResponseBody, &saved) != nil || saved.JobID != c.JobID || saved.OperationID != c.OperationID || saved.State != "queued" {
			return nil, ports.ErrSubmissionRetryConflict
		}
		return &saved, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrUnavailable
	}
	preview, job, e := retryPreview(ctx, q, a, c.OperationID, rows)
	if e != nil {
		return nil, e
	}
	if !preview.CanRetry || job.ID != c.JobID || preview.InputStamp != c.ExpectedStamp || !c.Acknowledged {
		return nil, ports.ErrSubmissionRetryConflict
	}
	if _, e = q.RequeueOriginalUnsentSubmission(ctx, db.RequeueOriginalUnsentSubmissionParams{ID: job.ID, ExpectedToken: job.LeaseToken}); errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrSubmissionRetryConflict
	} else if e != nil {
		return nil, ports.ErrUnavailable
	}
	safe := ports.SubmissionRetryReceipt{OperationID: c.OperationID, JobID: c.JobID, State: "queued"}
	raw, _ := json.Marshal(safe)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "download.submission_retry", TargetType: "acquisition", TargetID: c.OperationID.String(), Payload: raw}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "acquisition", AggregateID: c.OperationID.String()}); e != nil {
		return nil, ports.ErrUnavailable
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "acquisition", AggregateID: c.OperationID.String()})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "acquisition", AggregateID: c.OperationID.String(), EventType: "download.submission_retry", Payload: raw, Seq: int64(seq)}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.Key, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: c.OperationID.String(), Valid: true}}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return &safe, nil
}
