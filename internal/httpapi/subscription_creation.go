package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// App assembles the implementation. HTTP maps input and the persisted result.
type subscriptionCreationApplication interface {
	Create(context.Context, *ports.Actor, ports.SubscriptionCreationInput, string) (*ports.SubscriptionCreationReceipt, error)
	Receipt(context.Context, *ports.Actor, string) (*ports.SubscriptionCreationReceipt, error)
}

func (s *ServerHandler) SetSubscriptionCreationService(service subscriptionCreationApplication) {
	s.subCreationSvc = service
}

func (s *ServerHandler) CreateSubscription(ctx context.Context, req CreateSubscriptionRequestObject) (CreateSubscriptionResponseObject, error) {
	if req.Body == nil {
		return subscriptionCreationError(ctx, ports.ErrInvalidSubscriptionBindings), nil
	}
	if s.subCreationSvc == nil {
		return subscriptionCreationError(ctx, ports.ErrUnavailable), nil
	}
	b := req.Body
	in := ports.SubscriptionCreationInput{
		MediaID: b.MediaId, TMDBID: b.TmdbId, RootID: b.RootId, ClassificationVersion: b.ClassificationVersion,
		DownloaderID: b.DownloaderId, ExpectedIntentVersion: b.ExpectedIntentVersion,
	}
	in.ClassificationPreviewRootID = b.ClassificationPreviewRootId
	if b.ClassificationPreviewStamp != nil {
		in.ClassificationPreviewStamp = *b.ClassificationPreviewStamp
	}
	if b.MediaType != nil {
		in.MediaType = string(*b.MediaType)
	}
	if b.QualityPolicyId != nil {
		in.QualityPolicyID = *b.QualityPolicyId
	}
	if b.ScopeType != nil {
		in.ScopeType = string(*b.ScopeType)
	}
	if b.IndexerIds != nil {
		in.IndexerIDs = append(in.IndexerIDs, (*b.IndexerIds)...)
		if in.IndexerIDs == nil {
			in.IndexerIDs = []uuid.UUID{}
		}
	}
	if b.Seasons != nil {
		in.Seasons = append([]int{}, (*b.Seasons)...)
	}
	if b.Episodes != nil {
		in.EpisodeKeys = append([]string{}, (*b.Episodes)...)
	}
	if b.SelectedUnitIds != nil {
		in.SelectedUnitIDs = append(in.SelectedUnitIDs, (*b.SelectedUnitIds)...)
	}
	if b.IncludeSpecials != nil {
		in.IncludeSpecials = *b.IncludeSpecials
	}
	receipt, err := s.subCreationSvc.Create(ctx, GetActor(ctx), in, req.Params.IdempotencyKey)
	if err != nil {
		return subscriptionCreationError(ctx, err), nil
	}
	return subscriptionCreationResult{receipt: receipt}, nil
}

func (s *ServerHandler) GetSubscriptionCreationReceipt(ctx context.Context, req GetSubscriptionCreationReceiptRequestObject) (GetSubscriptionCreationReceiptResponseObject, error) {
	if s.subCreationSvc == nil {
		return subscriptionCreationError(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.subCreationSvc.Receipt(ctx, GetActor(ctx), req.IdempotencyKey)
	if err != nil {
		return subscriptionCreationError(ctx, err), nil
	}
	return subscriptionCreationResult{receipt: receipt}, nil
}

type subscriptionCreationResult struct {
	receipt *ports.SubscriptionCreationReceipt
}

func (r subscriptionCreationResult) VisitCreateSubscriptionResponse(w http.ResponseWriter) error {
	return r.write(w, http.StatusCreated)
}
func (r subscriptionCreationResult) VisitGetSubscriptionCreationReceiptResponse(w http.ResponseWriter) error {
	return r.write(w, http.StatusOK)
}
func (r subscriptionCreationResult) write(w http.ResponseWriter, status int) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.receipt == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return json.NewEncoder(w).Encode(ErrorResponse{Code: "CREATE_UNAVAILABLE", Message: "subscription result could not be verified"})
	}
	sub := r.receipt.Subscription
	wanted := make([]WantedItemDetails, 0, len(r.receipt.Wanted))
	for _, item := range r.receipt.Wanted {
		key := item.UnitKey
		wanted = append(wanted, WantedItemDetails{Id: item.ID, UnitId: item.UnitID, UnitKey: &key, IsSkipped: item.IsSkipped, IsSatisfied: item.IsSatisfied})
	}
	indexers := append([]uuid.UUID{}, sub.IndexerIDs...)
	body := SubscriptionDetails{
		Id: sub.ID, UserId: sub.UserID, MediaId: sub.MediaID, RootId: sub.RootID,
		QualityPolicyId: sub.QualityPolicyID, QualityPolicyRevision: int(sub.QualityPolicyRevision),
		IndexerIds: &indexers, DownloaderId: sub.DownloaderID, State: sub.State,
		IntentVersion: int(sub.IntentVersion), ScopeType: sub.ScopeType, WantedItems: &wanted,
	}
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}

type subscriptionCreationFailure struct {
	status int
	body   ErrorResponse
}

func subscriptionCreationError(ctx context.Context, err error) subscriptionCreationFailure {
	status, code, message := http.StatusServiceUnavailable, "CREATE_UNAVAILABLE", "subscription command could not be verified"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "current account cannot access this subscription command"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "subscription command or selected object not found"
	case errors.Is(err, ports.ErrSubscriptionBindingsConflict):
		status, code, message = http.StatusConflict, "VERSION_CONFLICT", "command or intent changed; reread before changing the original request"
	case errors.Is(err, ports.ErrClassificationConflict):
		status, code, message = http.StatusConflict, "CLASSIFICATION_CONFLICT", "classification version or target changed; reread before creating"
	case errors.Is(err, ports.ErrClassificationEvidence):
		status, code, message = http.StatusConflict, "INSUFFICIENT_EVIDENCE", "trusted media metadata is required for classification"
	case errors.Is(err, ports.ErrNoSubscriptionRoot):
		status, code, message = http.StatusBadRequest, "NO_ROOT_AVAILABLE", "select a matching online library root"
	case errors.Is(err, ports.ErrAmbiguousSubscriptionRoot):
		status, code, message = http.StatusBadRequest, "ROOT_SELECTION_REQUIRED", "select one matching online library root explicitly"
	case errors.Is(err, ports.ErrInvalidSubscriptionBindings):
		status, code, message = http.StatusBadRequest, "INVALID_SCOPE", "select a valid media identity, scope and target configuration"
	}
	return subscriptionCreationFailure{status: status, body: ErrorResponse{Code: code, Message: message, RequestId: GetRequestID(ctx)}}
}
func (r subscriptionCreationFailure) VisitCreateSubscriptionResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r subscriptionCreationFailure) VisitGetSubscriptionCreationReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r subscriptionCreationFailure) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
