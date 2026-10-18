package transfer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Confirmation freezes the actor and root namespace. The controlled worker
// seals Content under its execution fence before any destination publication.
type fileActionEvidence struct {
	Version          int                    `json:"version"`
	ActorID          uuid.UUID              `json:"actor_id"`
	SourceRootDevice int64                  `json:"source_root_device"`
	SourceRootInode  int64                  `json:"source_root_inode"`
	Content          *ports.TransferContent `json:"-"`
}

func verifiedAction(action string) bool {
	return action == "copy" || action == "reflink" || action == "move"
}

func readFileAction(plan db.TransferPlan, step db.TransferStep) (*fileActionEvidence, error) {
	if plan.Strategy == "hardlink" && step.Action == "hardlink" {
		return nil, nil
	}
	if !verifiedAction(plan.Strategy) || step.Action != plan.Strategy || !plan.RequiresApproval || plan.OldFileID.Valid {
		return nil, ErrReviewRequired
	}
	var evidence transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &evidence) != nil || evidence.UnitKey != "movie" || evidence.EpisodeOperationID != uuid.Nil || evidence.CompleteTVPlanID != uuid.Nil {
		return nil, ErrReviewRequired
	}
	a := evidence.FileAction
	if a == nil || a.Version != 1 || a.ActorID == uuid.Nil || a.SourceRootDevice < 0 || a.SourceRootInode <= 0 {
		return nil, ErrReviewRequired
	}
	return a, nil
}

// Runtime evidence is sealed independently; frozen target JSON stays unchanged.
func loadFileAction(ctx context.Context, q *db.Queries, plan db.TransferPlan, step db.TransferStep) (*fileActionEvidence, error) {
	a, err := readFileAction(plan, step)
	if err != nil || a == nil {
		return a, err
	}
	raw, err := q.GetVerifiedTransferContent(ctx, step.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	var proof ports.TransferContent
	if json.Unmarshal(raw, &proof) != nil || proof.File != frozen(step) || proof.SHA256 == [32]byte{} {
		return nil, ErrReviewRequired
	}
	a.Content = &proof
	return a, nil
}

// Called before root/unit locks. These additional preconditions apply only to
// the new explicitly selected actions; existing hardlink actors are unchanged.
func requireFileActionActor(ctx context.Context, q *db.Queries, plan db.TransferPlan, step db.TransferStep, imported bool) (*fileActionEvidence, error) {
	a, err := loadFileAction(ctx, q, plan, step)
	if err != nil || a == nil {
		return a, err
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: a.ActorID.String(), Role: ports.RoleAdmin}
	if _, err = recoveryUser(ctx, q, actor); err != nil {
		if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
			return nil, ErrReviewRequired
		}
		return nil, err
	}
	requirements, err := q.ListRequirementsForOperation(ctx, plan.OperationID)
	if err != nil {
		return nil, err
	}
	for _, r := range requirements {
		if r.ActorType == string(ports.ActorTypeUser) && r.ActorID == actor.ID && r.RootID == plan.RootID && r.UnitID == step.UnitID && (r.State == "pending" || imported && r.State == "imported") {
			return a, nil
		}
	}
	return nil, ErrReviewRequired
}

func fileActionPlanStamp(p db.TransferPlan) string {
	return recoveryHash([]any{p.ID, p.OperationID, p.RootID, p.RequiresApproval, p.Strategy, p.RootPath, p.RootGeneration, p.RootDevice, p.RootInode, p.OldFileID})
}
func fileActionStepStamp(s db.TransferStep) string {
	return recoveryHash([]any{s.ID, s.PlanID, s.UnitID, s.SourcePath, s.TargetPath, s.Action, s.StepOrder, s.SourceSize, s.SourceMtime, s.SourceRoot, s.SourceRel, s.TargetRel, s.SourceDevice, s.SourceInode, json.RawMessage(s.QualityProfile), json.RawMessage(s.TargetEvidence)})
}

// The plan is already guarded under its row lock. A separate step lock keeps
// frozen content and confirmation inputs stable through the whole transaction.
func guardFileActionSnapshot(ctx context.Context, q *db.Queries, plan db.TransferPlan, step db.TransferStep) error {
	if plan.Strategy == "hardlink" {
		return nil
	}
	currentPlan, err := q.GetTransferPlanByID(ctx, plan.ID)
	if err != nil {
		return err
	}
	current, err := q.LockVerifiedTransferStep(ctx, step.ID)
	if err != nil {
		return err
	}
	if fileActionPlanStamp(currentPlan) != fileActionPlanStamp(plan) || fileActionStepStamp(current) != fileActionStepStamp(step) {
		return ErrReviewRequired
	}
	_, err = readFileAction(currentPlan, current)
	return err
}

func (s *Service) freezeFileActionContent(ctx context.Context, plan db.TransferPlan, step db.TransferStep, proof ports.TransferContent, authority []ports.JobLease) (db.TransferStep, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return step, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	a, err := requireFileActionActor(ctx, q, plan, step, false)
	if err != nil || a == nil || a.Content != nil || proof.File != frozen(step) || proof.SHA256 == [32]byte{} {
		if err != nil {
			return step, err
		}
		return step, ErrReviewRequired
	}
	if err = guardOrganization(ctx, q, authority); err != nil {
		return step, err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return step, ErrExecutionRejected
	}
	root, err := q.LockTransferRoot(ctx, plan.RootID)
	if err != nil {
		return step, err
	}
	if root.MediaType != "movie" || root.Health != "online" || !root.BaselineCompleted || root.Path != plan.RootPath.String || root.Generation != plan.RootGeneration.Int64 {
		return step, ErrReviewRequired
	}
	if err = lockTransferAcquisition(ctx, q, plan.OperationID, step.UnitID, step.TargetEvidence); err != nil {
		return step, err
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return step, err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return step, err
	}
	_, err = q.FreezeVerifiedTransferContent(ctx, db.FreezeVerifiedTransferContentParams{ID: step.ID, Content: raw})
	if errors.Is(err, pgx.ErrNoRows) {
		return step, ErrReviewRequired
	}
	if err != nil {
		return step, err
	}
	if err = guardOrganization(ctx, q, authority); err != nil {
		return step, err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return step, ErrExecutionRejected
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return step, err
	}
	if err = tx.Commit(ctx); err != nil {
		return step, err
	}
	return step, nil
}

func (s *Service) requireFileActionCompletion(ctx context.Context, q *db.Queries, plan db.TransferPlan) error {
	if plan.Strategy == "hardlink" {
		return nil
	}
	steps, err := q.ListStepsForPlan(ctx, plan.ID)
	if err != nil {
		return err
	}
	if len(steps) != 1 || steps[0].State != "completed" {
		return ErrReviewRequired
	}
	_, err = requireFileActionActor(ctx, q, plan, steps[0], true)
	return err
}
