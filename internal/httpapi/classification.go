package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/classification"
	"net/http"
)

func (s *ServerHandler) SetClassificationService(svc *classification.Service) {
	s.classificationSvc = svc
}
func (s *ServerHandler) GetClassification(ctx context.Context, r GetClassificationRequestObject) (GetClassificationResponseObject, error) {
	if s.classificationSvc == nil {
		return classificationResponse{err: ports.ErrUnavailable}, nil
	}
	out, e := s.classificationSvc.Read(ctx, GetActor(ctx))
	return classificationResponse{body: out, err: e}, nil
}
func (s *ServerHandler) PublishClassification(ctx context.Context, r PublishClassificationRequestObject) (PublishClassificationResponseObject, error) {
	if s.classificationSvc == nil {
		return classificationResponse{err: ports.ErrUnavailable}, nil
	}
	if r.Body == nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	raw, e := json.Marshal(r.Body)
	if e != nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	var in ports.ClassificationPublish
	if json.Unmarshal(raw, &in) != nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	out, e := s.classificationSvc.Publish(ctx, GetActor(ctx), in, r.Params.IdempotencyKey)
	return classificationResponse{body: out, err: e}, nil
}
func (s *ServerHandler) PreviewClassification(ctx context.Context, r PreviewClassificationRequestObject) (PreviewClassificationResponseObject, error) {
	if s.classificationSvc == nil {
		return classificationResponse{err: ports.ErrUnavailable}, nil
	}
	if r.Body == nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	raw, e := json.Marshal(r.Body)
	if e != nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	var in ports.ClassificationPreviewInput
	if json.Unmarshal(raw, &in) != nil {
		return classificationResponse{err: ports.ErrInvalidClassification}, nil
	}
	out, e := s.classificationSvc.Preview(ctx, GetActor(ctx), in)
	return classificationResponse{body: out, err: e}, nil
}

type classificationResponse struct {
	body any
	err  error
}

func (r classificationResponse) VisitGetClassificationResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r classificationResponse) VisitPublishClassificationResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r classificationResponse) VisitPreviewClassificationResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r classificationResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.err == nil {
		w.WriteHeader(http.StatusOK)
		return json.NewEncoder(w).Encode(r.body)
	}
	status, code, message := http.StatusServiceUnavailable, "UNAVAILABLE", "分类状态不可用，请刷新后核对。"
	switch {
	case errors.Is(r.err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "请登录。"
	case errors.Is(r.err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "当前账号无权访问。"
	case errors.Is(r.err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "分类版本或作品不存在。"
	case errors.Is(r.err, ports.ErrInvalidClassification):
		status, code, message = 400, "INVALID_CLASSIFICATION", "请检查分类条件与目标根。"
	case errors.Is(r.err, ports.ErrClassificationConflict):
		status, code, message = 409, "CLASSIFICATION_CONFLICT", "分类版本或目标根已变化，请刷新。"
	case errors.Is(r.err, ports.ErrClassificationEvidence):
		status, code, message = 409, "INSUFFICIENT_EVIDENCE", "缺少可信元数据，无法选择目标根。"
	}
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(ErrorResponse{Code: code, Message: message})
}
