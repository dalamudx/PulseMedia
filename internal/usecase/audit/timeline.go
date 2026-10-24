package audit

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type TimelineService struct{ reader ports.AuditTimelineReader }

func NewTimelineService(reader ports.AuditTimelineReader) *TimelineService {
	return &TimelineService{reader: reader}
}

func (s *TimelineService) List(ctx context.Context, actor *ports.Actor, request ports.AuditTimelineRequest) (*ports.AuditTimelinePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if actor.Scopes != nil && (len(actor.Scopes) != 1 || actor.Scopes[0] != "read") {
		return nil, ports.ErrForbidden
	}
	account, err := uuid.Parse(actor.ID)
	if err != nil || account == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	filter, err := auditlog.Normalize(request.Filter)
	if err != nil {
		return nil, ports.ErrRejected
	}
	limit := request.Limit
	if limit == 0 {
		limit = auditlog.DefaultLimit
	}
	if limit < 1 || limit > auditlog.MaximumLimit {
		return nil, ports.ErrRejected
	}
	before, err := auditlog.DecodeCursor(request.Cursor, account, filter)
	if err != nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	current := *actor
	if actor.Scopes != nil {
		current.Scopes = append([]string{}, actor.Scopes...)
	}
	rows, err := s.reader.ReadAuditTimeline(ctx, &current, filter, before, limit+1)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		for _, known := range []error{ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrRejected, context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, known) {
				return nil, known
			}
		}
		return nil, ports.ErrUnavailable
	}
	if len(rows) > limit+1 {
		return nil, ports.ErrUnavailable
	}
	previous := before
	for _, row := range rows {
		position := auditlog.Position{ID: row.ID, CreatedAt: row.CreatedAt}
		if !position.Valid() || previous != nil && !position.Before(*previous) {
			return nil, ports.ErrUnavailable
		}
		previous = &position
	}
	page := &ports.AuditTimelinePage{Items: make([]ports.AuditSummary, 0, min(limit, len(rows)))}
	if len(rows) > limit {
		last := rows[limit-1]
		page.NextCursor, err = auditlog.EncodeCursor(account, filter, auditlog.Position{ID: last.ID, CreatedAt: last.CreatedAt})
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		rows = rows[:limit]
	}
	for _, row := range rows {
		row.CreatedAt = row.CreatedAt.UTC()
		page.Items = append(page.Items, row)
	}
	return page, nil
}
