package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// TVEpisodeUpgradeApplication keeps registration independent of generated API types.
type TVEpisodeUpgradeApplication interface {
	Preview(context.Context, *ports.Actor, ports.TVEpisodeUpgradePreviewRequest) (*ports.TVEpisodeUpgradeResult, error)
	Command(context.Context, *ports.Actor, ports.TVEpisodeUpgradeCommand, string) (*ports.TVEpisodeUpgradeResult, error)
	ReadCommand(context.Context, *ports.Actor, uuid.UUID, string) (*ports.TVEpisodeUpgradeResult, error)
}

// RegisterTVEpisodeUpgradeRoutes must run on the router protected by AuthMiddleware.
func RegisterTVEpisodeUpgradeRoutes(r *http.ServeMux, service TVEpisodeUpgradeApplication) {
	h := tvEpisodeUpgradeHTTP{service: service}
	r.HandleFunc("POST /api/v1/subscriptions/{id}/tv-episode-upgrade-previews", h.PreviewTVEpisodeUpgrade)
	r.HandleFunc("POST /api/v1/subscriptions/{id}/tv-episode-upgrade-commands", h.CommandTVEpisodeUpgrade)
	r.HandleFunc("GET /api/v1/subscriptions/{id}/tv-episode-upgrade-commands/{key}", h.ReadTVEpisodeUpgradeCommand)
}

type tvEpisodeUpgradeHTTP struct{ service TVEpisodeUpgradeApplication }

func tvEpisodeUpgradeFailure(ctx context.Context, err error) mpBusinessResponse {
	if errors.Is(err, ports.ErrTVEpisodeUpgradeConflict) || errors.Is(err, ports.ErrTVEpisodeUpgradeObservation) {
		return mpBusinessResponse{status: http.StatusConflict, problem: true, body: problemResponse(http.StatusConflict, "TV_EPISODE_UPGRADE_CONFLICT", "read the current state; existing protection is retained", GetRequestID(ctx))}
	}
	return mpBusinessError(ctx, err)
}

func (h tvEpisodeUpgradeHTTP) authorize(w http.ResponseWriter, r *http.Request) (*ports.Actor, uuid.UUID, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	a := GetActor(r.Context())
	var err error
	switch {
	case a == nil:
		err = ports.ErrUnauthorized
	case a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil || len(r.Header.Values("Authorization")) != 0:
		err = ports.ErrForbidden
	case r.Method != http.MethodGet && !sameRequestOrigin(r):
		err = ports.ErrForbidden
	}
	id, ok := tvEpisodeUpgradeUUID(r.PathValue("id"))
	if err == nil && !ok {
		err = ports.ErrRejected
	}
	if err != nil {
		_ = tvEpisodeUpgradeFailure(r.Context(), err).write(w)
		return nil, uuid.Nil, false
	}
	return a, id, true
}

func tvEpisodeUpgradeUUID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil && id != uuid.Nil && raw == id.String()
}

func tvEpisodeUpgradeKey(raw string) bool {
	if len(raw) < 1 || len(raw) > 128 {
		return false
	}
	for _, c := range raw {
		if c < 33 || c > 126 || c == ',' {
			return false
		}
	}
	return true
}

func tvEpisodeUpgradeBody(w http.ResponseWriter, r *http.Request, command bool) (ports.TVEpisodeUpgradeCommand, bool) {
	var out ports.TVEpisodeUpgradeCommand
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
		return out, false
	}
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		return out, false
	}
	required := []string{"unit_id", "current_file_id", "candidate_id"}
	optional := ""
	if command {
		required = append(required, "action", "expected_version", "confirmed")
		optional = "operation_id"
	}
	fields, ok := selectionJSONObject(raw, required, optional)
	if !ok {
		return out, false
	}
	for name, target := range map[string]*uuid.UUID{"unit_id": &out.UnitID, "current_file_id": &out.CurrentFileID, "candidate_id": &out.CandidateID, "operation_id": &out.OperationID} {
		value, exists := fields[name]
		if !exists {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return out, false
		}
		id, valid := tvEpisodeUpgradeUUID(text)
		if !valid {
			return out, false
		}
		*target = id
	}
	if command {
		if json.Unmarshal(fields["action"], &out.Action) != nil || json.Unmarshal(fields["expected_version"], &out.ExpectedVersion) != nil || json.Unmarshal(fields["confirmed"], &out.Confirmed) != nil || !out.Confirmed || !validUpgradeDigest(out.ExpectedVersion) {
			return out, false
		}
		switch out.Action {
		case "reserve":
			if out.OperationID != uuid.Nil {
				return out, false
			}
		case "prepare", "send", "observe", "organization-preview", "organization-confirm", "organize", "retirement-preview", "retirement-confirm", "retire":
			if out.OperationID == uuid.Nil {
				return out, false
			}
		default:
			return out, false
		}
	}
	return out, true
}

func (h tvEpisodeUpgradeHTTP) respond(w http.ResponseWriter, r *http.Request, result *ports.TVEpisodeUpgradeResult, err error, status int) {
	if err == nil && result == nil {
		err = ports.ErrUnavailable
	}
	if err != nil {
		_ = tvEpisodeUpgradeFailure(r.Context(), err).write(w)
		return
	}
	_ = mpBusinessResponse{status: status, body: result}.write(w)
}

func (h tvEpisodeUpgradeHTTP) PreviewTVEpisodeUpgrade(w http.ResponseWriter, r *http.Request) {
	a, id, ok := h.authorize(w, r)
	if !ok {
		return
	}
	body, ok := tvEpisodeUpgradeBody(w, r, false)
	if !ok {
		_ = mpBusinessError(r.Context(), ports.ErrRejected).write(w)
		return
	}
	if h.service == nil {
		h.respond(w, r, nil, ports.ErrUnavailable, 0)
		return
	}
	body.SubscriptionID = id
	result, err := h.service.Preview(r.Context(), a, body.TVEpisodeUpgradePreviewRequest)
	h.respond(w, r, result, err, http.StatusOK)
}

func (h tvEpisodeUpgradeHTTP) CommandTVEpisodeUpgrade(w http.ResponseWriter, r *http.Request) {
	a, id, ok := h.authorize(w, r)
	if !ok {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !tvEpisodeUpgradeKey(keys[0]) {
		_ = mpBusinessError(r.Context(), ports.ErrRejected).write(w)
		return
	}
	body, ok := tvEpisodeUpgradeBody(w, r, true)
	if !ok {
		_ = mpBusinessError(r.Context(), ports.ErrRejected).write(w)
		return
	}
	if h.service == nil {
		h.respond(w, r, nil, ports.ErrUnavailable, 0)
		return
	}
	body.SubscriptionID = id
	result, err := h.service.Command(r.Context(), a, body, keys[0])
	h.respond(w, r, result, err, http.StatusAccepted)
}

func (h tvEpisodeUpgradeHTTP) ReadTVEpisodeUpgradeCommand(w http.ResponseWriter, r *http.Request) {
	a, id, ok := h.authorize(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !tvEpisodeUpgradeKey(key) || strings.ContainsAny(key, "/\\") {
		_ = mpBusinessError(r.Context(), ports.ErrRejected).write(w)
		return
	}
	if h.service == nil {
		h.respond(w, r, nil, ports.ErrUnavailable, 0)
		return
	}
	result, err := h.service.ReadCommand(r.Context(), a, id, key)
	h.respond(w, r, result, err, http.StatusOK)
}
