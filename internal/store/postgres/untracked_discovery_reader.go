package postgres

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Unlike watchGuard this has no row-locking clause, so readers use READ ONLY.
func discoveryReadGuard(ctx context.Context, tx pgx.Tx) error {
	var isolated bool
	if err := tx.QueryRow(ctx, `SELECT isolated FROM restore_isolation WHERE singleton`).Scan(&isolated); err != nil {
		return err
	}
	if isolated {
		return ErrRestoreIsolated
	}
	return ctx.Err()
}

func (s *UntrackedDiscoveryStore) ListUntrackedDiscoveryScopes(ctx context.Context, cursor string, limit int) (out ports.UntrackedDiscoverySourcePage, err error) {
	defer func() { err = discoveryError(ctx, err) }()
	if !s.ready() {
		return out, ports.ErrUnavailable
	}
	n, err := discoveryLimit(limit)
	if err != nil {
		return out, err
	}
	c := untrackedDiscoveryCursor{Kind: "scopes"}
	if cursor != "" {
		c, err = s.decodeCursor(cursor, "scopes", uuid.Nil, uuid.Nil, 0)
		if err != nil {
			return out, err
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer managementRollback(ctx, tx)
	if err = discoveryReadGuard(ctx, tx); err != nil {
		return out, err
	}
	q := db.New(tx)
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return out, err
	}
	if c.Horizon.IsZero() {
		c.Horizon = now.Time
	}
	if c.Horizon.After(now.Time) {
		return out, ports.ErrRejected
	}
	ids, err := q.ListUntrackedDiscoveryCandidates(ctx, db.ListUntrackedDiscoveryCandidatesParams{AfterID: c.AfterID, Horizon: discoveryTime(c.Horizon), PageSize: int32(n)})
	if err != nil {
		return out, err
	}
	out.ManifestAt = c.Horizon
	out.Scopes = make([]ports.UntrackedScope, 0, len(ids))
	// At most 64 candidates per call, including invalid configurations. Progress
	// uses the last examined ID so invalid rows cannot stall pagination.
	for _, id := range ids {
		owner, e := q.UntrackedDiscoveryScopeOwner(ctx, id)
		if e != nil {
			return ports.UntrackedDiscoverySourcePage{}, e
		}
		scope, e := s.authority(ctx, tx, id, owner, false)
		if e != nil {
			if ctx.Err() != nil {
				return ports.UntrackedDiscoverySourcePage{}, ctx.Err()
			}
			if discoverySkippable(discoveryError(ctx, e)) {
				continue
			}
			return ports.UntrackedDiscoverySourcePage{}, e
		}
		if !scope.UpdatedAt.After(c.Horizon) {
			out.Scopes = append(out.Scopes, *scope)
		}
	}
	out.Complete = len(ids) < n
	if !out.Complete {
		c.AfterID = ids[len(ids)-1]
		out.NextCursor = s.encodeCursor(c)
	}
	if err = discoveryReadGuard(ctx, tx); err != nil {
		return ports.UntrackedDiscoverySourcePage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.UntrackedDiscoverySourcePage{}, err
	}
	return out, nil
}

func (s *UntrackedDiscoveryStore) ReadUntrackedDiscovery(ctx context.Context, actor *ports.Actor, scopeID uuid.UUID, cursor string, limit int) (out ports.UntrackedDiscoveryPage, err error) {
	defer func() { err = discoveryError(ctx, err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return out, err
	}
	if !s.ready() {
		return out, ports.ErrUnavailable
	}
	if scopeID == uuid.Nil {
		return out, ports.ErrRejected
	}
	n, err := discoveryLimit(limit)
	if err != nil {
		return out, err
	}
	owner := uuid.MustParse(a.ID)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer managementRollback(ctx, tx)
	scope, err := s.authority(ctx, tx, scopeID, owner, false)
	if err != nil {
		return out, err
	}
	q := db.New(tx)
	c := untrackedDiscoveryCursor{Kind: "files", Owner: owner, Scope: scopeID, Version: scope.Version}
	if cursor != "" {
		c, err = s.decodeCursor(cursor, "files", owner, scopeID, scope.Version)
		if err != nil {
			return out, err
		}
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return out, err
	}
	if c.Horizon.IsZero() {
		c.Horizon = now.Time
	}
	if c.Horizon.After(now.Time) {
		return out, ports.ErrRejected
	}
	rows, err := q.ReadUntrackedDiscoveryFiles(ctx, db.ReadUntrackedDiscoveryFilesParams{ScopeID: scopeID, Horizon: discoveryTime(c.Horizon), AfterCreated: discoveryTime(c.AfterCreated), AfterID: c.AfterID, PageSize: int32(n + 1)})
	if err != nil {
		return out, err
	}
	out.Complete = len(rows) <= n
	if !out.Complete {
		rows = rows[:n]
	}
	out.Files = make([]ports.UntrackedDiscoveredFile, 0, len(rows))
	for _, raw := range rows {
		r, file, e := discoveryFile(raw, scopeID, scope.Version)
		if e != nil {
			return ports.UntrackedDiscoveryPage{}, e
		}
		out.Files = append(out.Files, file)
		c.AfterID, c.AfterCreated = r.ID, r.CreatedAt
	}
	if !out.Complete {
		out.NextCursor = s.encodeCursor(c)
	}
	if err = s.recheck(ctx, tx, *scope); err != nil {
		return ports.UntrackedDiscoveryPage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.UntrackedDiscoveryPage{}, err
	}
	return out, nil
}
