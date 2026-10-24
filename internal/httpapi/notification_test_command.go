package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"net/http"
)

func (s *ServerHandler) SetNotificationTestService(service *notify.TestService) {
	s.notificationTestSvc = service
}

type notificationTestFailure struct{ controlFailure }

func (r notificationTestFailure) VisitQueueNotificationTestResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (s *ServerHandler) QueueNotificationTest(ctx context.Context, r QueueNotificationTestRequestObject) (QueueNotificationTestResponseObject, error) {
	fail := func(e error) QueueNotificationTestResponseObject {
		f := failedControl(ctx, e)
		if errors.Is(e, notify.ErrInvalidConfig) {
			f.status = 400
			f.body.Code = "INVALID_REQUEST"
		}
		if errors.Is(e, ports.ErrChannelStateConflict) {
			f.status = 409
			f.body.Code = "CONFLICT"
		}
		f.body.Message = "notification test could not be queued"
		return notificationTestFailure{f}
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized), nil
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return fail(ports.ErrForbidden), nil
	}
	if r.Body == nil {
		return fail(notify.ErrInvalidConfig), nil
	}
	if s.notificationTestSvc == nil {
		return fail(ports.ErrUnavailable), nil
	}
	result, e := s.notificationTestSvc.Queue(ctx, a, r.Id, r.Body.ExpectedUpdatedAt, r.Params.IdempotencyKey)
	if e != nil {
		return fail(e), nil
	}
	return QueueNotificationTest202JSONResponse{ChannelId: result.ChannelID, EventId: result.EventID, Status: "queued"}, nil
}
