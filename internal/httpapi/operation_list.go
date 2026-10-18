package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) ListOperations(ctx context.Context, request ListOperationsRequestObject) (ListOperationsResponseObject, error) {
	actor := GetActor(ctx)
	id := GetRequestID(ctx)
	if actor == nil {
		return ListOperations401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: id}, nil
	}
	query := ports.OperationListRequest{}
	if request.Params.View != nil {
		query.View = string(*request.Params.View)
	}
	if request.Params.Limit != nil {
		query.Limit = *request.Params.Limit
		if query.Limit < 1 || query.Limit > 50 {
			return ListOperations400JSONResponse{Code: "INVALID_PAGE", Message: "invalid operation page limit", RequestId: id}, nil
		}
	}
	if request.Params.Cursor != nil {
		query.Cursor = *request.Params.Cursor
	}
	page, e := s.operationSvc.List(ctx, actor, query)
	if errors.Is(e, ports.ErrForbidden) {
		return ListOperations403JSONResponse{Code: "FORBIDDEN", Message: "operation access denied", RequestId: id}, nil
	}
	if errors.Is(e, ports.ErrInvalidOperationQuery) {
		return ListOperations400JSONResponse{Code: "INVALID_PAGE", Message: "invalid operation page or cursor", RequestId: id}, nil
	}
	if e != nil {
		return ListOperations503JSONResponse{Code: "UNAVAILABLE", Message: "operation list temporarily unavailable", RequestId: id}, nil
	}
	out := OperationPage{Items: make([]OperationSummary, 0, len(page.Items))}
	for _, entry := range page.Items {
		out.Items = append(out.Items, OperationSummary{OperationId: entry.OperationID, Status: entry.Status, DownloadStatus: entry.DownloadStatus, Progress: entry.Progress, PlanStatus: entry.PlanStatus, PlanId: entry.PlanID, Title: entry.Title, CreatedAt: entry.CreatedAt})
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	return ListOperations200JSONResponse(out), nil
}
