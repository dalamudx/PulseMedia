package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetChannelReplacementService(service *notify.ChannelReplacementService) {
	s.channelReplacementSvc = service
}

type channelReplacementFailure struct{ controlFailure }

func (r channelReplacementFailure) VisitReplaceNotificationChannelResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}

type channelReplacementSuccess struct {
	receipt *ports.ChannelReplacementReceipt
}

func (r channelReplacementSuccess) VisitReplaceNotificationChannelResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	return json.NewEncoder(w).Encode(r.receipt)
}
func (s *ServerHandler) ReplaceNotificationChannel(ctx context.Context, r ReplaceNotificationChannelRequestObject) (ReplaceNotificationChannelResponseObject, error) {
	fail := func(e error) ReplaceNotificationChannelResponseObject {
		f := failedControl(ctx, e)
		if errors.Is(e, ports.ErrInvalidChannelReplacement) {
			f.status = 400
			f.body.Code = "INVALID_CHANNEL_REPLACEMENT"
		}
		if errors.Is(e, ports.ErrChannelReplacementConflict) {
			f.status = 409
			f.body.Code = "CHANNEL_REPLACEMENT_CONFLICT"
		}
		f.body.Message = "notification channel replacement could not be completed"
		return channelReplacementFailure{f}
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized), nil
	}
	if !a.IsAdmin() {
		return fail(ports.ErrForbidden), nil
	}
	if s.channelReplacementSvc == nil {
		return fail(ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidChannelReplacement), nil
	}
	raw, _ := json.Marshal(r.Body.Config)
	var cfg ports.NotificationChannelConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return fail(ports.ErrInvalidChannelReplacement), nil
	}
	receipt, e := s.channelReplacementSvc.Replace(ctx, a, r.Id, ports.ChannelReplacementCommand{ExpectedUpdatedAt: r.Body.ExpectedUpdatedAt, Name: r.Body.Name, ChannelType: string(r.Body.ChannelType), Config: cfg, AcceptOldPending: r.Body.AcceptOldPending}, r.Params.IdempotencyKey)
	if e != nil {
		return fail(e), nil
	}
	return channelReplacementSuccess{receipt}, nil
}
