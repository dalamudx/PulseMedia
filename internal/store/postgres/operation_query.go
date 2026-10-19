package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OperationQueryStore struct {
	pool     *pgxpool.Pool
	activity ports.OperationQuery
}

func NewOperationQueryStore(pool *pgxpool.Pool) *OperationQueryStore {
	return &OperationQueryStore{pool: pool}
}
func (s *OperationQueryStore) ReadOperation(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.OperationSnapshot, error) {
	if s != nil && s.activity != nil {
		return s.activity.ReadOperation(ctx, a, id)
	}
	if a == nil || a.Type != ports.ActorTypeUser {
		return nil, ports.ErrForbidden
	}
	if len(a.Scopes) > 0 {
		read := false
		for _, scope := range a.Scopes {
			if scope == "read" {
				read = true
			}
		}
		if !read {
			return nil, ports.ErrForbidden
		}
	}
	if s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if a.Type == ports.ActorTypeUser {
		uid, err := uuid.Parse(a.ID)
		if err != nil {
			return nil, ports.ErrForbidden
		}
		user, err := q.GetUserByID(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrForbidden
		}
		if err != nil {
			return nil, err
		}
		if !user.IsActive || user.Role != a.Role {
			return nil, ports.ErrForbidden
		}
	} else {
		return nil, ports.ErrForbidden
	}
	requirements, e := q.ListRequirementsForOperation(ctx, id)
	if e != nil {
		return nil, e
	}
	if len(requirements) == 0 {
		return nil, ports.ErrNotFound
	}
	owned := a.IsAdmin()
	hasPending := false
	for _, r := range requirements {
		if r.State == "pending" {
			hasPending = true
		}
		if r.ActorID == a.ID && r.ActorType == string(a.Type) {
			owned = true
		}
	}
	if !owned {
		return nil, ports.ErrForbidden
	}
	task, te := q.GetDownloadTaskByOpID(ctx, id)
	plan, pe := q.GetTransferPlanByOpID(ctx, id)
	if te != nil && !errors.Is(te, pgx.ErrNoRows) {
		return nil, te
	}
	if pe != nil && !errors.Is(pe, pgx.ErrNoRows) {
		return nil, pe
	}
	out := &ports.OperationSnapshot{OperationID: id, Status: "unknown"}
	if te == nil {
		out.Status = task.State
		if task.State == "completed" {
			switch task.OrganizationState {
			case "completed":
				out.Status = "completed"
			case "review_required":
				out.Status = "review_required"
			case "queued":
				out.Status = "organizing"
			default:
				out.Status = "waiting_organization"
			}
		}
		out.DownloadStatus = &task.State
		out.Progress = &task.Progress
	}
	if pe == nil {
		out.PlanStatus = &plan.State
		out.PlanID = &plan.ID
		if te != nil {
			out.Status = plan.State
		}
	}
	if errors.Is(te, pgx.ErrNoRows) && errors.Is(pe, pgx.ErrNoRows) {
		if !hasPending {
			out.Status = "cancelled"
		} else {
			waiting, err := q.HasQueuedOperationSubmission(ctx, id.String())
			if err != nil {
				return nil, err
			}
			if waiting {
				out.Status = "queued"
			}
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
