package postgres

import (
	"bytes"
	"context"
	"errors"
	"path"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UntrackedOrganizationStore struct {
	pool   *pgxpool.Pool
	ids    ports.IDGen
	clock  ports.Clock
	review *UntrackedReviewStore
}

var _ ports.UntrackedOrganizationRepository = (*UntrackedOrganizationStore)(nil)

func NewUntrackedOrganizationStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *UntrackedOrganizationStore {
	return &UntrackedOrganizationStore{pool: pool, ids: ids, clock: clock, review: NewUntrackedReviewStore(pool, ids, clock)}
}

func (s *UntrackedOrganizationStore) begin(ctx context.Context, a *ports.Actor, read bool) (pgx.Tx, error) {
	if s == nil || s.review == nil {
		return nil, ports.ErrUnavailable
	}
	return s.review.begin(ctx, a, read)
}

func organizationKey(ctx context.Context, q *db.Queries, owner, scope, file uuid.UUID, key string) (*ports.UntrackedOrganizationReceipt, error) {
	raw, err := q.GetUntrackedOrganizationKey(ctx, db.GetUntrackedOrganizationKeyParams{ScopeID: scope, FileID: file, CommandKey: key})
	if err != nil {
		return nil, err
	}
	r, err := organizationDecode(raw)
	if err != nil {
		return nil, err
	}
	if err = organizationOriginal(ctx, q, r, owner, scope, file); err != nil {
		return nil, err
	}
	return &r.Receipt, nil
}

func (s *UntrackedOrganizationStore) PrepareUntrackedOrganization(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, in ports.UntrackedOrganizationInput, key string) (out *ports.UntrackedOrganizationPreparation, err error) {
	defer func() { err = organizationError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || in.ConfirmationID == uuid.Nil || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner, q := uuid.MustParse(a.ID), db.New(tx)
	// Fresh isolation/account and current ownership precede original-key recovery.
	if _, _, err = reviewOwned(ctx, tx, owner, scope, file, false); err != nil {
		return nil, err
	}
	old, err := organizationKey(ctx, q, owner, scope, file, key)
	if err == nil {
		if old.Operation.ConfirmationID != in.ConfirmationID {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return &ports.UntrackedOrganizationPreparation{Receipt: old}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, _, err = reviewOwned(ctx, tx, owner, scope, file, true); err != nil {
		return nil, err
	}
	// A waiter must reread the original key after obtaining the file mutex.
	old, err = organizationKey(ctx, q, owner, scope, file, key)
	if err == nil {
		if old.Operation.ConfirmationID != in.ConfirmationID {
			return nil, ports.ErrEndpointVersionConflict
		}
		return &ports.UntrackedOrganizationPreparation{Receipt: old}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	confirmation, err := organizationConfirmation(ctx, q, owner, scope, file, in.ConfirmationID)
	if err != nil {
		return nil, err
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return nil, err
	}
	o := ports.UntrackedOrganizationOperation{ID: s.ids.NewUUID(), ActorID: owner, ScopeID: scope, FileID: file, ConfirmationID: in.ConfirmationID, PlanID: s.ids.NewUUID(), StepID: s.ids.NewUUID(), JobID: s.ids.NewUUID(), ScopeVersion: confirmation.Material.Scope.Version, FileRevision: confirmation.Material.File.Revision, CreatedAt: now.Time}
	m := ports.UntrackedOrganizationMaterial{Operation: o, Confirmation: reviewClone(*confirmation)}
	m.Operation.MaterialHash = organizationMaterialHash(m)
	if err = organizationCurrent(ctx, tx, m); err != nil {
		return nil, err
	}
	candidate := *m.Confirmation.Material.Review.Evidence.Candidate
	if err = organizationIdentityLock(ctx, q, candidate); err != nil {
		return nil, err
	}
	metadata, err := organizationMetadataSeal(ctx, q, candidate)
	if err != nil {
		return nil, err
	}
	c := &untrackedOrganizationCapture{store: s, actor: *a, material: reviewClone(m), key: key, inputHash: watchDigest(in), deadline: now.Time.Add(untrackedScopeTTL), issuedLocal: s.clock.Now(), metadata: append([]byte(nil), metadata...)}
	c.seal = organizationCaptureSeal(c)
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.UntrackedOrganizationPreparation{Capture: c}, nil
}

func organizationCaptureSeal(c *untrackedOrganizationCapture) string {
	return watchDigest(struct {
		Actor     ports.Actor
		Material  ports.UntrackedOrganizationMaterial
		Key, Hash string
		Metadata  []byte
	}{c.actor, c.material, c.key, c.inputHash, c.metadata})
}

func (s *UntrackedOrganizationStore) captureCurrent(ctx context.Context, tx pgx.Tx, c *untrackedOrganizationCapture) error {
	if c.seal != organizationCaptureSeal(c) || c.actor.ID != c.material.Operation.ActorID.String() ||
		c.inputHash != watchDigest(ports.UntrackedOrganizationInput{ConfirmationID: c.material.Operation.ConfirmationID}) ||
		!c.deadline.Equal(c.material.Operation.CreatedAt.Add(untrackedScopeTTL)) {
		return ports.ErrRejected
	}
	if err := organizationCurrent(ctx, tx, c.material); err != nil {
		return err
	}
	now, err := db.New(tx).UntrackedDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	elapsed := s.clock.Since(c.issuedLocal)
	if !now.Time.Before(c.deadline) || elapsed < 0 || elapsed >= untrackedScopeTTL {
		return ports.ErrUntrackedOrganizationStale
	}
	return nil
}

func (s *UntrackedOrganizationStore) CommitUntrackedOrganization(ctx context.Context, in ports.UntrackedOrganizationCapture) (out *ports.UntrackedOrganizationReceipt, err error) {
	defer func() { err = organizationError(ctx, err) }()
	c, ok := in.(*untrackedOrganizationCapture)
	if !ok || c == nil || s == nil || c.store != s || c.seal != organizationCaptureSeal(c) {
		return nil, ports.ErrRejected
	}
	a, err := reviewActor(&c.actor)
	if err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	m, q := c.material, db.New(tx)
	o := m.Operation
	if _, _, err = reviewOwned(ctx, tx, o.ActorID, o.ScopeID, o.FileID, true); err != nil {
		return nil, err
	}
	old, err := organizationKey(ctx, q, o.ActorID, o.ScopeID, o.FileID, c.key)
	if err == nil {
		if old.Operation.ConfirmationID != o.ConfirmationID {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err = s.captureCurrent(ctx, tx, c); err != nil {
		return nil, err
	}
	candidate := *m.Confirmation.Material.Review.Evidence.Candidate
	if err = organizationIdentityLock(ctx, q, candidate); err != nil {
		return nil, err
	}
	before, err := organizationMetadataSeal(ctx, q, candidate)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(before, c.metadata) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	units, err := s.publishUnits(ctx, tx, m)
	if err != nil {
		return nil, err
	}
	primary := units[0]
	cm := m.Confirmation.Material
	scope := cm.Scope
	f := cm.File
	out = &ports.UntrackedOrganizationReceipt{CommandKey: c.key, Operation: o, State: "queued", ExecutionAllowed: false}
	created := pgtype.Timestamptz{Time: o.CreatedAt, Valid: true}
	if err = q.CreateUntrackedOrganizationPlan(ctx, db.CreateUntrackedOrganizationPlanParams{ID: o.PlanID, OperationID: o.ID, RootID: scope.TargetRootID, Strategy: cm.Input.Action, RootPath: scope.TargetRootPath, RootGeneration: scope.TargetRootGeneration, RootDevice: scope.TargetIdentity.Device, RootInode: scope.TargetIdentity.Inode, CreatedAt: created}); err != nil {
		return nil, err
	}
	if err = q.CreateUntrackedOrganizationStep(ctx, db.CreateUntrackedOrganizationStepParams{ID: o.StepID, PlanID: o.PlanID, UnitID: primary.UnitID, SourcePath: path.Join(scope.LocalRoot, f.RelativePath), TargetPath: path.Join(scope.TargetRootPath, cm.Input.TargetRelativePath), Action: cm.Input.Action, SourceSize: f.Identity.Size, SourceMtime: f.Identity.Mtime, SourceRoot: scope.LocalRoot, SourceRel: f.RelativePath, TargetRel: cm.Input.TargetRelativePath, SourceDevice: f.Identity.Device, SourceInode: f.Identity.Inode, QualityProfile: organizationQuality(m), TargetEvidence: organizationEvidence(m, primary), CreatedAt: created}); err != nil {
		return nil, err
	}
	if err = q.CreateUntrackedOrganizationJob(ctx, db.CreateUntrackedOrganizationJobParams{ID: o.JobID, OperationID: o.ID}); err != nil {
		return nil, err
	}
	if err = q.InsertUntrackedOrganization(ctx, db.InsertUntrackedOrganizationParams{ID: o.ID, OwnerID: o.ActorID, ScopeID: o.ScopeID, FileID: o.FileID, ConfirmationID: o.ConfirmationID, PlanID: o.PlanID, StepID: o.StepID, JobID: o.JobID, CommandKey: c.key, InputHash: c.inputHash, Material: reviewJSON(m), Receipt: reviewJSON(out)}); err != nil {
		return nil, err
	}
	for _, u := range units {
		if err = q.InsertUntrackedOrganizationUnit(ctx, db.InsertUntrackedOrganizationUnitParams{OperationID: o.ID, UnitID: u.UnitID, MediaID: u.MediaID, MetadataVersion: u.MetadataVersion, MediaMetadataVersion: u.MediaMetadataVersion, UnitKey: u.UnitKey, TmdbEpisodeID: u.TMDBEpisodeID, CandidateUnit: reviewJSON(u.CandidateUnit), MediaIdentity: reviewJSON(u.MediaIdentity)}); err != nil {
			return nil, err
		}
	}
	aid, eid, err := s.audit(ctx, q, a, o.ID, reviewJSON(out))
	if err != nil {
		return nil, err
	}
	recheck := func() error {
		if e := s.captureCurrent(ctx, tx, c); e != nil {
			return e
		}
		r, e := organizationLoad(ctx, q, o.ID, false)
		if e != nil {
			return e
		}
		if !reviewJSONSame(reviewJSON(r.Material), reviewJSON(m)) || !reviewJSONSame(reviewJSON(r.Receipt), reviewJSON(out)) {
			return ports.ErrUntrackedOrganizationStale
		}
		frozen, e := organizationFrozenUnits(ctx, q, m)
		if e != nil {
			return e
		}
		if e = organizationValidateUnits(ctx, q, m, frozen, true); e != nil {
			return e
		}
		p, steps, e := organizationPlan(ctx, tx, r, frozen, true)
		if e != nil {
			return e
		}
		if p.State != "approved" || steps.State != "pending" || p.ExecutionOwner.Valid || p.ExecutionToken != 0 || p.LeaseUntil.Valid || steps.TargetFileID.Valid || steps.Error.Valid {
			return ports.ErrUntrackedOrganizationStale
		}
		j, e := q.GetJobByID(ctx, o.JobID)
		if e != nil {
			return e
		}
		if e = organizationJob(j, o, ports.JobLease{}, "queued"); e != nil {
			return e
		}
		return nil
	}
	if err = recheck(); err != nil {
		return nil, err
	}
	seal := db.UntrackedOrganizationSealParams{OperationID: o.ID, AuditID: aid, EventID: eid}
	first, err := q.UntrackedOrganizationSeal(ctx, seal)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = recheck(); err != nil {
		return nil, err
	}
	last, err := q.UntrackedOrganizationSeal(ctx, seal)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, last) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if err = recheck(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UntrackedOrganizationStore) audit(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID, payload []byte) (uuid.UUID, uuid.UUID, error) {
	aid, eid := s.ids.NewUUID(), s.ids.NewUUID()
	if aid == uuid.Nil || eid == uuid.Nil || aid == eid {
		return uuid.Nil, uuid.Nil, ports.ErrUnavailable
	}
	const action = "untracked.organization.queued"
	audit, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: aid, ActorType: string(a.Type), ActorID: a.ID, Action: action, TargetType: "untracked_organization", TargetID: id.String(), Payload: payload})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "untracked_organization", AggregateID: id.String()})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	event, err := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: eid, AggregateType: "untracked_organization", AggregateID: id.String(), EventType: action, Seq: int64(seq), Payload: payload})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if audit.ID != aid || audit.ActorType != string(a.Type) || audit.ActorID != a.ID || audit.Action != action || audit.TargetType != "untracked_organization" || audit.TargetID != id.String() || !audit.CreatedAt.Valid || !reviewJSONSame(audit.Payload, payload) || event.ID != eid || event.AggregateType != "untracked_organization" || event.AggregateID != id.String() || event.EventType != action || event.Seq != int64(seq) || !event.CreatedAt.Valid || !reviewJSONSame(event.Payload, payload) {
		return uuid.Nil, uuid.Nil, ports.ErrUntrackedOrganizationStale
	}
	return aid, eid, nil
}

func organizationLoad(ctx context.Context, q *db.Queries, id uuid.UUID, lock bool) (untrackedOrganizationRow, error) {
	var raw []byte
	var err error
	if lock {
		raw, err = q.LockUntrackedOrganization(ctx, id)
	} else {
		raw, err = q.GetUntrackedOrganizationByID(ctx, id)
	}
	if err != nil {
		return untrackedOrganizationRow{}, err
	}
	return organizationDecode(raw)
}

func (s *UntrackedOrganizationStore) ReadUntrackedOrganizationReceipt(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, key string) (out *ports.UntrackedOrganizationReceipt, err error) {
	defer func() { err = organizationError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner := uuid.MustParse(a.ID)
	if _, _, err = reviewOwned(ctx, tx, owner, scope, file, false); err != nil {
		return nil, err
	}
	out, err = organizationKey(ctx, db.New(tx), owner, scope, file, key)
	if err != nil {
		return nil, err
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *UntrackedOrganizationStore) ReadUntrackedOrganization(ctx context.Context, actor *ports.Actor, scope, file, id uuid.UUID) (out *ports.UntrackedOrganizationView, err error) {
	defer func() { err = organizationError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner, q := uuid.MustParse(a.ID), db.New(tx)
	if _, _, err = reviewOwned(ctx, tx, owner, scope, file, false); err != nil {
		return nil, err
	}
	r, err := organizationLoad(ctx, q, id, false)
	if err != nil {
		return nil, err
	}
	if err = organizationOriginal(ctx, q, r, owner, scope, file); err != nil {
		return nil, err
	}
	j, err := q.GetJobByID(ctx, r.JobID)
	if err != nil {
		return nil, err
	}
	p, err := q.GetTransferPlanByID(ctx, r.PlanID)
	if err != nil {
		return nil, err
	}
	out = &ports.UntrackedOrganizationView{Operation: r.Material.Operation, State: "queued", ExecutionAllowed: false}
	raw, e := q.GetUntrackedOrganizationCompletion(ctx, id)
	if e == nil {
		var c struct {
			OperationID   uuid.UUID          `json:"operation_id"`
			StepID        uuid.UUID          `json:"step_id"`
			FileID        uuid.UUID          `json:"file_id"`
			ObservationID uuid.UUID          `json:"observation_id"`
			Result        map[string]any     `json:"result"`
			RecordedAt    pgtype.Timestamptz `json:"recorded_at"`
		}
		if checkinDecode(raw, &c) != nil || c.OperationID != id || c.StepID != r.StepID || c.FileID == uuid.Nil || !c.RecordedAt.Valid || p.State != "completed" || j.State != "succeeded" {
			return nil, ports.ErrUntrackedOrganizationStale
		}
		out.State = "completed"
		out.CompletedFileID = &c.FileID
		t := c.RecordedAt.Time
		out.RecordedAt = &t
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	} else {
		switch {
		case p.State == "review_required" || j.State == "unknown":
			out.State = "review_required"
			out.ReviewRequired = true
		case p.State == "failed" || j.State == "failed" || j.State == "dead" || j.State == "cancelled":
			out.State = "failed"
		case p.State == "executing" || j.State == "running":
			out.State = "running"
		case p.State == "completed" || j.State == "succeeded":
			out.State = "review_required"
			out.ReviewRequired = true
		}
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
