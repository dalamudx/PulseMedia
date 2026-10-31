package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"regexp"
)

type ManagedSidecarUpdateApplication interface {
	Preview(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (ports.ManagedSidecarUpdateResult, error)
	Command(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (ports.ManagedSidecarUpdateResult, error)
	ReadCommand(context.Context, *ports.Actor, uuid.UUID, string) (ports.ManagedSidecarUpdateResult, error)
}

// RegisterManagedSidecarUpdateRoutes requires the existing AuthMiddleware.
func RegisterManagedSidecarUpdateRoutes(mux *http.ServeMux, svc ManagedSidecarUpdateApplication) {
	h := managedSidecarUpdateHTTP{svc: svc}
	mux.HandleFunc("POST /api/v1/library/files/{id}/sidecar-update-previews", h.preview)
	mux.HandleFunc("POST /api/v1/library/files/{id}/sidecar-update-commands", h.command)
	mux.HandleFunc("GET /api/v1/library/files/{id}/sidecar-update-commands/{key}", h.read)
}

type managedSidecarUpdateHTTP struct {
	svc ManagedSidecarUpdateApplication
}

var managedSidecarKey = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,80}$`)

func managedSidecarFailure(ctx context.Context, e error) mpBusinessResponse {
	if errors.Is(e, ports.ErrMediaSidecarStale) || errors.Is(e, ports.ErrMediaSidecarUnavailable) || errors.Is(e, ports.ErrFileIdentity) || errors.Is(e, ports.ErrFileConflict) {
		return mpBusinessResponse{status: http.StatusConflict, problem: true, body: problemResponse(http.StatusConflict, "MANAGED_SIDECAR_CHANGED", "read the current state; existing files and protection are retained", GetRequestID(ctx))}
	}
	return mpBusinessError(ctx, e)
}
func (h managedSidecarUpdateHTTP) authorize(w http.ResponseWriter, r *http.Request) (*ports.Actor, uuid.UUID, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	a := GetActor(r.Context())
	var e error
	switch {
	case a == nil:
		e = ports.ErrUnauthorized
	case a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil || len(r.Header.Values("Authorization")) != 0:
		e = ports.ErrForbidden
	case r.Method != http.MethodGet && !sameRequestOrigin(r):
		e = ports.ErrForbidden
	}
	id, ok := tvEpisodeUpgradeUUID(r.PathValue("id"))
	if e == nil && !ok {
		e = ports.ErrRejected
	}
	if e != nil {
		_ = managedSidecarFailure(r.Context(), e).write(w)
		return nil, uuid.Nil, false
	}
	return a, id, true
}
func managedSidecarBody(w http.ResponseWriter, r *http.Request, confirm bool) (uuid.UUID, bool) {
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
		return uuid.Nil, false
	}
	defer func() { _ = r.Body.Close() }()
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if e != nil {
		return uuid.Nil, false
	}
	fields := []string{"unit_id"}
	if confirm {
		fields = []string{"preview_id", "confirmed"}
	}
	obj, ok := selectionJSONObject(b, fields, "")
	if !ok {
		return uuid.Nil, false
	}
	name := "unit_id"
	if confirm {
		name = "preview_id"
		var yes bool
		if json.Unmarshal(obj["confirmed"], &yes) != nil || !yes {
			return uuid.Nil, false
		}
	}
	var raw string
	if json.Unmarshal(obj[name], &raw) != nil {
		return uuid.Nil, false
	}
	return tvEpisodeUpgradeUUID(raw)
}
func (h managedSidecarUpdateHTTP) respond(w http.ResponseWriter, r *http.Request, out ports.ManagedSidecarUpdateResult, e error) {
	if e != nil {
		_ = managedSidecarFailure(r.Context(), e).write(w)
		return
	}
	_ = mpBusinessResponse{status: http.StatusOK, body: out}.write(w)
}
func (h managedSidecarUpdateHTTP) preview(w http.ResponseWriter, r *http.Request) {
	a, file, ok := h.authorize(w, r)
	if !ok {
		return
	}
	unit, ok := managedSidecarBody(w, r, false)
	if !ok {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrRejected)
		return
	}
	if h.svc == nil {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrUnavailable)
		return
	}
	out, e := h.svc.Preview(r.Context(), a, file, unit)
	h.respond(w, r, out, e)
}
func (h managedSidecarUpdateHTTP) command(w http.ResponseWriter, r *http.Request) {
	a, file, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, ok := managedSidecarBody(w, r, true)
	keys := r.Header.Values("Idempotency-Key")
	if !ok || len(keys) != 1 || !managedSidecarKey.MatchString(keys[0]) {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrRejected)
		return
	}
	if h.svc == nil {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrUnavailable)
		return
	}
	out, e := h.svc.Command(r.Context(), a, file, id, keys[0])
	h.respond(w, r, out, e)
}
func (h managedSidecarUpdateHTTP) read(w http.ResponseWriter, r *http.Request) {
	a, file, ok := h.authorize(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !managedSidecarKey.MatchString(key) {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrRejected)
		return
	}
	if h.svc == nil {
		h.respond(w, r, ports.ManagedSidecarUpdateResult{}, ports.ErrUnavailable)
		return
	}
	out, e := h.svc.ReadCommand(r.Context(), a, file, key)
	h.respond(w, r, out, e)
}
