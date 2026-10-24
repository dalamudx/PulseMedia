package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ execution *Service }

var _ ports.TransferRepository = (*Repository)(nil)

func NewRepository(pool *pgxpool.Pool, ids ports.IDGen, events *outbox.Outbox, files ports.TransferFilesystem) *Repository {
	return &Repository{NewService(pool, ids, events, files)}
}

// NewRepositoryWithMinimumMediaBytes applies deployment filtering to new physical attempts.
func NewRepositoryWithMinimumMediaBytes(pool *pgxpool.Pool, ids ports.IDGen, events *outbox.Outbox, files ports.TransferFilesystem, minimum int64) *Repository {
	r := NewRepository(pool, ids, events, files)
	r.execution.minimumMediaBytes = minimum
	return r
}
func (r *Repository) PrepareMovieTransfer(ctx context.Context, c ports.MovieTransferRequest) (ports.PreparedMovieTransfer, error) {
	old := pgtype.UUID{}
	if c.OldFileID != nil {
		old = pgtype.UUID{Bytes: *c.OldFileID, Valid: true}
	}
	p, s, e := r.execution.createMoviePlan(ctx, c.OperationID, c.RootID, c.UnitID, c.SourcePath, c.Title, c.Year, c.Quality, old)
	if e != nil {
		return ports.PreparedMovieTransfer{}, e
	}
	return ports.PreparedMovieTransfer{Plan: ports.TransferPlan{ID: p.ID, OperationID: p.OperationID, RootID: p.RootID, State: p.State, Strategy: p.Strategy}, Step: ports.TransferStep{ID: s.ID, PlanID: s.PlanID, UnitID: s.UnitID, SourcePath: s.SourcePath, TargetPath: s.TargetPath, Action: s.Action, StepOrder: s.StepOrder}}, nil
}
func (r *Repository) ExecuteTransfer(ctx context.Context, id uuid.UUID) error {
	return r.execution.ExecutePlan(ctx, id)
}
func (r *Repository) OrganizeCompletedDownload(ctx context.Context, lease ports.JobLease) error {
	return r.execution.OrganizeDownload(ctx, lease)
}
