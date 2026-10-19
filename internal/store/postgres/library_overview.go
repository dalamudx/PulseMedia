package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LibraryOverviewStore struct{ pool *pgxpool.Pool }

var _ ports.LibraryOverviewReader = (*LibraryOverviewStore)(nil)

func NewLibraryOverviewStore(pool *pgxpool.Pool) *LibraryOverviewStore {
	return &LibraryOverviewStore{pool: pool}
}

func (s *LibraryOverviewStore) ReadLibraryOverview(ctx context.Context, actor *ports.Actor, in ports.LibraryOverviewRequest) (*ports.LibraryOverview, error) {
	if actor == nil || actor.Type != ports.ActorTypeUser ||
		(actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) ||
		(actor.Scopes != nil && !slices.Contains(actor.Scopes, "read")) {
		return nil, ports.ErrForbidden
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil, ports.ErrForbidden
	}
	if in.RecentLimit == 0 {
		in.RecentLimit = 12
	}
	if in.RecentLimit < 1 || in.RecentLimit > 24 {
		return nil, ports.ErrInvalidLibraryQuery
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	user, err := q.GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if !user.IsActive || user.Role != actor.Role {
		return nil, ports.ErrForbidden
	}
	observed, err := q.LibraryOverviewSnapshotTime(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	counts, err := q.CountLibraryOverview(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	roots, err := q.ListLibraryOverviewRoots(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	recent, err := q.ListLibraryOverviewRecent(ctx, int32(in.RecentLimit))
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.LibraryOverview{
		Counts:     ports.LibraryOverviewCounts{Movies: counts.Movies, TVShows: counts.TvShows, Episodes: counts.Episodes, Files: counts.Files},
		Roots:      make([]ports.LibraryRootOverview, 0, len(roots)),
		Recent:     make([]ports.LibraryRecentImport, 0, len(recent)),
		ObservedAt: observed.Time,
	}
	for _, row := range roots {
		root := ports.LibraryRootOverview{RootID: row.RootID, Name: row.Name, MediaType: row.MediaType, Health: row.Health, Availability: row.Availability, CapacityState: row.CapacityState}
		if row.CapacityState != "unknown" {
			total, totalOK := row.TotalBytes.(string)
			available, availableOK := row.AvailableBytes.(string)
			if !totalOK || !availableOK {
				return nil, ports.ErrUnavailable
			}
			root.TotalBytes, root.AvailableBytes = &total, &available
		}
		if row.SampledAt.Valid {
			v := row.SampledAt.Time
			root.SampledAt = &v
		}
		out.Roots = append(out.Roots, root)
	}
	for _, row := range recent {
		item := ports.LibraryRecentImport{MediaID: row.MediaID, MediaType: row.MediaType, Title: row.Title, ImportedAt: row.ImportedAt.Time}
		if row.Year.Valid {
			v := row.Year.Int32
			item.Year = &v
		}
		if poster := media.TMDBPosterURL(row.PosterPath); poster != "" {
			item.PosterURL = &poster
		}
		out.Recent = append(out.Recent, item)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
