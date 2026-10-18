package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// TVSelectionService is an internal read boundary, never an execution entry point.
type TVSelectionService struct{ repo ports.TVSelectionRepository }

func NewTVSelectionService(repo ports.TVSelectionRepository) *TVSelectionService {
	return &TVSelectionService{repo: repo}
}
func (s *TVSelectionService) Preview(ctx context.Context, actor *ports.Actor, request ports.TVSelectionRequest) (*TVSelectionPreview, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || actor.Type != ports.ActorTypeUser || !actor.IsAdmin() || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if err = ports.ValidateTVSelectionRequest(request); err != nil {
		return nil, err
	}
	stamp, err := TVSelectionPolicyStamp(request.PackPolicy)
	if err != nil {
		return nil, err
	}
	in, err := s.repo.ReadTVSelection(ctx, actor, request)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, ports.ErrUnavailable
	}
	if in.Current.Version.SelectionPolicyStamp != stamp {
		return nil, ErrTVSelectionChanged
	}
	in.Expected = in.Current.Version
	if request.Expected != nil {
		in.Expected = *request.Expected
	}
	return PreviewTVSelection(ctx, actor, *in)
}

