package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetDeliveryManagementService(service *notify.DeliveryManagementService) {
	s.deliveryManagementSvc = service
}

type deliveryManagementFailure struct{ controlFailure }

func deliveryFailure(ctx context.Context, e error) deliveryManagementFailure {
	f := failedControl(ctx, e)
	if errors.Is(e, ports.ErrInvalidDeliveryCommand) {
		f.status = 400
		f.body.Code = "INVALID_DELIVERY_COMMAND"
	}
	if errors.Is(e, ports.ErrDeliveryCommandConflict) {
		f.status = 409
		f.body.Code = "DELIVERY_COMMAND_CONFLICT"
	}
	f.body.Message = "integration delivery request could not be completed"
	return deliveryManagementFailure{f}
}
func (e deliveryManagementFailure) VisitListIntegrationDeliveriesResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}
func (e deliveryManagementFailure) VisitReplayIntegrationDeliveryResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}

type deliveryListSuccess struct {
	Body ListIntegrationDeliveries200JSONResponse
}

func (s deliveryListSuccess) VisitListIntegrationDeliveriesResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.Body)
}

type deliveryReplaySuccess struct {
	Body ReplayIntegrationDelivery200JSONResponse
}

func (s deliveryReplaySuccess) VisitReplayIntegrationDeliveryResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.Body)
}
func (s *ServerHandler) ListIntegrationDeliveries(ctx context.Context, r ListIntegrationDeliveriesRequestObject) (ListIntegrationDeliveriesResponseObject, error) {
	if s.deliveryManagementSvc == nil {
		return deliveryFailure(ctx, ports.ErrUnavailable), nil
	}
	query := ports.DeliveryListQuery{Limit: 100}
	if r.Params.Limit != nil {
		query.Limit = *r.Params.Limit
	}
	if r.Params.State != nil {
		query.State = string(*r.Params.State)
	}
	if r.Params.Cursor != nil {
		query.Cursor = *r.Params.Cursor
	}
	if r.Params.EventId != nil {
		query.EventID = *r.Params.EventId
	}
	page, e := s.deliveryManagementSvc.List(ctx, GetActor(ctx), query)
	if e != nil {
		return deliveryFailure(ctx, e), nil
	}
	items := []DeliverySummary{}
	for _, d := range page.Items {
		items = append(items, DeliverySummary{Id: d.ID, EventId: d.EventID, Consumer: d.Consumer, AggregateType: d.AggregateType, AggregateId: d.AggregateID, Sequence: d.Sequence, State: DeliverySummaryState(d.State), Attempts: d.Attempts, LeaseToken: d.LeaseToken, UpdatedAt: d.UpdatedAt, NotBefore: d.NotBefore, RecipientName: d.RecipientName, DuplicateRisk: d.DuplicateRisk})
	}
	return deliveryListSuccess{ListIntegrationDeliveries200JSONResponse{Items: items, Limit: page.Limit, NextCursor: page.NextCursor}}, nil
}
func (s *ServerHandler) ReplayIntegrationDelivery(ctx context.Context, r ReplayIntegrationDeliveryRequestObject) (ReplayIntegrationDeliveryResponseObject, error) {
	if s.deliveryManagementSvc == nil {
		return deliveryFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return deliveryFailure(ctx, ports.ErrInvalidDeliveryCommand), nil
	}
	d, e := s.deliveryManagementSvc.Replay(ctx, GetActor(ctx), r.Id, ports.DeliveryReplayCommand{ExpectedToken: r.Body.ExpectedToken, ExpectedAttempts: r.Body.ExpectedAttempts, ExpectedUpdatedAt: r.Body.ExpectedUpdatedAt, AcceptDuplicateRisk: r.Body.AcceptDuplicateRisk}, r.Params.IdempotencyKey)
	if e != nil {
		return deliveryFailure(ctx, e), nil
	}
	return deliveryReplaySuccess{ReplayIntegrationDelivery200JSONResponse{Id: d.ID, EventId: d.EventID, State: d.State, LeaseToken: d.LeaseToken}}, nil
}
