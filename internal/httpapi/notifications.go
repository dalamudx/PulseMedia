package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
)

// Injected by app before starting HTTP; no global registration or fallback key.
func (s *ServerHandler) SetNotificationDispatcher(d *notify.Dispatcher) { s.notifySvc = d }
func channelDetails(row ports.NotificationChannelObservation) NotificationChannelDetails {
	return NotificationChannelDetails{Id: row.ID, Name: row.Name, ChannelType: row.ChannelType, IsEnabled: row.IsEnabled, UpdatedAt: row.UpdatedAt, ReplacedBy: row.ReplacedBy}
}
func (s *ServerHandler) ListNotificationChannels(ctx context.Context, r ListNotificationChannelsRequestObject) (ListNotificationChannelsResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil || !a.IsAdmin() {
		return ListNotificationChannels403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if s.notifySvc == nil {
		return ListNotificationChannels503JSONResponse{Code: "UNAVAILABLE", Message: "notification service unavailable", RequestId: id}, nil
	}
	rows, err := s.notifySvc.ListChannels(ctx, a)
	if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
		return ListNotificationChannels403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if err != nil {
		return ListNotificationChannels503JSONResponse{Code: "UNAVAILABLE", Message: "notification channel observations unavailable", RequestId: id}, nil
	}
	out := ListNotificationChannels200JSONResponse{}
	for _, row := range rows {
		out = append(out, channelDetails(row))
	}
	return out, nil
}
func (s *ServerHandler) CreateNotificationChannel(ctx context.Context, r CreateNotificationChannelRequestObject) (CreateNotificationChannelResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil || !a.IsAdmin() {
		return CreateNotificationChannel403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if s.notifySvc == nil {
		return CreateNotificationChannel503JSONResponse{Code: "UNAVAILABLE", Message: "notification service unavailable", RequestId: id}, nil
	}
	if r.Body == nil {
		return CreateNotificationChannel400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid notification configuration", RequestId: id}, nil
	}
	plain, err := json.Marshal(r.Body.Config)
	if err != nil {
		return nil, err
	}
	var cfg notify.ChannelConfig
	if err = json.Unmarshal(plain, &cfg); err != nil {
		return nil, err
	}
	row, err := s.notifySvc.CreateChannel(ctx, a, r.Body.Name, string(r.Body.ChannelType), cfg, r.Params.IdempotencyKey)
	if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
		return CreateNotificationChannel403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if errors.Is(err, notify.ErrIdempotencyConflict) {
		return CreateNotificationChannel409JSONResponse{Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key belongs to a different command", RequestId: id}, nil
	}
	if errors.Is(err, notify.ErrInvalidConfig) {
		return CreateNotificationChannel400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid notification configuration", RequestId: id}, nil
	}
	if err != nil {
		return CreateNotificationChannel503JSONResponse{Code: "UNAVAILABLE", Message: "notification service unavailable", RequestId: id}, nil
	}
	return CreateNotificationChannel201JSONResponse(channelDetails(*row)), nil
}
func (s *ServerHandler) DisableNotificationChannel(ctx context.Context, r DisableNotificationChannelRequestObject) (DisableNotificationChannelResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil || !a.IsAdmin() {
		return DisableNotificationChannel403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if s.notifySvc == nil {
		return DisableNotificationChannel503JSONResponse{Code: "UNAVAILABLE", Message: "notification service unavailable", RequestId: id}, nil
	}
	return DisableNotificationChannel409JSONResponse{PolicyConflictJSONResponse: PolicyConflictJSONResponse{Code: "VERSIONED_COMMAND_REQUIRED", Message: "use the versioned channel state command", RequestId: id}}, nil
}
