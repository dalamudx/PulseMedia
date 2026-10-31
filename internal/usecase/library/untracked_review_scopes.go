package library

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *UntrackedReviewService) ScopeOptions(ctx context.Context, actor *ports.Actor, cursor string) (ports.UntrackedReviewScopePage, error) {
	var empty ports.UntrackedReviewScopePage
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if actor == nil {
		return empty, ports.ErrUnauthorized
	}
	a := *actor
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || id.String() != a.ID || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return empty, ports.ErrForbidden
	}
	if cursor != "" {
		after, e := uuid.Parse(cursor)
		if e != nil || after == uuid.Nil || after.String() != cursor {
			return empty, ports.ErrRejected
		}
	}
	if s == nil || s.repo == nil {
		return empty, ports.ErrUnavailable
	}
	repo, ok := s.repo.(ports.UntrackedReviewScopeRepository)
	if !ok {
		return empty, ports.ErrUnavailable
	}
	page, err := repo.ReadUntrackedReviewScopes(ctx, &a, cursor)
	if err != nil || ctx.Err() != nil {
		return empty, untrackedReviewError(ctx, err)
	}
	if page.Items == nil || len(page.Items) > 100 || page.HasMore != (page.NextCursor != "") || (page.HasMore && len(page.Items) != 100) {
		return empty, ports.ErrUnavailable
	}
	previous := cursor
	for _, o := range page.Items {
		id, e := uuid.Parse(o.ID)
		if e != nil || id == uuid.Nil || id.String() != o.ID || o.ID <= previous || o.Version < 1 || o.LocalRoot == "" || o.TargetRootPath == "" || (o.TargetMediaType != "movie" && o.TargetMediaType != "tv") {
			return empty, ports.ErrUnavailable
		}
		previous = o.ID
	}
	if page.HasMore && page.NextCursor != previous {
		return empty, ports.ErrUnavailable
	}
	page.Items = append([]ports.UntrackedReviewScopeOption{}, page.Items...)
	return page, nil
}
