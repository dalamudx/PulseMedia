package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

var _ ports.RequirementWithdrawalRepository = (*DownloadSubmissionStore)(nil)

func requirementFenceDigest(raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		value = map[string]any{}
	}
	canonical, _ := json.Marshal(value)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
func (s *DownloadSubmissionStore) ListOwnRequirements(ctx context.Context, a *ports.Actor, id uuid.UUID) ([]ports.RequirementInfo, error) {
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
	rows, e := q.ListRequirementsForOperation(ctx, id)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := []ports.RequirementInfo{}
	for _, r := range rows {
		if r.ActorType != string(a.Type) || r.ActorID != a.ID {
			continue
		}
		out = append(out, ports.RequirementInfo{ID: r.ID, OperationID: r.OperationID, UnitID: r.UnitID, RootID: r.RootID, State: r.State, FenceDigest: requirementFenceDigest(r.SubmissionFence)})
	}
	if len(out) == 0 {
		return nil, ports.ErrForbidden
	}
	return out, nil
}
func (s *DownloadSubmissionStore) WithdrawRequirement(ctx context.Context, a *ports.Actor, c ports.RequirementWithdrawalCommand) (*ports.RequirementWithdrawalReceipt, error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	q := db.New(s.pool)
	r, e := q.GetDownloadRequirement(ctx, c.RequirementID)
	if e != nil || r.OperationID != c.OperationID || r.ActorType != string(a.Type) || r.ActorID != a.ID {
		return nil, ports.ErrForbidden
	}
	released, e := s.releaseRequirement(ctx, a, r.OperationID, r.UnitID, r.RootID, &c)
	if e != nil {
		return nil, e
	}
	return &ports.RequirementWithdrawalReceipt{OperationID: r.OperationID, RequirementID: r.ID, State: "released", PhysicalReleased: released}, nil
}
