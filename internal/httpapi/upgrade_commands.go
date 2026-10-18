package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetUpgradeReservationService(service *acquisition.UpgradeReservationService) {
	s.upgradeReservationSvc = service
}

type upgradeCommandFailure struct{ controlFailure }

func (e upgradeCommandFailure) VisitSetSubscriptionUpgradeModeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, e.status, e.body)
}
func (e upgradeCommandFailure) VisitReserveSubscriptionUpgradeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, e.status, e.body)
}
func failedUpgradeCommand(ctx context.Context, err error) upgradeCommandFailure {
	failure := failedControl(ctx, err)
	failure.body.Message = "upgrade command could not be completed"
	if errors.Is(err, ports.ErrUpgradeReservationConflict) || errors.Is(err, ports.ErrMovieUpgradeMetainfoReview) || errors.Is(err, ports.ErrMovieUpgradeOrganizationReview) {
		failure.status, failure.body.Code = http.StatusConflict, "UPGRADE_CONFLICT"
	}
	return upgradeCommandFailure{failure}
}
func invalidUpgradeCommand(ctx context.Context) upgradeCommandFailure {
	return upgradeCommandFailure{controlFailure{http.StatusBadRequest, ErrorResponse{Code: "INVALID_UPGRADE_COMMAND", Message: "invalid upgrade command", RequestId: GetRequestID(ctx)}}}
}
func writeUpgradeCommandJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}

type upgradeModeSuccess struct{ result *ports.UpgradeModeResult }

func (e upgradeModeSuccess) VisitSetSubscriptionUpgradeModeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, http.StatusAccepted, e.result)
}

type upgradeReservationSuccess struct {
	result *ports.UpgradeReservationResult
}

func (e upgradeReservationSuccess) VisitReserveSubscriptionUpgradeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, http.StatusAccepted, e.result)
}

func (s *ServerHandler) SetSubscriptionUpgradeMode(ctx context.Context, r SetSubscriptionUpgradeModeRequestObject) (SetSubscriptionUpgradeModeResponseObject, error) {
	if r.Body == nil || r.Id == uuid.Nil || r.Body.ExpectedIntent < 1 || !bool(r.Body.Confirmed) || (ports.UpgradeMode(r.Body.Mode) != ports.UpgradeDisabled && !ports.UpgradeMode(r.Body.Mode).Enabled()) {
		return invalidUpgradeCommand(ctx), nil
	}
	if s.upgradeReservationSvc == nil {
		return failedUpgradeCommand(ctx, ports.ErrUnavailable), nil
	}
	result, err := s.upgradeReservationSvc.SetMode(ctx, GetActor(ctx), ports.SetUpgradeModeCommand{SubscriptionID: r.Id, ExpectedIntent: r.Body.ExpectedIntent, Mode: ports.UpgradeMode(r.Body.Mode), Confirmed: bool(r.Body.Confirmed)}, r.Params.IdempotencyKey)
	if err != nil {
		return failedUpgradeCommand(ctx, err), nil
	}
	if result == nil {
		return failedUpgradeCommand(ctx, ports.ErrUnavailable), nil
	}
	return upgradeModeSuccess{result}, nil
}
func (s *ServerHandler) ReserveSubscriptionUpgrade(ctx context.Context, r ReserveSubscriptionUpgradeRequestObject) (ReserveSubscriptionUpgradeResponseObject, error) {
	if r.Body == nil || r.Id == uuid.Nil || r.Body.CurrentFileId == uuid.Nil || r.Body.CandidateId == uuid.Nil || !bool(r.Body.Confirmed) || !validUpgradeVersion(r.Body.Expected) || !validUpgradeDigest(r.Body.ExpectedClientDigest) {
		return invalidUpgradeCommand(ctx), nil
	}
	if s.upgradeReservationSvc == nil {
		return failedUpgradeCommand(ctx, ports.ErrUnavailable), nil
	}
	expected := r.Body.Expected
	result, err := s.upgradeReservationSvc.Reserve(ctx, GetActor(ctx), ports.ReserveUpgradeCommand{SubscriptionID: r.Id, CurrentFileID: r.Body.CurrentFileId, CandidateID: r.Body.CandidateId, Expected: ports.UpgradePreviewVersion{ActorID: expected.ActorId, SubscriptionID: expected.SubscriptionId, MediaID: expected.MediaId, UnitID: expected.UnitId, RootID: expected.RootId, IntentVersion: expected.IntentVersion, PolicyID: expected.PolicyId, PolicyRevision: expected.PolicyRevision, InputStamp: expected.InputStamp}, ExpectedClientDigest: r.Body.ExpectedClientDigest, Confirmed: bool(r.Body.Confirmed)}, r.Params.IdempotencyKey)
	if err != nil {
		return failedUpgradeCommand(ctx, err), nil
	}
	if result == nil {
		return failedUpgradeCommand(ctx, ports.ErrUnavailable), nil
	}
	return upgradeReservationSuccess{result}, nil
}
func validUpgradeVersion(v UpgradePreviewVersion) bool {
	return v.ActorId != uuid.Nil && v.SubscriptionId != uuid.Nil && v.MediaId != uuid.Nil && v.UnitId != uuid.Nil && v.RootId != uuid.Nil && v.IntentVersion > 0 && v.PolicyRevision > 0 && strings.TrimSpace(v.PolicyId) != "" && strings.TrimSpace(v.InputStamp) != ""
}
func validUpgradeDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}
func strictUpgradeCommandsJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.Method == http.MethodPost && r.Pattern == "POST /api/v1/subscriptions/{id}/upgrade-modes"
		reservation := r.Method == http.MethodPost && r.Pattern == "POST /api/v1/subscriptions/{id}/upgrade-reservations"
		preparation := r.Method == http.MethodPost && r.Pattern == "POST /api/v1/subscriptions/{id}/upgrade-preparations"
		send := r.Method == http.MethodPost && r.Pattern == "POST /api/v1/subscriptions/{id}/upgrade-sends"
		if !mode && !reservation && !preparation && !send {
			next.ServeHTTP(w, r)
			return
		}
		reject := func() { _ = invalidUpgradeCommand(r.Context()).VisitSetSubscriptionUpgradeModeResponse(w) }
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 130 || strings.TrimSpace(keys[0]) != keys[0] || strings.ContainsAny(keys[0], ",\r\n\t") {
			reject()
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
		if err != nil || len(body) > 64*1024 {
			reject()
			return
		}
		var object map[string]json.RawMessage
		var ok bool
		if mode {
			object, ok = selectionJSONObject(body, []string{"expected_intent", "mode", "confirmed"}, "")
			if !ok || !bytes.Equal(bytes.TrimSpace(object["confirmed"]), []byte("true")) {
				reject()
				return
			}
		} else if send {
			object, ok = selectionJSONObject(body, []string{"operation_id", "task_id", "confirmed"}, "")
			if !ok || !bytes.Equal(bytes.TrimSpace(object["confirmed"]), []byte("true")) {
				reject()
				return
			}
		} else if preparation {
			object, ok = selectionJSONObject(body, []string{"operation_id", "confirmed"}, "")
			if !ok || !bytes.Equal(bytes.TrimSpace(object["confirmed"]), []byte("true")) {
				reject()
				return
			}
		} else {
			object, ok = selectionJSONObject(body, []string{"current_file_id", "candidate_id", "expected", "expected_client_digest", "confirmed"}, "")
			if !ok || !bytes.Equal(bytes.TrimSpace(object["confirmed"]), []byte("true")) {
				reject()
				return
			}
			if _, ok = selectionJSONObject(object["expected"], []string{"actor_id", "subscription_id", "media_id", "unit_id", "root_id", "intent_version", "policy_id", "policy_revision", "input_stamp"}, ""); !ok {
				reject()
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
