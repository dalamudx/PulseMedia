package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatadiagnostics"
	"io"
	"mime"
	"net/http"
)

type metadataDiagnosticsApplication interface {
	Run(context.Context, *ports.Actor, ports.MetadataDiagnosticTarget) (ports.MetadataDiagnosticResult, error)
}

func (s *ServerHandler) SetMetadataDiagnosticsService(v metadataDiagnosticsApplication) {
	s.metadataDiagnosticsSvc = v
}
func (r mpBusinessResponse) VisitRunMetadataDiagnosticResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) RunMetadataDiagnostic(ctx context.Context, req RunMetadataDiagnosticRequestObject) (RunMetadataDiagnosticResponseObject, error) {
	actor := GetActor(ctx)
	if err := metadatadiagnostics.Admin(actor); err != nil {
		return mpBusinessError(ctx, err), nil
	}
	if req.Body == nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	if s.metadataDiagnosticsSvc == nil || s.authSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	current, err := s.authSvc.AuthenticateToken(ctx, GetRawToken(ctx))
	if err != nil || metadatadiagnostics.Admin(current) != nil || current.ID != actor.ID {
		return mpBusinessError(ctx, ports.ErrForbidden), nil
	}
	result, err := s.metadataDiagnosticsSvc.Run(ctx, current, ports.MetadataDiagnosticTarget(req.Body.Target))
	if err != nil {
		return mpBusinessError(ctx, err), nil
	}
	current, err = s.authSvc.AuthenticateToken(ctx, GetRawToken(ctx))
	if err != nil || metadatadiagnostics.Admin(current) != nil || current.ID != actor.ID {
		return mpBusinessError(ctx, ports.ErrForbidden), nil
	}
	return mpBusinessResponse{status: 200, body: result}, nil
}

const metadataDiagnosticPath = "/api/v1/settings/metadata-diagnostics"

func MetadataDiagnosticNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == metadataDiagnosticPath {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// StrictMetadataDiagnosticJSON admits only explicit, current administrator Cookie POSTs.
func StrictMetadataDiagnosticJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != metadataDiagnosticPath {
			next.ServeHTTP(w, r)
			return
		}
		fail := func(status int, code string) {
			WriteJSONError(w, status, code, "metadata diagnostic request rejected", GetRequestID(r.Context()))
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			fail(405, "METHOD_NOT_ALLOWED")
			return
		}
		if err := metadatadiagnostics.Admin(GetActor(r.Context())); err != nil {
			if GetActor(r.Context()) == nil {
				fail(401, "UNAUTHORIZED")
			} else {
				fail(403, "FORBIDDEN")
			}
			return
		}
		cookies := 0
		session := ""
		for _, cookie := range r.Cookies() {
			if cookie.Name == "pulsemedia_session" {
				cookies++
				session = cookie.Value
			}
		}
		if cookies != 1 || session == "" || len(r.Header.Values("Authorization")) != 0 || session != GetRawToken(r.Context()) {
			fail(403, "COOKIE_REQUIRED")
			return
		}
		if !sameRequestOrigin(r) {
			fail(403, "CSRF_REJECTED")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(400, "INVALID_REQUEST")
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
			fail(400, "INVALID_REQUEST")
			return
		}
		if r.Body == nil {
			fail(400, "INVALID_REQUEST")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1025))
		if err != nil || len(raw) > 1024 {
			fail(400, "INVALID_REQUEST")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			fail(400, "INVALID_REQUEST")
			return
		}
		if !decoder.More() {
			fail(400, "INVALID_REQUEST")
			return
		}
		token, err = decoder.Token()
		if err != nil || token != "target" {
			fail(400, "INVALID_REQUEST")
			return
		}
		var target ports.MetadataDiagnosticTarget
		if decoder.Decode(&target) != nil || !target.Valid() || decoder.More() {
			fail(400, "INVALID_REQUEST")
			return
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			fail(400, "INVALID_REQUEST")
			return
		}
		if _, err = decoder.Token(); err != io.EOF {
			fail(400, "INVALID_REQUEST")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		next.ServeHTTP(w, r)
	})
}
