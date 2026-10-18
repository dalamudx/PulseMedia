package httpapi

import (
	"context"

	"github.com/google/uuid"
)

// Remaining legacy download and wishlist entry points share this projection.
// Subscription creation resolves and fences its choice inside its repository.
func (s *ServerHandler) soleConfiguredRoot(ctx context.Context, mediaType string) uuid.UUID {
	if s.rootSelectionSvc == nil {
		return uuid.Nil
	}
	roots, err := s.rootSelectionSvc.List(ctx, GetActor(ctx))
	if err != nil {
		return uuid.Nil
	}
	id := uuid.Nil
	for _, root := range roots {
		if root.MediaType != mediaType || root.Health != "online" {
			continue
		}
		if id != uuid.Nil {
			return uuid.Nil
		}
		id = root.ID
	}
	return id
}
