package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

// A snapshot is an observation, not a cacheable permanent ownership result.
type uncachedStatusResponse struct {
	GetSubscriptionStatusResponseObject
}

func (r uncachedStatusResponse) VisitGetSubscriptionStatusResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return r.GetSubscriptionStatusResponseObject.VisitGetSubscriptionStatusResponse(w)
}

func (s *ServerHandler) SetSubscriptionStatusService(service *subscription.StatusService) {
	s.statusSvc = service
}
func (s *ServerHandler) GetSubscriptionStatus(ctx context.Context, r GetSubscriptionStatusRequestObject) (GetSubscriptionStatusResponseObject, error) {
	id := GetRequestID(ctx)
	a := GetActor(ctx)
	if a == nil {
		return GetSubscriptionStatus401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: id}, nil
	}
	if s.statusSvc == nil {
		return GetSubscriptionStatus503JSONResponse{Code: "UNAVAILABLE", Message: "status unavailable", RequestId: id}, nil
	}
	report, err := s.statusSvc.Read(ctx, a, r.Id)
	switch {
	case errors.Is(err, ports.ErrForbidden):
		return GetSubscriptionStatus403JSONResponse{Code: "FORBIDDEN", Message: "subscription access denied", RequestId: id}, nil
	case errors.Is(err, ports.ErrNotFound):
		return GetSubscriptionStatus404JSONResponse{Code: "NOT_FOUND", Message: "subscription not found", RequestId: id}, nil
	case err != nil:
		return GetSubscriptionStatus503JSONResponse{Code: "UNAVAILABLE", Message: "status observation unavailable", RequestId: id}, nil
	}
	result := SubscriptionStatusReport{Id: report.ID, IntentVersion: report.IntentVersion, PolicyRevision: report.PolicyRevision, RootGeneration: report.RootGeneration, ObservedAt: report.ObservedAt, Blockers: report.Blockers, EndpointRetryAt: report.EndpointRetryAt, Items: make([]WantedStatusDetails, len(report.Items))}
	for i, value := range report.Items {
		row := WantedStatusDetails{Id: value.ID, UnitId: value.UnitID, UnitKey: value.UnitKey, FactsVersion: value.FactsVersion, IsSkipped: value.IsSkipped, IsSatisfied: value.IsSatisfied, ReservationProtected: value.ReservationProtected, Status: WantedStatusDetailsStatus(value.Status)}
		if value.WaitReason != "" {
			reason := value.WaitReason
			row.WaitReason = &reason
		}
		if value.CurrentFactStage != "" {
			stage := WantedStatusDetailsCurrentFactStage(value.CurrentFactStage)
			row.CurrentFactStage = &stage
		}
		result.Items[i] = row
	}
	return uncachedStatusResponse{GetSubscriptionStatus200JSONResponse(result)}, nil
}
