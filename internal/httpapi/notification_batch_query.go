package httpapi

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetBatchQueryService(service *notify.BatchQueryService) {
	s.batchQuerySvc = service
}

type batchQueryFailure struct{ controlFailure }

func (f batchQueryFailure) VisitListNotificationBatchesResponse(w http.ResponseWriter) error {
	return f.VisitControlSubscriptionResponse(w)
}

type batchQuerySuccess struct {
	Items []ports.NotificationBatchSummary `json:"items"`
	Limit int                              `json:"limit"`
}

func (r batchQuerySuccess) VisitListNotificationBatchesResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(r)
}
func (s *ServerHandler) ListNotificationBatches(ctx context.Context, r ListNotificationBatchesRequestObject) (ListNotificationBatchesResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return batchQueryFailure{failedControl(ctx, ports.ErrUnauthorized)}, nil
	}
	if !a.IsAdmin() {
		return batchQueryFailure{failedControl(ctx, ports.ErrForbidden)}, nil
	}
	if s.batchQuerySvc == nil {
		return batchQueryFailure{failedControl(ctx, ports.ErrUnavailable)}, nil
	}
	items, e := s.batchQuerySvc.List(ctx, a)
	if e != nil {
		f := failedControl(ctx, e)
		f.body.Message = "notification batch observations unavailable"
		return batchQueryFailure{f}, nil
	}
	return batchQuerySuccess{items, 100}, nil
}
