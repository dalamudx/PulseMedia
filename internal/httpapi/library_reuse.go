package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func (s *ServerHandler) SetLibraryReuseService(v *transfer.LibraryReuseService) { s.reuseSvc = v }

type reuseResponse struct {
	status int
	body   any
}

func (v reuseResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(v.status)
	return json.NewEncoder(w).Encode(v.body)
}
func (v reuseResponse) VisitPreviewLibraryReuseResponse(w http.ResponseWriter) error {
	return v.write(w)
}
func (v reuseResponse) VisitConfirmLibraryReuseResponse(w http.ResponseWriter) error {
	return v.write(w)
}
func (v reuseResponse) VisitListLibraryReusesResponse(w http.ResponseWriter) error { return v.write(w) }
func reuseFailure(e error) reuseResponse {
	status, code, message := 503, "UNAVAILABLE", "本地复用暂不可用，请稍后重试"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "请先登录"
	case errors.Is(e, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "当前账号无权操作这项需求"
	case errors.Is(e, ports.ErrInvalidLibraryReuse):
		status, code, message = 400, "INVALID_REQUEST", "请检查目标、动作和完整覆盖确认"
	case errors.Is(e, ports.ErrLibraryReuseChanged):
		status, code, message = 409, "INPUTS_CHANGED", "材料已变化，请重新预览"
	}
	return reuseResponse{status, map[string]any{"error": map[string]string{"code": code, "message": message}}}
}
func (s *ServerHandler) PreviewLibraryReuse(ctx context.Context, r PreviewLibraryReuseRequestObject) (PreviewLibraryReuseResponseObject, error) {
	if s.reuseSvc == nil {
		return reuseFailure(ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return reuseFailure(ports.ErrInvalidLibraryReuse), nil
	}
	// Generated wire types are decoded into the narrow ports contract; no storage
	// or filesystem types cross the HTTP boundary.
	var request struct {
		Targets []ports.LibraryReuseTarget `json:"targets"`
	}
	raw, e := json.Marshal(r.Body)
	if e != nil {
		return reuseFailure(ports.ErrInvalidLibraryReuse), nil
	}
	if json.Unmarshal(raw, &request) != nil {
		return reuseFailure(ports.ErrInvalidLibraryReuse), nil
	}
	rows, e := s.reuseSvc.Preview(ctx, GetActor(ctx), request.Targets)
	if e != nil {
		return reuseFailure(e), nil
	}
	return reuseResponse{200, rows}, nil
}
func (s *ServerHandler) ConfirmLibraryReuse(ctx context.Context, r ConfirmLibraryReuseRequestObject) (ConfirmLibraryReuseResponseObject, error) {
	if s.reuseSvc == nil {
		return reuseFailure(ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return reuseFailure(ports.ErrInvalidLibraryReuse), nil
	}
	var command ports.LibraryReuseCommand
	raw, e := json.Marshal(r.Body)
	if e != nil || json.Unmarshal(raw, &command) != nil {
		return reuseFailure(ports.ErrInvalidLibraryReuse), nil
	}
	receipt, e := s.reuseSvc.Confirm(ctx, GetActor(ctx), command, r.Params.IdempotencyKey)
	if e != nil {
		return reuseFailure(e), nil
	}
	return reuseResponse{202, receipt}, nil
}
func (s *ServerHandler) ListLibraryReuses(ctx context.Context, _ ListLibraryReusesRequestObject) (ListLibraryReusesResponseObject, error) {
	if s.reuseSvc == nil {
		return reuseFailure(ports.ErrUnavailable), nil
	}
	rows, e := s.reuseSvc.List(ctx, GetActor(ctx))
	if e != nil {
		return reuseFailure(e), nil
	}
	return reuseResponse{200, rows}, nil
}
func strictLibraryReuseJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || (r.URL.Path != "/api/v1/library/reuses" && r.URL.Path != "/api/v1/library/reuse-previews") {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			w.Header().Set("Cache-Control", "private, no-store")
			WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		d.DisallowUnknownFields()
		var value any
		if r.URL.Path == "/api/v1/library/reuses" {
			var b ports.LibraryReuseCommand
			if d.Decode(&b) != nil || !b.Confirmed || len(b.InputStamp) != 64 || len(b.TargetRel) > 1024 || len(b.ConfirmedUnitIDs) < 1 || len(b.ConfirmedUnitIDs) > 20 || len(r.Header.Values("Idempotency-Key")) != 1 {
				fail()
				return
			}
			value = b
		} else {
			var b struct {
				Targets []ports.LibraryReuseTarget `json:"targets"`
			}
			if d.Decode(&b) != nil || len(b.Targets) == 0 || len(b.Targets) > 20 {
				fail()
				return
			}
			value = b
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			fail()
			return
		}
		raw, e := json.Marshal(value)
		if e != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
