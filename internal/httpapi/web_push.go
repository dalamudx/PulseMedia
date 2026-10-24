package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetWebPushService(service *notify.WebPushService) { s.webPushSvc = service }
func webPushActorError(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) || err != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}

type webPushFailure struct{ controlFailure }

func (r webPushFailure) VisitGetWebPushConfigurationResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r webPushFailure) VisitListWebPushDevicesResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r webPushFailure) VisitRegisterWebPushDeviceResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r webPushFailure) VisitRevokeWebPushDeviceResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r webPushFailure) VisitGetWebPushReceiptResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func failedWebPush(ctx context.Context, err error) webPushFailure {
	f := failedControl(ctx, err)
	if errors.Is(err, notify.ErrInvalidConfig) {
		f.status = http.StatusBadRequest
		f.body.Code = "INVALID_REQUEST"
	}
	if errors.Is(err, ports.ErrWebPushConflict) {
		f.status = http.StatusConflict
		f.body.Code = "CONFLICT"
	}
	f.body.Message = "browser notification request could not be completed"
	return webPushFailure{f}
}
func (s *ServerHandler) webPushReady(ctx context.Context) error {
	if err := webPushActorError(GetActor(ctx)); err != nil {
		return err
	}
	if s.webPushSvc == nil {
		return ports.ErrUnavailable
	}
	return nil
}
func mapWebPushDevice(row *ports.WebPushDevice) WebPushDevice {
	return WebPushDevice{Id: row.ID, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func (s *ServerHandler) GetWebPushConfiguration(ctx context.Context, _ GetWebPushConfigurationRequestObject) (GetWebPushConfigurationResponseObject, error) {
	if err := s.webPushReady(ctx); err != nil {
		return failedWebPush(ctx, err), nil
	}
	r, err := s.webPushSvc.Configuration(ctx, GetActor(ctx))
	if err != nil {
		return failedWebPush(ctx, err), nil
	}
	return GetWebPushConfiguration200JSONResponse{Enabled: r.Enabled, Version: r.Version, PublicKey: r.PublicKey}, nil
}
func (s *ServerHandler) ListWebPushDevices(ctx context.Context, _ ListWebPushDevicesRequestObject) (ListWebPushDevicesResponseObject, error) {
	if err := s.webPushReady(ctx); err != nil {
		return failedWebPush(ctx, err), nil
	}
	rows, err := s.webPushSvc.List(ctx, GetActor(ctx))
	if err != nil {
		return failedWebPush(ctx, err), nil
	}
	result := make(ListWebPushDevices200JSONResponse, 0, len(rows))
	for i := range rows {
		result = append(result, mapWebPushDevice(&rows[i]))
	}
	return result, nil
}
func (s *ServerHandler) RegisterWebPushDevice(ctx context.Context, r RegisterWebPushDeviceRequestObject) (RegisterWebPushDeviceResponseObject, error) {
	if err := s.webPushReady(ctx); err != nil {
		return failedWebPush(ctx, err), nil
	}
	if r.Body == nil {
		return failedWebPush(ctx, notify.ErrInvalidConfig), nil
	}
	input := r.Body
	result, err := s.webPushSvc.Register(ctx, GetActor(ctx), input.Id, input.VapidVersion, ports.WebPushSubscription{Endpoint: input.Subscription.Endpoint, P256DH: input.Subscription.P256dh, Auth: input.Subscription.Auth}, r.Params.IdempotencyKey)
	if err != nil {
		return failedWebPush(ctx, err), nil
	}
	return RegisterWebPushDevice201JSONResponse(mapWebPushDevice(result)), nil
}
func (s *ServerHandler) RevokeWebPushDevice(ctx context.Context, r RevokeWebPushDeviceRequestObject) (RevokeWebPushDeviceResponseObject, error) {
	if err := s.webPushReady(ctx); err != nil {
		return failedWebPush(ctx, err), nil
	}
	if r.Body == nil {
		return failedWebPush(ctx, notify.ErrInvalidConfig), nil
	}
	result, err := s.webPushSvc.Revoke(ctx, GetActor(ctx), r.Id, r.Body.ExpectedUpdatedAt, r.Params.IdempotencyKey)
	if err != nil {
		return failedWebPush(ctx, err), nil
	}
	return RevokeWebPushDevice200JSONResponse(mapWebPushDevice(result)), nil
}
func (s *ServerHandler) GetWebPushReceipt(ctx context.Context, r GetWebPushReceiptRequestObject) (GetWebPushReceiptResponseObject, error) {
	if err := s.webPushReady(ctx); err != nil {
		return failedWebPush(ctx, err), nil
	}
	result, err := s.webPushSvc.Receipt(ctx, GetActor(ctx), r.Key)
	if err != nil {
		return failedWebPush(ctx, err), nil
	}
	return GetWebPushReceipt200JSONResponse(mapWebPushDevice(result)), nil
}
