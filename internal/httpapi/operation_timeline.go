package httpapi

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func operationTelemetryResponse(value *ports.OperationTelemetry) *OperationTelemetry {
	if value == nil {
		return nil
	}
	status := OperationTelemetryRateStatus(value.RateStatus)
	if status != "current" && status != "stale" {
		status = "unknown"
	}
	out := &OperationTelemetry{
		Progress: value.Progress, ProgressObservedAt: value.ProgressObservedAt,
		DownloadRateBytesPerSecond: value.DownloadRateBytesPerSecond,
		RateObservedAt:             value.RateObservedAt, RateStatus: status,
	}
	if value.ProgressSource == "native_percent" || value.ProgressSource == "verified_bytes" {
		source := OperationTelemetryProgressSource(value.ProgressSource)
		out.ProgressSource = &source
	}
	return out
}

func (s *ServerHandler) GetOperationTimeline(ctx context.Context, request GetOperationTimelineRequestObject) (GetOperationTimelineResponseObject, error) {
	requestID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetOperationTimeline401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: requestID}, nil
	}
	pageRequest := ports.OperationTimelineRequest{}
	if request.Params.Limit != nil {
		if *request.Params.Limit < 1 || *request.Params.Limit > 100 {
			return GetOperationTimeline400JSONResponse{Code: "INVALID_INPUT", Message: "invalid timeline page", RequestId: requestID}, nil
		}
		pageRequest.Limit = *request.Params.Limit
	}
	if request.Params.Cursor != nil {
		pageRequest.Cursor = *request.Params.Cursor
	}
	page, err := s.operationTimelineSvc.Read(ctx, actor, request.Id, pageRequest)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrInvalidOperationQuery):
			return GetOperationTimeline400JSONResponse{Code: "INVALID_INPUT", Message: "invalid timeline page or cursor", RequestId: requestID}, nil
		case errors.Is(err, ports.ErrForbidden):
			return GetOperationTimeline403JSONResponse{Code: "FORBIDDEN", Message: "access to private operation denied", RequestId: requestID}, nil
		case errors.Is(err, ports.ErrNotFound):
			return GetOperationTimeline404JSONResponse{Code: "NOT_FOUND", Message: "operation not found", RequestId: requestID}, nil
		default:
			return GetOperationTimeline503JSONResponse{Code: "UNAVAILABLE", Message: "operation timeline temporarily unavailable", RequestId: requestID}, nil
		}
	}
	out := OperationTimelinePage{
		OperationId: page.OperationID, PlanId: page.PlanID, PlanStatus: page.PlanStatus,
		Items: make([]OperationTimelineEntry, 0, len(page.Items)), ObservedAt: page.ObservedAt,
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	for _, step := range page.Items {
		out.Items = append(out.Items, OperationTimelineEntry{
			StepId: step.StepID, Order: step.Order, Action: step.Action, State: step.State,
			FileName: step.FileName, UpdatedAt: step.UpdatedAt, FinishedAt: step.FinishedAt,
		})
	}
	return GetOperationTimeline200JSONResponse(out), nil
}
