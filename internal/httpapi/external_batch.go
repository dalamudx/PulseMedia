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
	"strconv"
)

type ExternalBatchApplication interface {
	Create(context.Context, *ports.Actor, uuid.UUID, ports.ExternalBatchInput, string) (*ports.ExternalBatchView, error)
	Read(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.ExternalBatchView, error)
	Confirm(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int) (*ports.UntrackedConfirmationReceipt, error)
	Organize(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int) (*ports.UntrackedOrganizationReceipt, error)
}

// Register on the authenticated router; writes also require the existing CSRF middleware.
func RegisterExternalBatchRoutes(mux *http.ServeMux, svc ExternalBatchApplication) {
	h := externalBatchHTTP{svc}
	const base = "/api/v1/library/untracked-scopes/{scope_id}"
	mux.HandleFunc("POST "+base+"/external-batches", h.create)
	mux.HandleFunc("GET "+base+"/external-batch-commands/{key}", h.read)
	mux.HandleFunc("GET "+base+"/external-batches/{batch_id}", h.read)
	mux.HandleFunc("POST "+base+"/external-batches/{batch_id}/rows/{row}/confirm", h.confirm)
	mux.HandleFunc("POST "+base+"/external-batches/{batch_id}/rows/{row}/organize", h.organize)
}

type externalBatchHTTP struct{ svc ExternalBatchApplication }

func (h externalBatchHTTP) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ports.ErrUntrackedReviewStale) || errors.Is(err, ports.ErrUntrackedOrganizationStale) {
		err = ports.ErrEndpointVersionConflict
	}
	_ = mpBusinessError(r.Context(), err).write(w)
}
func (h externalBatchHTTP) guard(w http.ResponseWriter, r *http.Request) (*ports.Actor, uuid.UUID, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	a := GetActor(r.Context())
	var err error
	if a == nil {
		err = ports.ErrUnauthorized
	} else if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil || len(r.Header.Values("Authorization")) != 0 || r.Method != "GET" && !sameRequestOrigin(r) {
		err = ports.ErrForbidden
	}
	scope, valid := tvEpisodeUpgradeUUID(r.PathValue("scope_id"))
	if err == nil && (!valid || r.URL.RawQuery != "" || r.URL.ForceQuery) {
		err = ports.ErrRejected
	}
	if err == nil && h.svc == nil {
		err = ports.ErrUnavailable
	}
	if err != nil {
		h.fail(w, r, err)
		return nil, uuid.Nil, false
	}
	return a, scope, true
}
func externalBatchBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	return raw, err == nil
}
func externalBatchDecode(raw []byte) (ports.ExternalBatchInput, bool) {
	var out ports.ExternalBatchInput
	root, ok := selectionJSONObject(raw, []string{"rows"}, "")
	if !ok {
		return out, false
	}
	var rows []json.RawMessage
	if json.Unmarshal(root["rows"], &rows) != nil || len(rows) < 1 || len(rows) > ports.ExternalBatchLimit {
		return out, false
	}
	for _, rawRow := range rows {
		row, ok := selectionJSONObject(rawRow, []string{"file_id", "confirmation"}, "")
		if !ok {
			return out, false
		}
		if _, ok = selectionJSONObject(row["confirmation"], []string{"review_id", "material_hash", "scope_version", "file_revision", "candidate_key", "action", "target_relative_path"}, ""); !ok {
			return out, false
		}
		var value ports.ExternalBatchSelection
		if json.Unmarshal(rawRow, &value) != nil {
			return out, false
		}
		out.Rows = append(out.Rows, value)
	}
	return out, true
}
func (h externalBatchHTTP) create(w http.ResponseWriter, r *http.Request) {
	a, scope, ok := h.guard(w, r)
	if !ok {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	raw, valid := externalBatchBody(w, r)
	in, decoded := externalBatchDecode(raw)
	if len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || !valid || !decoded {
		h.fail(w, r, ports.ErrRejected)
		return
	}
	out, err := h.svc.Create(r.Context(), a, scope, in, keys[0])
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_ = mpBusinessResponse{status: 200, body: externalBatchViewResponse(out)}.write(w)
}
func (h externalBatchHTTP) read(w http.ResponseWriter, r *http.Request) {
	a, scope, ok := h.guard(w, r)
	if !ok {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 0 {
		h.fail(w, r, ports.ErrRejected)
		return
	}
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(raw) != 0 {
			h.fail(w, r, ports.ErrRejected)
			return
		}
	}
	id := uuid.Nil
	key := r.PathValue("key")
	if key != "" {
		if !validDoubanSyncCommandKey(key) {
			h.fail(w, r, ports.ErrRejected)
			return
		}
	} else {
		var valid bool
		id, valid = tvEpisodeUpgradeUUID(r.PathValue("batch_id"))
		if !valid {
			h.fail(w, r, ports.ErrRejected)
			return
		}
	}
	out, err := h.svc.Read(r.Context(), a, scope, id, key)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_ = mpBusinessResponse{status: 200, body: externalBatchViewResponse(out)}.write(w)
}
func (h externalBatchHTTP) row(w http.ResponseWriter, r *http.Request) (*ports.Actor, uuid.UUID, uuid.UUID, int, bool) {
	a, scope, ok := h.guard(w, r)
	if !ok {
		return nil, scope, uuid.Nil, 0, false
	}
	id, valid := tvEpisodeUpgradeUUID(r.PathValue("batch_id"))
	index, e := strconv.Atoi(r.PathValue("row"))
	raw, body := externalBatchBody(w, r)
	fields, decoded := selectionJSONObject(raw, []string{"confirmed"}, "")
	var confirmed bool
	if !valid || e != nil || strconv.Itoa(index) != r.PathValue("row") || index < 0 || index >= ports.ExternalBatchLimit || !body || !decoded || json.Unmarshal(fields["confirmed"], &confirmed) != nil || !confirmed || len(r.Header.Values("Idempotency-Key")) != 0 {
		h.fail(w, r, ports.ErrRejected)
		return nil, scope, id, index, false
	}
	return a, scope, id, index, true
}
func (h externalBatchHTTP) confirm(w http.ResponseWriter, r *http.Request) {
	a, scope, id, index, ok := h.row(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Confirm(r.Context(), a, scope, id, index)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_ = mpBusinessResponse{status: 200, body: untrackedConfirmationReceiptResponse(*out)}.write(w)
}
func (h externalBatchHTTP) organize(w http.ResponseWriter, r *http.Request) {
	a, scope, id, index, ok := h.row(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Organize(r.Context(), a, scope, id, index)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_ = mpBusinessResponse{status: 202, body: untrackedOrganizationReceiptResponse(*out)}.write(w)
}
