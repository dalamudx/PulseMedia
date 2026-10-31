package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strings"
)

type DownloadSubmissionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewDownloadSubmissionStore(pool *pgxpool.Pool, ids ports.IDGen) *DownloadSubmissionStore {
	return &DownloadSubmissionStore{pool, ids}
}

var _ ports.DownloadSubmissionRepository = (*DownloadSubmissionStore)(nil)

func (s *DownloadSubmissionStore) AttachRequirement(ctx context.Context, a *ports.Actor, op, unit, root uuid.UUID) error {
	if err := RejectTVEpisodeUpgradeOperation(ctx, s.pool, op); err != nil {
		return err
	}
	if a == nil {
		return ports.ErrUnauthorized
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// A copied manual fence is the only compatibility path. It is not an API for
	// attaching a different policy, unit or target root to somebody else's task.
	hints, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return ports.ErrUnavailable
	}
	var source *db.DownloadRequirement
	for i := range hints {
		r := &hints[i]
		if r.UnitID == unit && r.RootID == root && r.State == "pending" {
			source = r
			break
		}
	}
	if source == nil {
		return ports.ErrDownloadDemandChanged
	}
	var fence ports.DownloadDemandFence
	if json.Unmarshal(source.SubmissionFence, &fence) != nil || fence.SubscriptionID != nil {
		return ports.ErrDownloadDemandChanged
	}
	if _, e = q.LockSearchPolicy(ctx, fence.PolicyID); e != nil {
		return ports.ErrUnavailable
	}
	r, e := q.LockAcquisitionRoot(ctx, root)
	if e != nil || r.Health != "online" || !r.BaselineCompleted || r.Generation != fence.RootGeneration {
		return ports.ErrDownloadDemandChanged
	}
	if _, e = q.LockAcquisitionUnit(ctx, unit); e != nil {
		return ports.ErrUnavailable
	}
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return ports.ErrUnavailable
	}
	if task, e := q.GetDownloadTaskByOpID(ctx, op); e == nil && (task.State == "unknown" || task.State == "failed" || task.State == "cancelled" || task.OrganizationState == "completed") {
		return ports.ErrDownloadDemandChanged
	} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrUnavailable
	}
	reservation, e := q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: unit, RootID: root, Purpose: "manual_acquisition"})
	if e != nil || reservation.OperationID != op {
		return ports.ErrDownloadDemandChanged
	}
	current, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return ports.ErrUnavailable
	}
	stillPending := false
	for _, v := range current {
		if v.ID == source.ID && v.State == "pending" {
			stillPending = true
		}
		if v.ActorType == string(a.Type) && v.ActorID == a.ID && v.UnitID == unit && v.RootID == root && v.State == "pending" {
			return nil
		}
	}
	if !stillPending {
		return ports.ErrDownloadDemandChanged
	}
	fence.ActorID = a.ID
	fence.ActorType = a.Type
	raw, e := json.Marshal(fence)
	if e != nil {
		return ports.ErrUnavailable
	}
	if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: s.ids.NewUUID(), OperationID: op, UnitID: unit, RootID: root, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: raw}); e != nil {
		return ports.ErrUnavailable
	}
	if e = s.demandAudit(ctx, q, a, op, "attached"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *DownloadSubmissionStore) ReleaseRequirement(ctx context.Context, a *ports.Actor, op, unit, root uuid.UUID) (bool, error) {
	return s.releaseRequirement(ctx, a, op, unit, root, nil)
}
func (s *DownloadSubmissionStore) releaseRequirement(ctx context.Context, a *ports.Actor, op, unit, root uuid.UUID, c *ports.RequirementWithdrawalCommand) (bool, error) {
	if err := RejectTVEpisodeUpgradeOperation(ctx, s.pool, op); err != nil {
		return false, err
	}
	if e := acquisitionCommandActor(a); e != nil {
		return false, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return false, e
	}
	hints, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return false, ports.ErrUnavailable
	}
	rootIDs := map[uuid.UUID]bool{}
	unitIDs := map[uuid.UUID]bool{}
	for _, r := range hints {
		rootIDs[r.RootID] = true
		unitIDs[r.UnitID] = true
	}
	for _, id := range sortedUUIDKeys(rootIDs) {
		if _, e = q.LockAcquisitionRoot(ctx, id); e != nil {
			return false, ports.ErrUnavailable
		}
	}
	for _, id := range sortedUUIDKeys(unitIDs) {
		if _, e = q.LockAcquisitionUnit(ctx, id); e != nil {
			return false, ports.ErrUnavailable
		}
	}
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return false, ports.ErrUnavailable
	}
	current, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return false, ports.ErrUnavailable
	}
	owned := false
	for _, r := range current {
		if !rootIDs[r.RootID] || !unitIDs[r.UnitID] {
			return false, ports.ErrDownloadDemandChanged
		}
		if r.UnitID == unit && r.RootID == root && r.ActorType == string(a.Type) && r.ActorID == a.ID {
			owned = true
		}
	}
	if !owned {
		return false, ports.ErrForbidden
	}

	if c != nil {
		var target *db.DownloadRequirement
		for i := range current {
			if current[i].ID == c.RequirementID {
				target = &current[i]
			}
		}
		if target == nil || target.ActorID != a.ID || target.ActorType != string(a.Type) || target.OperationID != c.OperationID {
			return false, ports.ErrForbidden
		}
		lock, _ := json.Marshal([]string{string(a.Type), a.ID, c.Key})
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "requirement.command", AggregateID: string(lock)}); e != nil {
			return false, ports.ErrUnavailable
		}
		old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.Key})
		if e == nil {
			var saved ports.RequirementWithdrawalReceipt
			if old.RequestHash != c.Hash || json.Unmarshal(old.ResponseBody, &saved) != nil || saved.RequirementID != c.RequirementID {
				return false, ports.ErrRequirementConflict
			}
			return saved.PhysicalReleased, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return false, ports.ErrUnavailable
		}
		if target.State == "imported" || requirementFenceDigest(target.SubmissionFence) != c.ExpectedFence {
			return false, ports.ErrRequirementConflict
		}
	}
	if _, e = q.LockSubmissionJobsByOperation(ctx, op.String()); e != nil {
		return false, ports.ErrUnavailable
	}
	task, e := q.GetDownloadTaskByOpID(ctx, op)
	hasTask := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return false, ports.ErrUnavailable
	}
	if e = q.DeleteRequirement(ctx, db.DeleteRequirementParams{OperationID: op, UnitID: unit, RootID: root, ActorID: a.ID, ActorType: string(a.Type)}); e != nil {
		return false, ports.ErrUnavailable
	}
	remaining, e := q.CountRemainingRequirementsForOperation(ctx, op)
	if e != nil {
		return false, ports.ErrUnavailable
	}
	released := false
	// Presence of any physical task means sending may have begun. Tracking,
	// completed, submitting and unknown are not proof that cancellation is safe.
	if remaining == 0 && !hasTask {
		if e = q.CancelUnsentSubmissionJobs(ctx, op.String()); e != nil {
			return false, ports.ErrUnavailable
		}
		if e = q.ReleaseUnsentOperationReservations(ctx, op); e != nil {
			return false, ports.ErrUnavailable
		}
		released = true
	}
	_ = task
	if e = s.demandAudit(ctx, q, a, op, "released"); e != nil {
		return false, e
	}
	if released {
		if e = q.ReleaseFinishedAcquisitionLane(ctx, op); e != nil {
			return false, ports.ErrUnavailable
		}
	}
	if c != nil {
		safe, _ := json.Marshal(ports.RequirementWithdrawalReceipt{OperationID: op, RequirementID: c.RequirementID, State: "released", PhysicalReleased: released})
		if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.Key, RequestHash: c.Hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: c.RequirementID.String(), Valid: true}}); e != nil {
			return false, ports.ErrUnavailable
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return false, ports.ErrUnavailable
	}
	return released, nil
}
func (s *DownloadSubmissionStore) demandAudit(ctx context.Context, q *db.Queries, a *ports.Actor, op uuid.UUID, action string) error {
	safe, _ := json.Marshal(map[string]any{"operation_id": op, "action": action})
	if _, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "download.requirement_" + action, TargetType: "acquisition", TargetID: op.String(), Payload: safe}); e != nil {
		return ports.ErrUnavailable
	}
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "acquisition", AggregateID: op.String()}); e != nil {
		return ports.ErrUnavailable
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "acquisition", AggregateID: op.String()})
	if e != nil {
		return ports.ErrUnavailable
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "acquisition", AggregateID: op.String(), EventType: "download.requirement_" + action, Payload: safe, Seq: int64(seq)}); e != nil {
		return ports.ErrUnavailable
	}
	return nil
}

// CompleteSubmission records the observed result under the original execution
// authority. An expired callback leaves submitting/unknown for R3 observation.
func (s *DownloadSubmissionStore) CompleteSubmission(ctx context.Context, lease ports.JobLease, taskID uuid.UUID, accepted bool) (*ports.DownloadSubmissionReceipt, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	hint, e := q.GetSubmissionTask(ctx, taskID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = RejectTVEpisodeUpgradeOperation(ctx, tx, hint.OperationID); e != nil {
		return nil, e
	}
	if _, e = q.LockDownloadTaskUnits(ctx, taskID); e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = q.LockDownloadAcquisition(ctx, hint.OperationID); e != nil {
		return nil, ports.ErrUnavailable
	}
	task, e := q.LockDownloadTask(ctx, taskID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.LockJobForWrite(ctx, lease.ID); e != nil {
		return nil, ports.ErrUnavailable
	}
	job, e := q.GetAuthorizedSubmissionJob(ctx, db.GetAuthorizedSubmissionJobParams{ID: lease.ID, LeaseOwner: pgtype.Text{String: lease.Owner, Valid: true}, LeaseToken: lease.Token})
	if e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	var payload struct {
		OperationID      uuid.UUID `json:"operation_id"`
		CandidateID      uuid.UUID `json:"candidate_id"`
		EpisodeAuthority uuid.UUID `json:"episode_authority"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || payload.OperationID != task.OperationID || payload.CandidateID != task.CandidateID {
		return nil, ports.ErrDownloadDemandChanged
	}
	if job.Kind == "episode_download_submission" {
		record, e := q.GetEpisodeExecutionAuthority(ctx, task.OperationID)
		if e != nil || payload.EpisodeAuthority != task.OperationID || record.CandidateID != task.CandidateID || record.ClientID != task.ClientID {
			return nil, ports.ErrDownloadDemandChanged
		}
	} else if payload.EpisodeAuthority != uuid.Nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	if task.State == "submitting" {
		if accepted {
			task, e = q.TransitionTaskToTracking(ctx, task.ID)
		} else {
			task, e = q.MarkSubmissionOutcomeUnknown(ctx, task.ID)
		}
		if e != nil {
			return nil, ports.ErrUnavailable
		}
	}
	safe, _ := json.Marshal(map[string]any{"operation_id": task.OperationID, "task_id": task.ID, "state": task.State})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: lease.Owner, Action: "download.submission_observed", TargetType: "download_task", TargetID: task.ID.String(), Payload: safe}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "acquisition", AggregateID: task.OperationID.String()}); e != nil {
		return nil, ports.ErrUnavailable
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "acquisition", AggregateID: task.OperationID.String()})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "acquisition", AggregateID: task.OperationID.String(), Seq: int64(seq), EventType: "download.submission_observed", Payload: safe}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: lease.ID, Column2: lease.Owner, Column3: lease.Token, Column4: safe}); e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return &ports.DownloadSubmissionReceipt{TaskID: task.ID, State: task.State}, nil
}

func sortedUUIDKeys[V any](values map[uuid.UUID]V) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}
func (s *DownloadSubmissionStore) PrepareSubmission(ctx context.Context, c ports.DownloadSubmissionCommand) (receipt *ports.DownloadSubmissionReceipt, err error) {
	if err := RejectTVEpisodeUpgradeOperation(ctx, s.pool, c.OperationID); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrDownloadDemandChanged) {
			err = ports.ErrUnavailable
		}
	}()
	if c.Lease.ID == uuid.Nil || c.Lease.Owner == "" || c.TaskID == uuid.Nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	q := db.New(s.pool)
	initialJob, e := q.GetJobByID(ctx, c.Lease.ID)
	if e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	var initialPayload struct {
		RootID         uuid.UUID `json:"root_id"`
		RootGeneration int64     `json:"root_generation"`
	}
	if json.Unmarshal(initialJob.Payload, &initialPayload) != nil || initialPayload.RootID == uuid.Nil || initialPayload.RootGeneration < 1 {
		return nil, ports.ErrDownloadDemandChanged
	}
	hints, e := q.ListRequirementsForOperation(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	fences := map[uuid.UUID]ports.DownloadDemandFence{}
	policies := map[string]bool{}
	subs := map[uuid.UUID]bool{}
	roots := map[uuid.UUID]bool{initialPayload.RootID: true}
	units := map[uuid.UUID]bool{}
	for _, r := range hints {
		if r.State != "pending" {
			continue
		}
		var f ports.DownloadDemandFence
		if json.Unmarshal(r.SubmissionFence, &f) != nil || f.PolicyID == "" || f.PolicyRevision < 1 {
			return nil, ports.ErrDownloadDemandChanged
		}
		fences[r.ID] = f
		policies[f.PolicyID] = true
		if f.SubscriptionID != nil {
			subs[*f.SubscriptionID] = true
		}
		roots[r.RootID] = true
		units[r.UnitID] = true
	}
	if len(fences) == 0 {
		return nil, ports.ErrDownloadDemandChanged
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q = db.New(tx)
	if e = LockEpisodeExecutionUser(ctx, q, c.OperationID); e != nil {
		return nil, e
	}
	policyIDs := []string{}
	for id := range policies {
		policyIDs = append(policyIDs, id)
	}
	sort.Strings(policyIDs)
	lockedPolicies := map[string]db.Policy{}
	for _, id := range policyIDs {
		p, e := q.LockSearchPolicy(ctx, id)
		if e != nil {
			return nil, e
		}
		lockedPolicies[id] = p
	}
	lockedSubs := map[uuid.UUID]db.Subscription{}
	for _, id := range sortedUUIDKeys(subs) {
		sub, e := q.LockSearchSubscription(ctx, id)
		if e != nil {
			return nil, e
		}
		lockedSubs[id] = sub
	}
	lockedRoots := map[uuid.UUID]db.LibraryRoot{}
	for _, id := range sortedUUIDKeys(roots) {
		root, e := q.LockAcquisitionRoot(ctx, id)
		if e != nil {
			return nil, e
		}
		lockedRoots[id] = root
	}
	mediaIDs := map[uuid.UUID]bool{}
	for _, id := range sortedUUIDKeys(units) {
		u, e := q.LockAcquisitionUnit(ctx, id)
		if e != nil {
			return nil, e
		}
		mediaIDs[u.MediaID] = true
	}
	if e = q.LockDownloadAcquisition(ctx, c.OperationID); e != nil {
		return nil, e
	}
	for _, id := range sortedUUIDKeys(mediaIDs) {
		if _, e = q.LockSubmissionMedia(ctx, id); e != nil {
			return nil, e
		}
	}
	if _, e = q.LockJobForWrite(ctx, c.Lease.ID); e != nil {
		return nil, e
	}
	job, e := q.GetAuthorizedSubmissionJob(ctx, db.GetAuthorizedSubmissionJobParams{ID: c.Lease.ID, LeaseOwner: pgtype.Text{String: c.Lease.Owner, Valid: true}, LeaseToken: c.Lease.Token})
	if e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	var payload struct {
		OperationID, CandidateID, RootID uuid.UUID
		ClientID                         *uuid.UUID
	}
	var raw struct {
		OperationID      uuid.UUID  `json:"operation_id"`
		CandidateID      uuid.UUID  `json:"candidate_id"`
		RootID           uuid.UUID  `json:"root_id"`
		ClientID         *uuid.UUID `json:"client_id"`
		EpisodeAuthority uuid.UUID  `json:"episode_authority"`
	}
	if json.Unmarshal(job.Payload, &raw) != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	episodeSubmission := job.Kind == "episode_download_submission"
	if episodeSubmission && (raw.EpisodeAuthority != c.OperationID || len(fences) != 1 || raw.ClientID == nil) {
		return nil, ports.ErrDownloadDemandChanged
	}
	if episodeSubmission {
		for _, fence := range fences {
			if fence.SubscriptionID == nil || fence.ActorType != ports.ActorTypeUser {
				return nil, ports.ErrDownloadDemandChanged
			}
		}
		record, e := q.GetEpisodeExecutionAuthority(ctx, c.OperationID)
		if e != nil || record.CandidateID != c.CandidateID || record.ClientID != c.ClientID || record.RootID != raw.RootID {
			return nil, ports.ErrDownloadDemandChanged
		}
	}
	if !episodeSubmission && raw.EpisodeAuthority != uuid.Nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	payload.OperationID = raw.OperationID
	payload.CandidateID = raw.CandidateID
	payload.RootID = raw.RootID
	payload.ClientID = raw.ClientID
	physicalRoot := lockedRoots[initialPayload.RootID]
	if payload.RootID != initialPayload.RootID || physicalRoot.Health != "online" || !physicalRoot.BaselineCompleted || physicalRoot.Generation != initialPayload.RootGeneration || physicalRoot.Path != c.SavePath {
		return nil, ports.ErrDownloadDemandChanged
	}
	if payload.OperationID != c.OperationID || payload.CandidateID != c.CandidateID || payload.RootID == uuid.Nil || (payload.ClientID != nil && *payload.ClientID != c.ClientID) {
		return nil, ports.ErrDownloadDemandChanged
	}
	if existing, e := q.GetDownloadTaskByOpID(ctx, c.OperationID); e == nil {
		return &ports.DownloadSubmissionReceipt{TaskID: existing.ID, AlreadyExisted: true}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	hintCandidate, e := q.GetCandidateByID(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	idx, e := q.LockSubmissionIndexer(ctx, hintCandidate.IndexerID)
	if e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	candidate, e := q.LockSubmissionCandidate(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if candidate.IndexerID != idx.ID {
		return nil, ports.ErrDownloadDemandChanged
	}
	if candidate.SourceConfigVersion < 1 {
		return nil, ports.ErrDownloadDemandChanged
	}
	if candidate.SourceConfigVersion > 0 {
		if idx.ConfigVersion != candidate.SourceConfigVersion || idx.Health != "online" {
			return nil, ports.ErrDownloadDemandChanged
		}
	}
	if sha256.Sum256(candidate.LinkEncrypted) != c.CipherHash || (candidate.InfoHash.String != "" && strings.ToLower(candidate.InfoHash.String) != c.InfoHash) {
		return nil, ports.ErrDownloadDemandChanged
	}
	rows, e := q.ListRequirementsForOperation(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	pending := 0
	eligible := 0
	controlCancelled := 0
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	for _, r := range rows {
		if r.State != "pending" {
			continue
		}
		pending++
		f, known := fences[r.ID]
		if !known {
			return nil, ports.ErrDownloadDemandChanged
		}
		if f.RootID != r.RootID || f.UnitID != r.UnitID || f.ActorID != r.ActorID || f.ActorType != ports.ActorType(r.ActorType) {
			return nil, ports.ErrDownloadDemandChanged
		}
		if f.CandidateID != candidate.ID || f.SourceStamp != quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile) {
			return nil, ports.ErrDownloadDemandChanged
		}
		var current ports.DownloadDemandFence
		if json.Unmarshal(r.SubmissionFence, &current) != nil {
			return nil, ports.ErrDownloadDemandChanged
		}
		a, _ := json.Marshal(f)
		b, _ := json.Marshal(current)
		if string(a) != string(b) {
			return nil, ports.ErrDownloadDemandChanged
		}
		root, ok := lockedRoots[r.RootID]
		if !ok || root.Health != "online" || !root.BaselineCompleted || root.Generation != f.RootGeneration {
			continue
		}
		if r.RootID == payload.RootID && root.Path != c.SavePath {
			return nil, ports.ErrDownloadDemandChanged
		}
		unit, e := q.GetSearchUnit(ctx, r.UnitID)
		if e != nil {
			return nil, e
		}
		media, e := q.GetMediaByID(ctx, unit.MediaID)
		if e != nil {
			return nil, e
		}
		if unit.MediaID != f.MediaID || unit.Retired || unit.MetadataVersion != f.UnitMetadataVersion || media.MetadataVersion != f.MediaMetadataVersion {
			continue
		}
		if f.SubscriptionID != nil {
			sub := lockedSubs[*f.SubscriptionID]
			if sub.State != "active" || sub.UserID.String() != r.ActorID || sub.IntentVersion != f.IntentVersion || sub.QualityPolicyID != f.PolicyID || sub.QualityPolicyRevision != f.PolicyRevision || sub.RootID != r.RootID {
				controlCancelled++
				if e = q.DeleteRequirement(ctx, db.DeleteRequirementParams{OperationID: c.OperationID, UnitID: r.UnitID, RootID: r.RootID, ActorID: r.ActorID, ActorType: r.ActorType}); e != nil {
					return nil, e
				}
				continue
			}
			wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
			if e != nil {
				return nil, e
			}
			selected := false
			for _, w := range wanted {
				if w.UnitID == r.UnitID && !w.IsSkipped {
					selected = true
				}
			}
			identities, e := q.ListSearchMediaIdentities(ctx, media.ID)
			if e != nil {
				return nil, e
			}
			identityMap := map[string]string{}
			for _, identity := range identities {
				identityMap[identity.Namespace] = identity.ExternalID
			}
			if !selected {
				controlCancelled++
				if e = q.DeleteRequirement(ctx, db.DeleteRequirementParams{OperationID: c.OperationID, UnitID: r.UnitID, RootID: r.RootID, ActorID: r.ActorID, ActorType: r.ActorType}); e != nil {
					return nil, e
				}
				continue
			}
			if episodeSubmission {
				if e = ValidateEpisodeExecutionInputs(ctx, q, c, sub, root, unit, candidate, f); e != nil {
					return nil, e
				}
			} else if media.MediaType != "movie" || unit.UnitKey != "movie" || (unit.AirDate.Valid && unit.AirDate.Time.After(now.Time)) || !domainMedia.MatchesAutomaticMovie(candidate.Title, media.Title, int(media.Year.Int32), identityMap) {
				continue
			}
		}
		if lockedPolicies[f.PolicyID].CurrentRevision != f.PolicyRevision {
			continue
		}
		revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: f.PolicyID, Revision: f.PolicyRevision})
		if e != nil {
			return nil, e
		}
		var rules quality.PolicyRules
		if json.Unmarshal(revision.Rules, &rules) != nil {
			return nil, ports.ErrDownloadDemandChanged
		}
		if !quality.EvaluateCandidate(rules, candidate.Title, profile).Passed {
			continue
		}
		files, e := q.ListAcquisitionOwnershipEvidence(ctx, db.ListAcquisitionOwnershipEvidenceParams{UnitID: r.UnitID, RootID: r.RootID})
		if e != nil {
			return nil, e
		}
		evidence := []quality.FileEvidence{}
		for _, file := range files {
			var p quality.Profile
			_ = json.Unmarshal(file.QualityProfile, &p)
			evidence = append(evidence, quality.FileEvidence{Title: file.RelPath, Profile: p, Verified: file.Stage.Valid})
		}
		owned := quality.AssessOwnership(rules, evidence)
		if owned.Owned || owned.Unknown {
			continue
		}
		res, e := q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: r.UnitID, RootID: r.RootID, Purpose: "manual_acquisition"})
		if e != nil || res.OperationID != c.OperationID || res.CandidateID != c.CandidateID {
			continue
		}
		eligible++
	}
	if pending != len(fences) {
		return nil, ports.ErrDownloadDemandChanged
	}
	if eligible == 0 && controlCancelled == pending {
		if e = q.CancelUnsentSubmissionJobs(ctx, c.OperationID.String()); e != nil {
			return nil, e
		}
		if e = q.ReleaseUnsentOperationReservations(ctx, c.OperationID); e != nil {
			return nil, e
		}
		if e = q.ReleaseFinishedAcquisitionLane(ctx, c.OperationID); e != nil {
			return nil, e
		}
		if e = s.demandAudit(ctx, q, &ports.Actor{Type: ports.ActorTypeSystem, ID: c.Lease.Owner}, c.OperationID, "cancelled_before_send"); e != nil {
			return nil, e
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &ports.DownloadSubmissionReceipt{Declined: true}, nil
	}
	if eligible == 0 {
		return nil, ports.ErrDownloadDemandChanged
	}
	if c.ClientConfigStamp != "" {
		bound, e := q.GetBoundSubmissionClient(ctx, c.ClientID)
		if e != nil || bound.ConfigStamp != c.ClientConfigStamp {
			return nil, ports.ErrDownloadDemandChanged
		}
	}
	task, e := q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: c.TaskID, OperationID: c.OperationID, ClientID: c.ClientID, CandidateID: c.CandidateID, InfoHash: c.InfoHash, State: "submitting", SavePath: c.SavePath})
	if e != nil {
		return nil, e
	}
	if c.ClientConfigStamp != "" {
		if e = q.CreateDownloadTaskClientBinding(ctx, db.CreateDownloadTaskClientBindingParams{TaskID: task.ID, ClientID: c.ClientID, ClientStamp: c.ClientConfigStamp, JobID: c.Lease.ID, LeaseOwner: c.Lease.Owner, LeaseToken: c.Lease.Token}); e != nil {
			return nil, e
		}
	}
	if e = q.BindPhysicalAcquisitionTask(ctx, db.BindPhysicalAcquisitionTaskParams{OperationID: c.OperationID, TaskID: pgtype.UUID{Bytes: task.ID, Valid: true}}); e != nil {
		return nil, e
	}
	safe, _ := json.Marshal(map[string]any{"operation_id": c.OperationID, "task_id": task.ID, "eligible_requirements": eligible})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: c.Lease.Owner, Action: "download.submission_prepared", TargetType: "download_task", TargetID: task.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.GuardDownloadSubmissionCommit(ctx, db.GuardDownloadSubmissionCommitParams{ID: c.Lease.ID, Owner: c.Lease.Owner, Token: c.Lease.Token}); e != nil {
		return nil, ports.ErrDownloadDemandChanged
	}
	if e = GuardEpisodeNumberingTime(ctx, q, c.OperationID); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.DownloadSubmissionReceipt{TaskID: task.ID}, nil
}
