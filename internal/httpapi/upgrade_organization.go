package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
)

func (s *ServerHandler) SetPublicUpgradeOrganizationService(service *acquisition.PublicUpgradeOrganizationService) {
	s.upgradeOrganizationSvc = service
}

type upgradeOrganizationResponse struct {
	status int
	body   any
}

func (r upgradeOrganizationResponse) VisitGetSubscriptionUpgradeOrganizationResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (r upgradeOrganizationResponse) VisitPreviewSubscriptionUpgradeOrganizationResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (r upgradeOrganizationResponse) VisitAuthorizeSubscriptionUpgradeOrganizationResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (r upgradeOrganizationResponse) VisitPrepareSubscriptionUpgradeOrganizationResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (r upgradeOrganizationResponse) VisitQueueSubscriptionUpgradeOrganizationResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func upgradeOrganizationFailed(ctx context.Context, e error) upgradeOrganizationResponse {
	r := failedUpgradeCommand(ctx, e)
	return upgradeOrganizationResponse{r.status, r.body}
}
func upgradeOrganizationInvalid(ctx context.Context) upgradeOrganizationResponse {
	r := invalidUpgradeCommand(ctx)
	return upgradeOrganizationResponse{r.status, r.body}
}
func (s *ServerHandler) GetSubscriptionUpgradeOrganization(ctx context.Context, r GetSubscriptionUpgradeOrganizationRequestObject) (GetSubscriptionUpgradeOrganizationResponseObject, error) {
	if r.Id == uuid.Nil || r.OperationId == uuid.Nil {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeOrganizationSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeOrganizationSvc.State(ctx, GetActor(ctx), r.Id, r.OperationId)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.OperationId || out.CurrentFileID == uuid.Nil || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusOK, out}, nil
}
func (s *ServerHandler) PreviewSubscriptionUpgradeOrganization(ctx context.Context, r PreviewSubscriptionUpgradeOrganizationRequestObject) (PreviewSubscriptionUpgradeOrganizationResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeOrganizationSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeOrganizationSvc.Review(ctx, GetActor(ctx), r.Id, r.Body.OperationId)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.SubscriptionID != r.Id || out.ObservationID == uuid.Nil || out.TaskID == uuid.Nil || !validUpgradeDigest(out.InputStamp) || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusOK, out}, nil
}
func (s *ServerHandler) AuthorizeSubscriptionUpgradeOrganization(ctx context.Context, r AuthorizeSubscriptionUpgradeOrganizationRequestObject) (AuthorizeSubscriptionUpgradeOrganizationResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil || r.Body.ObservationId == uuid.Nil || !bool(r.Body.Confirmed) || !validUpgradeDigest(r.Body.InputStamp) {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeOrganizationSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeOrganizationSvc.Authorize(ctx, GetActor(ctx), r.Id, ports.AuthorizeMovieUpgradeOrganization{OperationID: r.Body.OperationId, ObservationID: r.Body.ObservationId, InputStamp: r.Body.InputStamp, Confirmed: true}, r.Params.IdempotencyKey)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.ObservationID != r.Body.ObservationId || out.ID == uuid.Nil || out.TaskID == uuid.Nil || out.AuthorizedAt.IsZero() || out.Status != "organization_authorized_not_executable" || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusAccepted, out}, nil
}
func (s *ServerHandler) PrepareSubscriptionUpgradeOrganization(ctx context.Context, r PrepareSubscriptionUpgradeOrganizationRequestObject) (PrepareSubscriptionUpgradeOrganizationResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil || r.Body.AuthorizationId == uuid.Nil || !bool(r.Body.Confirmed) {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeOrganizationSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeOrganizationSvc.Prepare(ctx, GetActor(ctx), r.Id, ports.PrepareMovieUpgradeOrganization{OperationID: r.Body.OperationId, AuthorizationID: r.Body.AuthorizationId, Confirmed: true}, r.Params.IdempotencyKey)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.AuthorizationID != r.Body.AuthorizationId || out.ID == uuid.Nil || out.PlanID == uuid.Nil || out.StepID == uuid.Nil || out.TaskID == uuid.Nil || out.PreparedAt.IsZero() || out.Status != "organization_prepared_not_executable" || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusAccepted, out}, nil
}
func (s *ServerHandler) QueueSubscriptionUpgradeOrganization(ctx context.Context, r QueueSubscriptionUpgradeOrganizationRequestObject) (QueueSubscriptionUpgradeOrganizationResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil || r.Body.PreparationId == uuid.Nil || !bool(r.Body.Confirmed) {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeOrganizationSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeOrganizationSvc.Queue(ctx, GetActor(ctx), r.Id, ports.QueueMovieUpgradeOrganization{OperationID: r.Body.OperationId, PreparationID: r.Body.PreparationId, Confirmed: true}, r.Params.IdempotencyKey)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.PreparationID != r.Body.PreparationId || out.CommandID == uuid.Nil || out.JobID == uuid.Nil || out.QueuedAt.IsZero() || out.Status != "queued" || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusAccepted, out}, nil
}
func strictUpgradeOrganizationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := map[string][]string{
			"POST /api/v1/subscriptions/{id}/upgrade-organization-previews":       {"operation_id"},
			"POST /api/v1/subscriptions/{id}/upgrade-organization-authorizations": {"operation_id", "observation_id", "input_stamp", "confirmed"},
			"POST /api/v1/subscriptions/{id}/upgrade-organization-preparations":   {"operation_id", "authorization_id", "confirmed"},
			"POST /api/v1/subscriptions/{id}/upgrade-organization-jobs":           {"operation_id", "preparation_id", "confirmed"},
			"POST /api/v1/subscriptions/{id}/upgrade-retirement-previews":         {"operation_id"},
			"POST /api/v1/subscriptions/{id}/upgrade-retirements":                 {"operation_id", "input_stamp", "confirmed"},
		}
		expected, ok := fields[r.Pattern]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		reject := func() {
			_ = upgradeOrganizationInvalid(r.Context()).VisitPreviewSubscriptionUpgradeOrganizationResponse(w)
		}
		command := len(expected) > 1
		if command {
			keys := r.Header.Values("Idempotency-Key")
			if len(keys) != 1 || keys[0] == "" || len(keys[0]) > 100 || strings.TrimSpace(keys[0]) != keys[0] || strings.ContainsAny(keys[0], ",\r\n\t") {
				reject()
				return
			}
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		if e != nil || len(body) > 64*1024 {
			reject()
			return
		}
		object, ok := selectionJSONObject(body, expected, "")
		if !ok {
			reject()
			return
		}
		if command && !bytes.Equal(bytes.TrimSpace(object["confirmed"]), []byte("true")) {
			reject()
			return
		}
		for name, raw := range object {
			if name == "confirmed" {
				continue
			}
			var v string
			if json.Unmarshal(raw, &v) != nil || v == "" {
				reject()
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
