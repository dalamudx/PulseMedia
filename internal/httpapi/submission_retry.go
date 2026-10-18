package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

type submissionRetryResponse struct{ transferRecoveryResponse }

func (r submissionRetryResponse) VisitPreviewSubmissionRetryResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r submissionRetryResponse) VisitCommitSubmissionRetryResponse(w http.ResponseWriter) error {
	return r.VisitCommitOriginalTransferRecoveryResponse(w)
}
func retryFailure(ctx context.Context, e error) submissionRetryResponse {
	f := failedControl(ctx, e)
	f.body.Message = "submission retry could not be completed"
	if errors.Is(e, ports.ErrSubmissionRetryConflict) {
		f.status = 409
		f.body.Code = "SUBMISSION_RETRY_CONFLICT"
	}
	return submissionRetryResponse{transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{failure: &f}}}
}
func (s *ServerHandler) PreviewSubmissionRetry(ctx context.Context, r PreviewSubmissionRetryRequestObject) (PreviewSubmissionRetryResponseObject, error) {
	if s.submissionRetrySvc == nil {
		return retryFailure(ctx, ports.ErrUnavailable), nil
	}
	v, e := s.submissionRetrySvc.Preview(ctx, GetActor(ctx), r.Id)
	if e != nil {
		return retryFailure(ctx, e), nil
	}
	return submissionRetryResponse{transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: v}}}, nil
}
func (s *ServerHandler) CommitSubmissionRetry(ctx context.Context, r CommitSubmissionRetryRequestObject) (CommitSubmissionRetryResponseObject, error) {
	if s.submissionRetrySvc == nil {
		return retryFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return retryFailure(ctx, ports.ErrSubmissionRetryConflict), nil
	}
	v, e := s.submissionRetrySvc.Retry(ctx, GetActor(ctx), ports.SubmissionRetryCommand{OperationID: r.Id, JobID: r.Body.JobId, Key: r.Params.IdempotencyKey, ExpectedStamp: r.Body.ExpectedInputStamp, Acknowledged: bool(r.Body.Acknowledged)})
	if e != nil {
		return retryFailure(ctx, e), nil
	}
	return submissionRetryResponse{transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: v}, accepted: true}}, nil
}
