package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetChannelStateService(service *notify.ChannelStateService) {
	s.channelStateSvc = service
}

type channelStateFailure struct{ controlFailure }

func (f channelStateFailure) VisitSetNotificationChannelStateResponse(w http.ResponseWriter) error {
	return f.VisitControlSubscriptionResponse(w)
}

type channelStateSuccess struct{ receipt *ports.ChannelStateReceipt }

func (r channelStateSuccess) VisitSetNotificationChannelStateResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(r.receipt)
}
func (s *ServerHandler) SetNotificationChannelState(ctx context.Context, r SetNotificationChannelStateRequestObject) (SetNotificationChannelStateResponseObject, error) {
	fail := func(e error) SetNotificationChannelStateResponseObject {
		f := failedControl(ctx, e)
		if errors.Is(e, ports.ErrInvalidChannelState) {
			f.status = 400
			f.body.Code = "INVALID_CHANNEL_STATE"
		}
		if errors.Is(e, ports.ErrChannelStateConflict) {
			f.status = 409
			f.body.Code = "CHANNEL_STATE_CONFLICT"
		}
		f.body.Message = "notification channel state command could not be completed"
		return channelStateFailure{f}
	}
	if s.channelStateSvc == nil {
		return fail(ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidChannelState), nil
	}
	out, e := s.channelStateSvc.Set(ctx, GetActor(ctx), r.Id, ports.ChannelStateCommand{ExpectedUpdatedAt: r.Body.ExpectedUpdatedAt, IsEnabled: r.Body.IsEnabled}, r.Params.IdempotencyKey)
	if e != nil {
		return fail(e), nil
	}
	return channelStateSuccess{out}, nil
}
