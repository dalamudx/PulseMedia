package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"net/http"
)

func (s *ServerHandler) SetTransferRevisionService(v *transfer.RevisionService) { s.revisionSvc = v }
func (s *ServerHandler) PreviewTransferRevision(ctx context.Context, r PreviewTransferRevisionRequestObject) (PreviewTransferRevisionResponseObject, error) {
	if s.revisionSvc == nil {
		return recoveryFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return recoveryFailure(ctx, ports.ErrTransferReviewRequired), nil
	}
	action := ""
	if r.Body.Action != nil {
		action = string(*r.Body.Action)
	}
	p, e := s.revisionSvc.PreviewAction(ctx, GetActor(ctx), r.Id, r.Body.TargetRel, action)
	if e != nil {
		return recoveryFailure(ctx, e), nil
	}
	var mediaType *TransferRevisionPreviewMediaType
	if p.MediaType == "movie" || p.MediaType == "tv" {
		m := TransferRevisionPreviewMediaType(p.MediaType)
		mediaType = &m
	}
	var unitKey *string
	if p.UnitKey != "" {
		unitKey = &p.UnitKey
	}
	var selectedAction *TransferRevisionPreviewAction
	if p.Action != "" {
		v := TransferRevisionPreviewAction(p.Action)
		selectedAction = &v
	}
	return privateRevisionPreview{Action: selectedAction, MediaType: mediaType, UnitKey: unitKey, PlanId: p.PlanID, TaskId: p.TaskID, TargetRel: p.TargetRel, OriginalTargetRel: p.OriginalTargetRel, SourceSize: p.SourceSize, InputStamp: p.InputStamp, ObservedAt: p.ObservedAt, Revisable: p.Revisable, Reason: p.Reason}, nil
}
func (s *ServerHandler) CommitTransferRevision(ctx context.Context, r CommitTransferRevisionRequestObject) (CommitTransferRevisionResponseObject, error) {
	if s.revisionSvc == nil {
		return recoveryFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return recoveryFailure(ctx, ports.ErrTransferReviewRequired), nil
	}
	b := r.Body
	action := ""
	if b.Action != nil {
		action = string(*b.Action)
	}
	p, e := s.revisionSvc.Commit(ctx, GetActor(ctx), ports.TransferRevisionCommand{PlanID: r.Id, TargetRel: b.TargetRel, Action: action, ExpectedInputStamp: b.ExpectedInputStamp, Confirmed: bool(b.Confirmed), Reason: b.Reason}, r.Params.IdempotencyKey)
	if e != nil {
		return recoveryFailure(ctx, e), nil
	}
	return privateRevisionReceipt{PreviousPlanId: p.PreviousPlanID, PlanId: p.PlanID, TaskId: p.TaskID, JobId: p.JobID, State: TransferRevisionReceiptState(p.State)}, nil
}
func (p transferRecoveryResponse) VisitPreviewTransferRevisionResponse(w http.ResponseWriter) error {
	return p.write(w)
}
func (p transferRecoveryResponse) VisitCommitTransferRevisionResponse(w http.ResponseWriter) error {
	return p.write(w)
}

type privateRevisionPreview PreviewTransferRevision200JSONResponse

func (p privateRevisionPreview) VisitPreviewTransferRevisionResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return PreviewTransferRevision200JSONResponse(p).VisitPreviewTransferRevisionResponse(w)
}

type privateRevisionReceipt CommitTransferRevision202JSONResponse

func (p privateRevisionReceipt) VisitCommitTransferRevisionResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	return CommitTransferRevision202JSONResponse(p).VisitCommitTransferRevisionResponse(w)
}
