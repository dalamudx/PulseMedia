package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UntrackedCompanionStore struct{ parent *UntrackedOrganizationStore }

func NewUntrackedCompanionStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *UntrackedCompanionStore {
	return &UntrackedCompanionStore{NewUntrackedOrganizationStore(pool, ids, clock)}
}

var _ ports.UntrackedCompanionRepository = (*UntrackedCompanionStore)(nil)

func (s *UntrackedCompanionStore) begin(ctx context.Context, actor *ports.Actor, operation uuid.UUID, current bool) (pgx.Tx, ports.UntrackedOrganizationMaterial, error) {
	var zero ports.UntrackedOrganizationMaterial
	a, err := reviewActor(actor)
	if err != nil {
		return nil, zero, err
	}
	if operation == uuid.Nil || s == nil || s.parent == nil {
		return nil, zero, ports.ErrRejected
	}
	tx, err := s.parent.begin(ctx, a, !current)
	if err != nil {
		return nil, zero, err
	}
	m, err := s.guard(ctx, tx, a, operation, current)
	if err != nil {
		managementRollback(ctx, tx)
		return nil, zero, err
	}
	return tx, m, nil
}
func (s *UntrackedCompanionStore) guard(ctx context.Context, tx pgx.Tx, actor *ports.Actor, operation uuid.UUID, current bool) (ports.UntrackedOrganizationMaterial, error) {
	var zero ports.UntrackedOrganizationMaterial
	q := db.New(tx)
	r, err := organizationLoad(ctx, q, operation, false)
	if err != nil {
		return zero, organizationError(ctx, err)
	}
	owner := uuid.MustParse(actor.ID)
	if r.OwnerID != owner {
		return zero, ports.ErrNotFound
	}
	if current {
		if err = organizationCurrent(ctx, tx, r.Material); err != nil {
			return zero, err
		}
		// Parent operation serializes selection and execution against its immutable intent.
		if _, err = organizationLoad(ctx, q, operation, true); err != nil {
			return zero, err
		}
	} else {
		if _, _, err = reviewOwned(ctx, tx, owner, r.ScopeID, r.FileID, false); err != nil {
			return zero, err
		}
	}
	if err = organizationOriginal(ctx, q, r, owner, r.ScopeID, r.FileID); err != nil {
		return zero, err
	}
	units, err := organizationFrozenUnits(ctx, q, r.Material)
	if err != nil {
		return zero, err
	}
	if current {
		if err = organizationValidateUnits(ctx, q, r.Material, units, true); err != nil {
			return zero, err
		}
	}
	p, step, err := organizationPlan(ctx, tx, r, units, current)
	if err != nil {
		return zero, err
	}
	j, err := q.GetJobByID(ctx, r.JobID)
	if err != nil {
		return zero, err
	}
	if p.State != "completed" || j.State != "succeeded" || !j.LeaseOwner.Valid {
		return zero, ports.ErrRejected
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	if err = organizationPermitCurrent(ctx, q, r, p, lease, true); err != nil {
		return zero, err
	}
	if current {
		if err = organizationCompletionCurrent(ctx, q, r, p, step, lease); err != nil {
			return zero, err
		}
	}
	return r.Material, nil
}
func (s *UntrackedCompanionStore) commit(ctx context.Context, tx pgx.Tx, a *ports.Actor, op uuid.UUID, current bool) error {
	if current {
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
	}
	if _, err := s.guard(ctx, tx, a, op, current); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *UntrackedCompanionStore) CompanionMaterial(ctx context.Context, a *ports.Actor, op uuid.UUID) (ports.UntrackedOrganizationMaterial, error) {
	tx, m, err := s.begin(ctx, a, op, true)
	if err != nil {
		return m, err
	}
	defer managementRollback(ctx, tx)
	return m, s.commit(ctx, tx, a, op, true)
}
func companionList(ctx context.Context, tx pgx.Tx, op uuid.UUID) (*ports.CompanionView, error) {
	out := &ports.CompanionView{VideoState: "completed", Items: []ports.CompanionAttachment{}}
	rows, err := db.New(tx).ListUntrackedCompanions(ctx, op)
	if err != nil {
		return nil, err
	}
	for _, raw := range rows {
		var item ports.CompanionAttachment
		if json.Unmarshal(raw, &item) != nil {
			return nil, ports.ErrRejected
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (s *UntrackedCompanionStore) ReadCompanions(ctx context.Context, a *ports.Actor, op uuid.UUID) (*ports.CompanionView, error) {
	tx, _, err := s.begin(ctx, a, op, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, err := companionList(ctx, tx, op)
	if err != nil {
		return nil, err
	}
	return out, s.commit(ctx, tx, a, op, false)
}

// Nil means no command with this key exists. Historical recovery does not read the filesystem.
func (s *UntrackedCompanionStore) ReplayCompanionSelection(ctx context.Context, a *ports.Actor, op uuid.UUID, key, hash string) (*ports.CompanionView, error) {
	tx, _, err := s.begin(ctx, a, op, false)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	old, err := db.New(tx).ReadUntrackedCompanionSelection(ctx, reviewJSON(map[string]any{"operation_id": op, "command_key": key}))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.commit(ctx, tx, a, op, false)
	}
	if err != nil {
		return nil, err
	}
	if old != hash {
		return nil, ports.ErrEndpointVersionConflict
	}
	out, err := companionList(ctx, tx, op)
	if err != nil {
		return nil, err
	}
	return out, s.commit(ctx, tx, a, op, false)
}
func (s *UntrackedCompanionStore) SelectCompanions(ctx context.Context, a *ports.Actor, op uuid.UUID, key, hash string, items []ports.CompanionCandidate, action string) (*ports.CompanionView, error) {
	if !doubanSyncASCII(key, 160, false) || !reviewHashValid(hash) || len(items) < 1 || len(items) > 32 || (action != "copy" && action != "hardlink") {
		return nil, ports.ErrRejected
	}
	tx, _, err := s.begin(ctx, a, op, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	old, err := db.New(tx).ReadUntrackedCompanionSelection(ctx, reviewJSON(map[string]any{"operation_id": op, "command_key": key}))
	if err == nil && old != hash {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		for _, item := range items {
			if err = db.New(tx).InsertUntrackedCompanion(ctx, reviewJSON(map[string]any{"id": s.parent.ids.NewUUID(), "operation_id": op, "command_key": key, "selection_hash": hash, "candidate": item, "action": action})); err != nil {
				return nil, organizationError(ctx, err)
			}
		}
		if err = s.companionAudit(ctx, tx, a, op, "untracked.companions.selected", map[string]any{"count": len(items), "selection_hash": hash}); err != nil {
			return nil, err
		}
	}
	out, err := companionList(ctx, tx, op)
	if err != nil {
		return nil, err
	}
	return out, s.commit(ctx, tx, a, op, true)
}
func (s *UntrackedCompanionStore) ClaimCompanion(ctx context.Context, a *ports.Actor, op, id uuid.UUID) (*ports.CompanionExecution, error) {
	tx, _, err := s.begin(ctx, a, op, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out := &ports.CompanionExecution{Attachment: ports.CompanionAttachment{ID: id, OperationID: op, SourceRetained: true}, Owner: s.parent.ids.NewUUID()}
	// Physical root locks are acquired before this transaction. Existing permits only authorize observation.
	raw, err := db.New(tx).ClaimUntrackedCompanion(ctx, companionExecutionDocument(*out, "running"))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err != nil {
		return nil, err
	}
	var row companionExecutionRow
	if json.Unmarshal(raw, &row) != nil {
		return nil, ports.ErrRejected
	}
	out.Attachment.Candidate = row.Candidate
	out.Attachment.Action = row.Action
	out.Attachment.State = "running"
	out.Token = row.ExecutionToken
	out.Content = row.Content
	return out, s.executionCommit(ctx, tx, a, *out, "running")
}
func (s *UntrackedCompanionStore) mutate(ctx context.Context, a *ports.Actor, e ports.CompanionExecution, proof *ports.TransferContent, result *ports.VerifiedTransferResult, finish bool) error {
	op, id := e.Attachment.OperationID, e.Attachment.ID
	tx, _, err := s.begin(ctx, a, op, true)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	row, err := companionExecutionCurrent(ctx, tx, e, "running")
	if err != nil {
		return err
	}
	state := "running"
	q := db.New(tx)
	doc := companionExecutionValues(e, state)
	var count int64
	if !finish {
		if proof == nil || proof.File != e.Attachment.Candidate.Identity {
			return ports.ErrRejected
		}
		doc["content"] = proof
		count, err = q.SealUntrackedCompanion(ctx, reviewJSON(doc))
	} else {
		state = "unknown"
		if row.Content == nil {
			state = "review_required"
		}
		if result != nil {
			p := row.Content
			if p == nil || p.File != e.Attachment.Candidate.Identity || result.SourceRemoved || result.Target.SHA256 != p.SHA256 || result.Target.File.Size != p.File.Size || (e.Attachment.Action == "hardlink" && result.Target.File != p.File) {
				return ports.ErrRejected
			}
			state = "completed"
		}
		doc["state"] = state
		doc["result"] = result
		count, err = q.FinishUntrackedCompanion(ctx, reviewJSON(doc))
	}
	if err != nil {
		return err
	}
	if count != 1 {
		return ports.ErrTransferExecutionRejected
	}
	if finish && result != nil {
		if err = s.companionAudit(ctx, tx, a, id, "untracked.companion.completed", map[string]any{"attachment_id": id, "operation_id": op, "state": state, "result": result}); err != nil {
			return err
		}
	}
	return s.executionCommit(ctx, tx, a, e, state)
}
func (s *UntrackedCompanionStore) SealCompanion(ctx context.Context, a *ports.Actor, e ports.CompanionExecution, p ports.TransferContent) error {
	return s.mutate(ctx, a, e, &p, nil, false)
}
func (s *UntrackedCompanionStore) FinishCompanion(ctx context.Context, a *ports.Actor, e ports.CompanionExecution, r *ports.VerifiedTransferResult) error {
	return s.mutate(ctx, a, e, nil, r, true)
}
