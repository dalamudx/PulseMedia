package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func (s *UntrackedReviewStore) ReadUntrackedReviewScopes(ctx context.Context, actor *ports.Actor, cursor string) (out ports.UntrackedReviewScopePage, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return out, err
	}
	after := uuid.Nil
	if cursor != "" {
		after, err = uuid.Parse(cursor)
		if err != nil || after == uuid.Nil || after.String() != cursor {
			return out, ports.ErrRejected
		}
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return out, err
	}
	defer managementRollback(ctx, tx)
	owner := uuid.MustParse(a.ID)
	ids, err := db.New(tx).ListUntrackedReviewScopes(ctx, db.ListUntrackedReviewScopesParams{OwnerID: owner, AfterID: after})
	if err != nil {
		return out, err
	}
	out.Items = []ports.UntrackedReviewScopeOption{}
	if len(ids) > 100 {
		out.HasMore = true
		ids = ids[:100]
	}
	for _, id := range ids {
		c, e := untrackedCurrent(ctx, tx, owner, id, false)
		if e != nil {
			return out, e
		}
		if c == nil {
			return out, ports.ErrUntrackedReviewStale
		}
		out.Items = append(out.Items, ports.UntrackedReviewScopeOption{ID: c.ID.String(), Version: c.Version, Enabled: c.Enabled, LocalRoot: c.LocalRoot, TargetRootPath: c.TargetRootPath, TargetMediaType: c.TargetMediaType})
	}
	if out.HasMore {
		out.NextCursor = ids[len(ids)-1].String()
	}
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
