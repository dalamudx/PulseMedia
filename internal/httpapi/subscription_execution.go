package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetSubscriptionExecutionService(svc *subscription.ExecutionService) {
	s.subExecutionSvc = svc
}
func (s *ServerHandler) GetSubscriptionExecution(ctx context.Context, r GetSubscriptionExecutionRequestObject) (GetSubscriptionExecutionResponseObject, error) {
	if s.subExecutionSvc == nil {
		return executionResponse{err: ports.ErrUnavailable}, nil
	}
	out, e := s.subExecutionSvc.Read(ctx, GetActor(ctx), r.Id)
	return executionResponse{body: out, err: e}, nil
}

type executionResponse struct {
	body *ports.SubscriptionExecution
	err  error
}

func (r executionResponse) VisitGetSubscriptionExecutionResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.err == nil && r.body != nil {
		w.WriteHeader(http.StatusOK)
		return json.NewEncoder(w).Encode(r.body)
	}
	status, code, message := 503, "UNAVAILABLE", "执行详情不可用，请刷新。"
	switch {
	case errors.Is(r.err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "请登录。"
	case errors.Is(r.err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "当前账号无权查看该订阅。"
	case errors.Is(r.err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "订阅不存在。"
	}
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(ErrorResponse{Code: code, Message: message})
}
