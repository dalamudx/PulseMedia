package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *MediaRequestService) inboxReader(a *ports.Actor) (ports.MediaRequestInboxReader, error) {
	actor, err := requestActor(a)
	if err != nil {
		return nil, err
	}
	if actor.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	reader, ok := s.repo.(ports.MediaRequestInboxReader)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return reader, nil
}

func (s *MediaRequestService) Summary(ctx context.Context, a *ports.Actor) (out *ports.MediaRequestInboxSummary, err error) {
	defer func() {
		if err != nil {
			err = requestError(ctx, err)
		}
	}()
	reader, err := s.inboxReader(a)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err = reader.ReadMediaRequestInbox(ctx, a)
	if err == nil && (out == nil || out.PendingCount < 0 || out.ObservedAt.IsZero()) {
		return nil, ports.ErrUnavailable
	}
	return out, err
}

func (s *MediaRequestService) Pending(ctx context.Context, a *ports.Actor, limit int, before *uuid.UUID) (out []ports.MediaRequest, err error) {
	defer func() {
		if err != nil {
			err = requestError(ctx, err)
		}
	}()
	reader, err := s.inboxReader(a)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || before != nil && *before == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return reader.ListPendingMediaRequests(ctx, a, limit, before)
}
