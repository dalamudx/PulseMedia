package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"net/http"
)

func (s *ServerHandler) SetTransferRecoveryService(service *transfer.RecoveryService) {
	s.recoverySvc = service
}

type transferRecoveryResponse struct {
	episodeSourceResponse
	accepted bool
}

func (r transferRecoveryResponse) VisitPreviewOriginalTransferRecoveryResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r transferRecoveryResponse) VisitCommitOriginalTransferRecoveryResponse(w http.ResponseWriter) error {
	if r.failure != nil || !r.accepted {
		return r.write(w)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(202)
	return json.NewEncoder(w).Encode(r.body)
}
func recoveryFailure(ctx context.Context, e error) transferRecoveryResponse {
	if errors.Is(e, ports.ErrTransferReviewRequired) || errors.Is(e, ports.ErrTransferExecutionRejected) {
		e = ports.ErrEpisodeSourceConflict
	}
	return transferRecoveryResponse{episodeSourceResponse: episodeSourceFailure(ctx, e)}
}
func (s *ServerHandler) PreviewOriginalTransferRecovery(ctx context.Context, r PreviewOriginalTransferRecoveryRequestObject) (PreviewOriginalTransferRecoveryResponseObject, error) {
	if s.recoverySvc == nil {
		return recoveryFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.recoverySvc.Preview(ctx, GetActor(ctx), r.Id)
	if e != nil {
		return recoveryFailure(ctx, e), nil
	}
	return transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: out}}, nil
}
func (s *ServerHandler) CommitOriginalTransferRecovery(ctx context.Context, r CommitOriginalTransferRecoveryRequestObject) (CommitOriginalTransferRecoveryResponseObject, error) {
	if s.recoverySvc == nil {
		return recoveryFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return recoveryFailure(ctx, ports.ErrInvalidEpisodeMapping), nil
	}
	c := ports.TransferRecoveryCommand{PlanID: r.Id, ExpectedInputStamp: r.Body.ExpectedInputStamp, Confirmed: bool(r.Body.Confirmed), Reason: r.Body.Reason}
	out, e := s.recoverySvc.Commit(ctx, GetActor(ctx), c, r.Params.IdempotencyKey)
	if e != nil {
		return recoveryFailure(ctx, e), nil
	}
	return transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: out}, accepted: true}, nil
}
