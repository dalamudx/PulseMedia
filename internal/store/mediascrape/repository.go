package mediascrape

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

var _ ports.MediaScrapeBatchRepository = (*Repository)(nil)

func authorize(ctx context.Context, q *db.Queries, actor *ports.Actor) (uuid.UUID, error) {
	if actor == nil {
		return uuid.Nil, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || actor.Role != ports.RoleAdmin {
		return uuid.Nil, ports.ErrForbidden
	}
	user, err := q.LockMediaScrapeUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ports.ErrForbidden
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !user.IsActive || user.Role != "admin" {
		return uuid.Nil, ports.ErrForbidden
	}
	isolated, err := q.GuardMediaScrapeIsolation(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if isolated {
		return uuid.Nil, ports.ErrUnavailable
	}
	return id, nil
}

func (r *Repository) ListMediaScrapeCandidates(ctx context.Context, actor *ports.Actor) ([]ports.MediaScrapeCandidate, error) {
	var out []ports.MediaScrapeCandidate
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := authorize(ctx, q, actor); err != nil {
			return err
		}
		rows, err := q.ListMediaScrapeCandidates(ctx)
		if err != nil {
			return err
		}
		out = make([]ports.MediaScrapeCandidate, 0, len(rows))
		for _, row := range rows {
			out = append(out, ports.MediaScrapeCandidate{SourceEventID: row.SourceEventID, FileID: row.FileID, UnitID: row.UnitID, MediaID: row.MediaID, MediaType: row.MediaType, Title: row.Title, RelPath: row.RelPath, Status: ports.MediaScrapeStatus(row.Status)})
		}
		return nil
	})
	return out, err
}

func (r *Repository) StartMediaScrape(ctx context.Context, actor *ports.Actor, key string, allocate func() uuid.UUID) (ports.MediaScrapeProgress, bool, error) {
	var progress ports.MediaScrapeProgress
	var reused bool
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		// Serialize publication, then recheck the account after any lock wait.
		if err := q.LockMediaScrapeSubmission(ctx); err != nil {
			return err
		}
		user, err := authorize(ctx, q, actor)
		if err != nil {
			return err
		}
		id, err := q.FindMediaScrapeBatch(ctx, db.FindMediaScrapeBatchParams{RequestedBy: user, CommandKey: key})
		if err == nil {
			row, e := q.ReadMediaScrapeBatch(ctx, id)
			if e != nil {
				return e
			}
			progress = progressOf(row)
			reused = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = q.LockMediaScrapeRoots(ctx); err != nil {
			return err
		}
		rows, err := q.ListMediaScrapeCandidates(ctx)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for _, row := range rows {
			if row.Status == "missing" || row.Status == "complete" {
				ids = append(ids, allocate())
			}
		}
		if len(ids) == 0 {
			return ports.ErrRejected
		}
		batch, err := q.CreateMediaScrapeBatch(ctx, db.CreateMediaScrapeBatchParams{ID: allocate(), CommandKey: key, RequestedBy: user})
		if err != nil {
			return err
		}
		count, err := q.EnqueueMediaScrapeItems(ctx, db.EnqueueMediaScrapeItemsParams{JobIds: ids, BatchID: batch.ID})
		if err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return ports.ErrUnavailable
		}
		if err = q.AuditMediaScrapeBatch(ctx, db.AuditMediaScrapeBatchParams{ID: allocate(), ActorID: user.String(), TargetID: batch.ID.String(), Total: count}); err != nil {
			return err
		}
		row, err := q.ReadMediaScrapeBatch(ctx, batch.ID)
		if err != nil {
			return err
		}
		progress = progressOf(row)
		return nil
	})
	return progress, reused, err
}

func (r *Repository) ReadMediaScrapeBatch(ctx context.Context, actor *ports.Actor, key string) (*ports.MediaScrapeProgress, error) {
	var out *ports.MediaScrapeProgress
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		user, err := authorize(ctx, q, actor)
		if err != nil {
			return err
		}
		var id uuid.UUID
		if key != "" {
			id, err = q.FindMediaScrapeBatch(ctx, db.FindMediaScrapeBatchParams{RequestedBy: user, CommandKey: key})
		} else {
			id, err = q.LatestMediaScrapeBatch(ctx)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		row, err := q.ReadMediaScrapeBatch(ctx, id)
		if err != nil {
			return err
		}
		p := progressOf(row)
		out = &p
		return nil
	})
	return out, err
}
func progressOf(row db.ReadMediaScrapeBatchRow) ports.MediaScrapeProgress {
	return ports.MediaScrapeProgress{ID: row.ID, CommandKey: row.CommandKey, CreatedAt: row.CreatedAt.Time, Total: int(row.Total), Queued: int(row.Queued), Running: int(row.Running), Succeeded: int(row.Succeeded), Review: int(row.Review)}
}
