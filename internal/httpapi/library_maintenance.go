package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/url"
	"strings"
	"unicode/utf8"
)

type LibraryMaintenanceApplication interface {
	PreviewReorganization(context.Context, *ports.Actor, ports.LibraryReorganizationRequest) (*ports.LibraryReorganizationPreview, error)
	PreviewRecycle(context.Context, *ports.Actor, ports.LibraryRecyclePreviewRequest) (*ports.LibraryRecycleItem, error)
	Command(context.Context, *ports.Actor, ports.LibraryMaintenanceCommand, string) (*ports.LibraryMaintenanceResult, error)
	ReadCommand(context.Context, *ports.Actor, string) (*ports.LibraryMaintenanceResult, error)
	ListRecycle(context.Context, *ports.Actor, ports.LibraryRecycleQuery) (*ports.LibraryRecycleList, error)
}

// RegisterLibraryMaintenanceRoutes requires the existing Cookie AuthMiddleware.
func RegisterLibraryMaintenanceRoutes(r *http.ServeMux, service LibraryMaintenanceApplication) {
	h := libraryMaintenanceHTTP{service}
	r.HandleFunc("POST /api/v1/library-maintenance/reorganization-previews", h.reorganize)
	r.HandleFunc("POST /api/v1/library-maintenance/recycle-previews", h.recyclePreview)
	r.HandleFunc("POST /api/v1/library-maintenance/commands", h.command)
	r.HandleFunc("GET /api/v1/library-maintenance/commands/{key}", h.receipt)
	r.HandleFunc("GET /api/v1/library-maintenance/recycle", h.recycle)
}

type libraryMaintenanceHTTP struct{ service LibraryMaintenanceApplication }

func (h libraryMaintenanceHTTP) authorize(w http.ResponseWriter, r *http.Request) bool {
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
	case h.service == nil:
		e = ports.ErrUnavailable
	}
	if e != nil {
		h.respond(w, r, nil, e)
		return false
	}
	return true
}
func (h libraryMaintenanceHTTP) respond(w http.ResponseWriter, r *http.Request, out any, e error) {
	if errors.Is(e, ports.ErrLibraryMaintenanceChanged) {
		_ = mpBusinessResponse{status: http.StatusConflict, problem: true, body: problemResponse(http.StatusConflict, "LIBRARY_MAINTENANCE_CHANGED", "refresh current evidence; any existing permit is retained", GetRequestID(r.Context()))}.write(w)
		return
	}
	if e != nil {
		_ = mpBusinessError(r.Context(), e).write(w)
		return
	}
	if out == nil {
		_ = mpBusinessError(r.Context(), ports.ErrUnavailable).write(w)
		return
	}
	_ = mpBusinessResponse{status: http.StatusOK, body: out}.write(w)
}
func maintenanceHTTPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	return raw, e == nil
}
func (h libraryMaintenanceHTTP) reorganize(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	raw, ok := maintenanceHTTPBody(w, r)
	var in ports.LibraryReorganizationRequest
	if !ok {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	_, ok = selectionJSONObject(raw, []string{"file_id", "target_relative_path"}, "")
	if !ok || json.Unmarshal(raw, &in) != nil || in.FileID.String() == "00000000-0000-0000-0000-000000000000" || in.TargetRelativePath == "" {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	out, e := h.service.PreviewReorganization(r.Context(), GetActor(r.Context()), in)
	h.respond(w, r, out, e)
}
func (h libraryMaintenanceHTTP) recyclePreview(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	raw, ok := maintenanceHTTPBody(w, r)
	var in ports.LibraryRecyclePreviewRequest
	if !ok {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	_, ok = selectionJSONObject(raw, []string{"recycle_id", "action"}, "")
	if !ok || json.Unmarshal(raw, &in) != nil || (in.Action != "restore" && in.Action != "cleanup") || in.RecycleID == "" {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	out, e := h.service.PreviewRecycle(r.Context(), GetActor(r.Context()), in)
	h.respond(w, r, out, e)
}
func (h libraryMaintenanceHTTP) command(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	raw, ok := maintenanceHTTPBody(w, r)
	var in ports.LibraryMaintenanceCommand
	if !ok || json.Unmarshal(raw, &in) != nil {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	fields := []string{"action", "expected_version", "confirmed"}
	switch in.Action {
	case "reorganize":
		fields = append(fields, "file_id", "target_relative_path")
	case "restore", "cleanup":
		fields = append(fields, "recycle_id")
	default:
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	_, ok = selectionJSONObject(raw, fields, "")
	key := r.Header.Get("Idempotency-Key")
	if !ok || !in.Confirmed || !validUpgradeDigest(in.ExpectedVersion) || len(r.Header.Values("Idempotency-Key")) != 1 || !tvEpisodeUpgradeKey(key) {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	out, e := h.service.Command(r.Context(), GetActor(r.Context()), in, key)
	h.respond(w, r, out, e)
}
func (h libraryMaintenanceHTTP) receipt(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	key := r.PathValue("key")
	if !tvEpisodeUpgradeKey(key) {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	out, e := h.service.ReadCommand(r.Context(), GetActor(r.Context()), key)
	h.respond(w, r, out, e)
}
func (h libraryMaintenanceHTTP) recycle(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	for k, v := range q {
		if (k != "cursor" && k != "limit" && k != "kind" && k != "q") || len(v) != 1 {
			h.respond(w, r, nil, ports.ErrRejected)
			return
		}
	}
	limit := 20
	if q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 100 {
			h.respond(w, r, nil, ports.ErrRejected)
			return
		}
		limit = n
	}
	query := strings.TrimSpace(q.Get("q"))
	kind := q.Get("kind")
	if (kind != "" && kind != "movie" && kind != "tv") || !utf8.ValidString(query) || strings.ContainsRune(query, 0) || utf8.RuneCountInString(query) > 128 || len(q.Get("cursor")) > 2048 {
		h.respond(w, r, nil, ports.ErrRejected)
		return
	}
	out, e := h.service.ListRecycle(r.Context(), GetActor(r.Context()), ports.LibraryRecycleQuery{Cursor: q.Get("cursor"), Limit: limit, Kind: kind, Query: query})
	h.respond(w, r, out, e)
}
