package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// App supplies the service; HTTP never performs repository or provider I/O.
type mediaRequestApplication interface {
	Submit(context.Context, *ports.Actor, mediarequest.Intent, string) (*ports.MediaRequestReceipt, error)
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.MediaRequest, error)
	List(context.Context, *ports.Actor, int, *uuid.UUID) ([]ports.MediaRequest, error)
	Receipt(context.Context, *ports.Actor, string) (*ports.MediaRequestReceipt, error)
	Close(context.Context, *ports.Actor, uuid.UUID, ports.MediaRequestCloseInput, string) (*ports.MediaRequestReceipt, error)
	Approve(context.Context, *ports.Actor, uuid.UUID, ports.MediaRequestApprovalInput, string) (*ports.MediaRequestApprovalReceipt, error)
	ApprovalReceipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.MediaRequestApprovalReceipt, error)
}

func (s *ServerHandler) SetMediaRequestService(service mediaRequestApplication) {
	s.mediaRequestSvc = service
}

func mediaRequestActorError(actor *ports.Actor, admin bool) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || id.String() != actor.ID || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || (actor.Role != ports.RoleMember && actor.Role != ports.RoleAdmin) || (admin && actor.Role != ports.RoleAdmin) {
		return ports.ErrForbidden
	}
	return nil
}

func (s *ServerHandler) mediaRequestAdmission(ctx context.Context, admin bool) error {
	if err := mediaRequestActorError(GetActor(ctx), admin); err != nil {
		return err
	}
	if s.mediaRequestSvc == nil {
		return ports.ErrUnavailable
	}
	return nil
}

func (s *ServerHandler) ListMediaRequests(ctx context.Context, req ListMediaRequestsRequestObject) (ListMediaRequestsResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, false); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	limit := 25
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if limit < 1 || limit > 100 || req.Params.BeforeId != nil && *req.Params.BeforeId == uuid.Nil {
		return mediaRequestBadRequest(ctx), nil
	}
	var rows []ports.MediaRequest
	var err error
	if req.Params.State != nil {
		if string(*req.Params.State) != "pending" {
			return mediaRequestBadRequest(ctx), nil
		}
		if err := s.mediaRequestAdmission(ctx, true); err != nil {
			return mediaRequestError(ctx, err), nil
		}
		app, ok := s.mediaRequestSvc.(mediaRequestInboxApplication)
		if !ok {
			return mediaRequestError(ctx, ports.ErrUnavailable), nil
		}
		rows, err = app.Pending(ctx, GetActor(ctx), limit, req.Params.BeforeId)
	} else {
		rows, err = s.mediaRequestSvc.List(ctx, GetActor(ctx), limit, req.Params.BeforeId)
	}
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	items := make([]MediaRequestDetails, 0, len(rows))
	for _, row := range rows {
		items = append(items, mediaRequestDetails(row))
	}
	return ListMediaRequests200JSONResponse{Items: items}, nil
}

func (s *ServerHandler) SubmitMediaRequest(ctx context.Context, req SubmitMediaRequestRequestObject) (SubmitMediaRequestResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, false); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if req.Body == nil || !validMediaRequestKey(req.Params.IdempotencyKey) {
		return mediaRequestBadRequest(ctx), nil
	}
	b := req.Body
	in := mediarequest.Intent{MediaType: string(b.MediaType), TMDBID: b.TmdbId, Seasons: append([]int{}, b.Seasons...), IncludeSpecials: b.IncludeSpecials}
	if !validMediaRequestIntentTransport(in) {
		return mediaRequestBadRequest(ctx), nil
	}
	receipt, err := s.mediaRequestSvc.Submit(ctx, GetActor(ctx), in, req.Params.IdempotencyKey)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if receipt == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return SubmitMediaRequest201JSONResponse(mediaRequestCommandReceipt(*receipt)), nil
}

func (s *ServerHandler) GetMediaRequest(ctx context.Context, req GetMediaRequestRequestObject) (GetMediaRequestResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, false); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if req.RequestId == uuid.Nil {
		return mediaRequestBadRequest(ctx), nil
	}
	row, err := s.mediaRequestSvc.Read(ctx, GetActor(ctx), req.RequestId)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if row == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return GetMediaRequest200JSONResponse(mediaRequestDetails(*row)), nil
}

func (s *ServerHandler) GetMediaRequestReceipt(ctx context.Context, req GetMediaRequestReceiptRequestObject) (GetMediaRequestReceiptResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, false); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if !validMediaRequestKey(req.IdempotencyKey) {
		return mediaRequestBadRequest(ctx), nil
	}
	receipt, err := s.mediaRequestSvc.Receipt(ctx, GetActor(ctx), req.IdempotencyKey)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if receipt == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return GetMediaRequestReceipt200JSONResponse(mediaRequestCommandReceipt(*receipt)), nil
}

func (s *ServerHandler) CloseMediaRequest(ctx context.Context, req CloseMediaRequestRequestObject) (CloseMediaRequestResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, false); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if req.RequestId == uuid.Nil || req.Body == nil || !validMediaRequestKey(req.Params.IdempotencyKey) {
		return mediaRequestBadRequest(ctx), nil
	}
	b := req.Body
	in := ports.MediaRequestCloseInput{ExpectedVersion: b.ExpectedVersion, Action: string(b.Action), Reason: b.Reason}
	if !validMediaRequestCloseTransport(in) {
		return mediaRequestBadRequest(ctx), nil
	}
	receipt, err := s.mediaRequestSvc.Close(ctx, GetActor(ctx), req.RequestId, in, req.Params.IdempotencyKey)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if receipt == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return CloseMediaRequest200JSONResponse(mediaRequestCommandReceipt(*receipt)), nil
}

func (s *ServerHandler) ApproveMediaRequest(ctx context.Context, req ApproveMediaRequestRequestObject) (ApproveMediaRequestResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, true); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if req.RequestId == uuid.Nil || req.Body == nil || !validMediaRequestKey(req.Params.IdempotencyKey) {
		return mediaRequestBadRequest(ctx), nil
	}
	b := req.Body
	downloader := b.DownloaderId
	in := ports.MediaRequestApprovalInput{ExpectedVersion: b.ExpectedVersion, RootID: b.RootId, QualityPolicyID: b.QualityPolicyId, IndexerIDs: append([]uuid.UUID{}, b.IndexerIds...), DownloaderID: &downloader}
	if !validMediaRequestApprovalTransport(in) {
		return mediaRequestBadRequest(ctx), nil
	}
	receipt, err := s.mediaRequestSvc.Approve(ctx, GetActor(ctx), req.RequestId, in, req.Params.IdempotencyKey)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if receipt == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return ApproveMediaRequest201JSONResponse(mediaRequestApprovalResult(*receipt)), nil
}

func (s *ServerHandler) GetMediaRequestApprovalReceipt(ctx context.Context, req GetMediaRequestApprovalReceiptRequestObject) (GetMediaRequestApprovalReceiptResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, true); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if req.RequestId == uuid.Nil || !validMediaRequestKey(req.IdempotencyKey) {
		return mediaRequestBadRequest(ctx), nil
	}
	receipt, err := s.mediaRequestSvc.ApprovalReceipt(ctx, GetActor(ctx), req.RequestId, req.IdempotencyKey)
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if receipt == nil {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return GetMediaRequestApprovalReceipt200JSONResponse(mediaRequestApprovalResult(*receipt)), nil
}

func mediaRequestDetails(row ports.MediaRequest) MediaRequestDetails {
	return MediaRequestDetails{
		Id: row.ID, OwnerId: row.OwnerID,
		Intent: MediaRequestIntent{MediaType: MediaRequestIntentMediaType(row.Intent.MediaType), TmdbId: row.Intent.TMDBID, Seasons: append([]int{}, row.Intent.Seasons...), IncludeSpecials: row.Intent.IncludeSpecials},
		State:  MediaRequestDetailsState(row.State), Version: row.Version,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DecidedBy: row.DecidedBy,
		DecidedAt: row.DecidedAt, SubscriptionId: row.SubscriptionID, Reason: row.Reason,
	}
}

func mediaRequestCommandReceipt(receipt ports.MediaRequestReceipt) MediaRequestCommandReceipt {
	return MediaRequestCommandReceipt{CommandKey: receipt.CommandKey, Request: mediaRequestDetails(receipt.Request)}
}

func mediaRequestApprovalResult(receipt ports.MediaRequestApprovalReceipt) MediaRequestApprovalResult {
	// Match subscriptionCreationResult's public format, including embedded Wanted.
	sub := receipt.Subscription.Subscription
	wanted := make([]WantedItemDetails, 0, len(receipt.Subscription.Wanted))
	for _, item := range receipt.Subscription.Wanted {
		key := item.UnitKey
		wanted = append(wanted, WantedItemDetails{Id: item.ID, UnitId: item.UnitID, UnitKey: &key, IsSkipped: item.IsSkipped, IsSatisfied: item.IsSatisfied})
	}
	indexers := append([]uuid.UUID{}, sub.IndexerIDs...)
	return MediaRequestApprovalResult{RequestId: receipt.RequestID, Subscription: SubscriptionDetails{
		Id: sub.ID, UserId: sub.UserID, MediaId: sub.MediaID, RootId: sub.RootID,
		QualityPolicyId: sub.QualityPolicyID, QualityPolicyRevision: int(sub.QualityPolicyRevision),
		IndexerIds: &indexers, DownloaderId: sub.DownloaderID, State: sub.State,
		IntentVersion: int(sub.IntentVersion), ScopeType: sub.ScopeType, WantedItems: &wanted,
	}}
}

type mediaRequestFailure struct {
	status int
	body   ErrorResponse
}

func mediaRequestBadRequest(ctx context.Context) mediaRequestFailure {
	return mediaRequestFailure{status: http.StatusBadRequest, body: problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid media request", GetRequestID(ctx))}
}

func mediaRequestError(ctx context.Context, err error) mediaRequestFailure {
	status, code, message := http.StatusServiceUnavailable, "MEDIA_REQUEST_UNAVAILABLE", "media request result could not be verified"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "current account cannot access this media request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "media request, command or selected object not found"
	case errors.Is(err, ports.ErrMediaRequestConflict), errors.Is(err, ports.ErrSubscriptionBindingsConflict):
		status, code, message = http.StatusConflict, "VERSION_CONFLICT", "command or request changed; reread before changing the original request"
	case errors.Is(err, ports.ErrMediaRequestLimit):
		status, code, message = http.StatusTooManyRequests, "MEDIA_REQUEST_LIMIT", "too many pending media requests"
	case errors.Is(err, ports.ErrRejected), errors.Is(err, mediarequest.ErrInvalid), errors.Is(err, ports.ErrInvalidSubscriptionBindings):
		status, code, message = http.StatusUnprocessableEntity, "INVALID_MEDIA_REQUEST", "select a valid media intent and subscription configuration"
	case errors.Is(err, ports.ErrNoSubscriptionRoot), errors.Is(err, ports.ErrAmbiguousSubscriptionRoot):
		status, code, message = http.StatusUnprocessableEntity, "ROOT_SELECTION_REQUIRED", "select one matching online library root"
	}
	return mediaRequestFailure{status: status, body: problemResponse(status, code, message, GetRequestID(ctx))}
}

func (r mediaRequestFailure) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}

func (r mediaRequestFailure) VisitListMediaRequestsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitSubmitMediaRequestResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitGetMediaRequestResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitGetMediaRequestReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitCloseMediaRequestResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitApproveMediaRequestResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mediaRequestFailure) VisitGetMediaRequestApprovalReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
