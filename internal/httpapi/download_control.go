package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"net/http"
)

func (s *ServerHandler) SetDownloadControlService(service *download.ControlService) {
	s.downloadControlSvc = service
}
func downloadControlDTO(r *ports.DownloadControlReceipt) DownloadControlReceipt {
	return DownloadControlReceipt{CommandId: r.CommandID, OperationId: r.OperationID, Action: DownloadControlReceiptAction(r.Action), State: DownloadControlReceiptState(r.State)}
}
func (s *ServerHandler) ControlOperationDownload(ctx context.Context, r ControlOperationDownloadRequestObject) (ControlOperationDownloadResponseObject, error) {
	if s.downloadControlSvc == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return downloadControlProblem(ctx, ports.ErrDownloadControlInvalid), nil
	}
	shared := r.Body.AffectShared != nil && *r.Body.AffectShared
	receipt, e := s.downloadControlSvc.Apply(ctx, GetActor(ctx), r.Id, r.Params.IdempotencyKey, ports.DownloadControlCommand{Action: string(r.Body.Action), Confirmed: bool(r.Body.Confirmed), AffectShared: shared})
	if e != nil {
		return downloadControlProblem(ctx, e), nil
	}
	if receipt == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	return downloadControlSuccess{downloadControlDTO(receipt)}, nil
}
func (s *ServerHandler) GetOperationDownloadControl(ctx context.Context, r GetOperationDownloadControlRequestObject) (GetOperationDownloadControlResponseObject, error) {
	if s.downloadControlSvc == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, e := s.downloadControlSvc.Get(ctx, GetActor(ctx), r.Id, r.Params.IdempotencyKey)
	if e != nil {
		return downloadControlProblem(ctx, e), nil
	}
	if receipt == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	return downloadControlSuccess{downloadControlDTO(receipt)}, nil
}
func (s *ServerHandler) ObserveOperationDownloadControl(ctx context.Context, r ObserveOperationDownloadControlRequestObject) (ObserveOperationDownloadControlResponseObject, error) {
	if s.downloadControlSvc == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, e := s.downloadControlSvc.Observe(ctx, GetActor(ctx), r.Id, r.Params.IdempotencyKey)
	if e != nil {
		return downloadControlProblem(ctx, e), nil
	}
	if receipt == nil {
		return downloadControlProblem(ctx, ports.ErrUnavailable), nil
	}
	return downloadControlSuccess{downloadControlDTO(receipt)}, nil
}

type downloadControlSuccess struct{ receipt DownloadControlReceipt }

func (r downloadControlSuccess) VisitControlOperationDownloadResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return ControlOperationDownload200JSONResponse(r.receipt).VisitControlOperationDownloadResponse(w)
}
func (r downloadControlSuccess) VisitGetOperationDownloadControlResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return GetOperationDownloadControl200JSONResponse(r.receipt).VisitGetOperationDownloadControlResponse(w)
}
func (r downloadControlSuccess) VisitObserveOperationDownloadControlResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return ObserveOperationDownloadControl200JSONResponse(r.receipt).VisitObserveOperationDownloadControlResponse(w)
}

type downloadControlHTTPError struct{ problem ErrorResponse }

func downloadControlProblem(ctx context.Context, e error) downloadControlHTTPError {
	status, code := 503, "DOWNLOAD_CONTROL_UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(e, ports.ErrDownloadControlInvalid):
		status, code = 400, "INVALID_DOWNLOAD_CONTROL"
	case errors.Is(e, ports.ErrDownloadControlConflict), errors.Is(e, ports.ErrDownloadControlUnsupported):
		status, code = 409, "DOWNLOAD_CONTROL_REVIEW_REQUIRED"
	}
	return downloadControlHTTPError{problemResponse(status, code, "download control could not be completed; restore the original receipt before another command", GetRequestID(ctx))}
}
func (r downloadControlHTTPError) VisitControlOperationDownloadResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r downloadControlHTTPError) VisitGetOperationDownloadControlResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r downloadControlHTTPError) VisitObserveOperationDownloadControlResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
