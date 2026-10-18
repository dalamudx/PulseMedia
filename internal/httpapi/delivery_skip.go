package httpapi

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetDeliverySkipService(service *notify.DeliverySkipService) {
	s.deliverySkipSvc = service
}

type deliverySkipFailure struct{ deliveryManagementFailure }

func (f deliverySkipFailure) VisitSkipIntegrationDeliveryResponse(w http.ResponseWriter) error {
	return f.VisitControlSubscriptionResponse(w)
}

type deliverySkipSuccess struct{ receipt *ports.DeliverySkipReceipt }

func (r deliverySkipSuccess) VisitSkipIntegrationDeliveryResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(r.receipt)
}
func (s *ServerHandler) SkipIntegrationDelivery(ctx context.Context, r SkipIntegrationDeliveryRequestObject) (SkipIntegrationDeliveryResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return deliverySkipFailure{deliveryFailure(ctx, ports.ErrUnauthorized)}, nil
	}
	if !a.IsAdmin() {
		return deliverySkipFailure{deliveryFailure(ctx, ports.ErrForbidden)}, nil
	}
	if s.deliverySkipSvc == nil {
		return deliverySkipFailure{deliveryFailure(ctx, ports.ErrUnavailable)}, nil
	}
	if r.Body == nil {
		return deliverySkipFailure{deliveryFailure(ctx, ports.ErrInvalidDeliveryCommand)}, nil
	}
	receipt, e := s.deliverySkipSvc.Skip(ctx, a, r.Id, ports.DeliverySkipCommand{ExpectedToken: r.Body.ExpectedToken, ExpectedAttempts: r.Body.ExpectedAttempts, ExpectedUpdatedAt: r.Body.ExpectedUpdatedAt, AcceptDeliveryLoss: r.Body.AcceptDeliveryLoss, Reason: string(r.Body.Reason)}, r.Params.IdempotencyKey)
	if e != nil {
		return deliverySkipFailure{deliveryFailure(ctx, e)}, nil
	}
	return deliverySkipSuccess{receipt}, nil
}
