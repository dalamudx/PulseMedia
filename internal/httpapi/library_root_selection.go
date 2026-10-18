package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func (s *ServerHandler) SetRootSelectionService(service *subscription.RootSelectionService) {
	s.rootSelectionSvc = service
}
func (s *ServerHandler) ListLibraryRootSelections(ctx context.Context, r ListLibraryRootSelectionsRequestObject) (ListLibraryRootSelectionsResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return ListLibraryRootSelections401JSONResponse{PolicyUnauthorizedJSONResponse: PolicyUnauthorizedJSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: GetRequestID(ctx)}}, nil
	}
	if s.rootSelectionSvc == nil {
		return rootSelectionUnavailable(ctx), nil
	}
	rows, e := s.rootSelectionSvc.List(ctx, a)
	if e != nil {
		return rootSelectionUnavailable(ctx), nil
	}
	items := []LibraryRootSelection{}
	for _, row := range rows {
		items = append(items, LibraryRootSelection{Id: row.ID, Name: row.Name, MediaType: LibraryRootSelectionMediaType(row.MediaType), Health: row.Health, Generation: row.Generation, BaselineCompleted: row.BaselineCompleted})
	}
	return ListLibraryRootSelections200JSONResponse{Items: items}, nil
}
func rootSelectionUnavailable(ctx context.Context) ListLibraryRootSelections503JSONResponse {
	return ListLibraryRootSelections503JSONResponse{PolicyUnavailableJSONResponse: PolicyUnavailableJSONResponse{Code: "UNAVAILABLE", Message: "root selections unavailable", RequestId: GetRequestID(ctx)}}
}
