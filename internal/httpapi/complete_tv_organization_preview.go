package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"net/http"
)

func (s *ServerHandler) SetCompleteTVOrganizationPreviewService(svc *transfer.CompleteTVOrganizationPreviewService) {
	s.completeTVOrganizationPreviewSvc = svc
}

type completeTVOrganizationPreviewResponse struct{ transferRecoveryResponse }

func (r completeTVOrganizationPreviewResponse) VisitListCompleteTVOrganizationsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) ListCompleteTVOrganizations(ctx context.Context, r ListCompleteTVOrganizationsRequestObject) (ListCompleteTVOrganizationsResponseObject, error) {
	a := GetActor(ctx)
	var err error
	if a == nil {
		err = ports.ErrUnauthorized
	} else if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		err = ports.ErrForbidden
	}
	if err != nil {
		return completeTVOrganizationPreviewResponse{recoveryFailure(ctx, err)}, nil
	}
	if s.completeTVOrganizationPreviewSvc == nil {
		return completeTVOrganizationPreviewResponse{recoveryFailure(ctx, ports.ErrUnavailable)}, nil
	}
	out, err := s.completeTVOrganizationPreviewSvc.Read(ctx, a, r.Id)
	if err != nil {
		return completeTVOrganizationPreviewResponse{recoveryFailure(ctx, err)}, nil
	}
	return completeTVOrganizationPreviewResponse{transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: out}}}, nil
}
