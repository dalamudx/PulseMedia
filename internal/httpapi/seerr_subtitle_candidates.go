package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	seerrusecase "github.com/dalamudx/PulseMedia/internal/usecase/seerr"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

type seerrApplication interface {
	Configuration(context.Context, *ports.Actor) (ports.SeerrConfiguration, error)
	Save(context.Context, *ports.Actor, ports.SeerrConfigurationInput) (ports.SeerrConfiguration, error)
	Receipts(context.Context, *ports.Actor) ([]ports.SeerrReceipt, error)
	Receive(context.Context, string, []byte) (ports.SeerrReceipt, error)
}
type subtitleCandidatesApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (ports.SubtitleCandidateState, error)
	Query(context.Context, *ports.Actor, uuid.UUID, int64, string) (ports.SubtitleCandidateState, error)
	Confirm(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int64) (ports.SubtitleCandidateState, error)
}

func (s *ServerHandler) SetSeerrService(v seerrApplication) { s.seerrSvc = v }
func (s *ServerHandler) SetSubtitleCandidatesService(v subtitleCandidatesApplication) {
	s.subtitleCandidatesSvc = v
}

type mpBusinessResponse struct {
	status  int
	body    any
	problem bool
}

func (r mpBusinessResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.problem {
		w.Header().Set("Content-Type", "application/problem+json")
	}
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
func mpBusinessError(ctx context.Context, e error) mpBusinessResponse {
	status, code, detail := 503, "INTEGRATION_UNAVAILABLE", "result could not be verified"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code, detail = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(e, ports.ErrForbidden):
		status, code, detail = 403, "FORBIDDEN", "current account cannot perform this action"
	case errors.Is(e, ports.ErrNotFound):
		status, code, detail = 404, "NOT_FOUND", "selected object not found"
	case errors.Is(e, ports.ErrRejected):
		status, code, detail = 400, "INVALID_REQUEST", "request is invalid"
	case errors.Is(e, ports.ErrEndpointVersionConflict), errors.Is(e, ports.ErrMediaSidecarStale), errors.Is(e, ports.ErrFileIdentity), errors.Is(e, ports.ErrMediaRequestConflict):
		status, code, detail = 409, "SOURCE_CHANGED", "source or configuration changed; read the current state"
	case errors.Is(e, ports.ErrFileConflict):
		status, code, detail = 409, "FILE_CONFLICT", "existing file is protected"
	case errors.Is(e, ports.ErrMediaRequestLimit):
		status, code, detail = 429, "REQUEST_LIMIT", "request limit reached"
	}
	return mpBusinessResponse{status: status, problem: true, body: problemResponse(status, code, detail, GetRequestID(ctx))}
}
func (r mpBusinessResponse) VisitGetSeerrConfigurationResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitSaveSeerrConfigurationResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitListSeerrReceiptsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitReceiveSeerrWebhookResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitGetSubtitleCandidatesResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitQuerySubtitleCandidatesResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r mpBusinessResponse) VisitConfirmSubtitleCandidateResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) GetSeerrConfiguration(ctx context.Context, _ GetSeerrConfigurationRequestObject) (GetSeerrConfigurationResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.seerrSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	v, e := s.seerrSvc.Configuration(ctx, GetActor(ctx))
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: v}, nil
}
func (s *ServerHandler) SaveSeerrConfiguration(ctx context.Context, req SaveSeerrConfigurationRequestObject) (SaveSeerrConfigurationResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.seerrSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	if req.Body == nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	raw, e := json.Marshal(req.Body)
	if e != nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	var in ports.SeerrConfigurationInput
	if json.Unmarshal(raw, &in) != nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	v, e := s.seerrSvc.Save(ctx, GetActor(ctx), in)
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: v}, nil
}
func (s *ServerHandler) ListSeerrReceipts(ctx context.Context, _ ListSeerrReceiptsRequestObject) (ListSeerrReceiptsResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.seerrSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	v, e := s.seerrSvc.Receipts(ctx, GetActor(ctx))
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: struct {
		Items []ports.SeerrReceipt `json:"items"`
	}{v}}, nil
}

// The generated route cannot confer ingress authority via Cookie or generic Token.
func (s *ServerHandler) ReceiveSeerrWebhook(ctx context.Context, _ ReceiveSeerrWebhookRequestObject) (ReceiveSeerrWebhookResponseObject, error) {
	return mpBusinessError(ctx, ports.ErrUnauthorized), nil
}
func (s *ServerHandler) GetSubtitleCandidates(ctx context.Context, req GetSubtitleCandidatesRequestObject) (GetSubtitleCandidatesResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.subtitleCandidatesSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	v, e := s.subtitleCandidatesSvc.Read(ctx, GetActor(ctx), req.FileId)
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: v}, nil
}
func (s *ServerHandler) QuerySubtitleCandidates(ctx context.Context, req QuerySubtitleCandidatesRequestObject) (QuerySubtitleCandidatesResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.subtitleCandidatesSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	if req.Body == nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	v, e := s.subtitleCandidatesSvc.Query(ctx, GetActor(ctx), req.FileId, req.Body.ExpectedMappingRevision, req.Params.IdempotencyKey)
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: v}, nil
}
func (s *ServerHandler) ConfirmSubtitleCandidate(ctx context.Context, req ConfirmSubtitleCandidateRequestObject) (ConfirmSubtitleCandidateResponseObject, error) {
	if e := mediaRequestActorError(GetActor(ctx), true); e != nil {
		return mpBusinessError(ctx, e), nil
	}
	if s.subtitleCandidatesSvc == nil {
		return mpBusinessError(ctx, ports.ErrUnavailable), nil
	}
	if req.Body == nil {
		return mpBusinessError(ctx, ports.ErrRejected), nil
	}
	v, e := s.subtitleCandidatesSvc.Confirm(ctx, GetActor(ctx), req.FileId, req.CandidateId, req.Body.ExpectedMappingRevision)
	if e != nil {
		return mpBusinessError(ctx, e), nil
	}
	return mpBusinessResponse{status: 200, body: v}, nil
}

// SeerrIngress is assembled outside generic authentication/CSRF. It intercepts
// exactly one method/path and keeps its own credential, capacity and body bounds.
func (s *ServerHandler) SeerrIngress(next http.Handler) http.Handler {
	var mu sync.Mutex
	var start time.Time
	count := 0
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/settings/seerr" || r.URL.Path == "/api/v1/seerr/receipts" || r.URL.Path == "/api/v1/integrations/seerr" || (strings.HasPrefix(r.URL.Path, "/api/v1/library/files/") && strings.Contains(r.URL.Path, "/subtitle-candidates")) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		if r.URL.Path != "/api/v1/integrations/seerr" || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		fail := func(e error) { _ = mpBusinessError(r.Context(), e).write(w) }
		if s.seerrSvc == nil {
			fail(ports.ErrUnavailable)
			return
		}
		if len(r.Header.Values("Authorization")) != 1 || !seerrusecase.ValidCredential(r.Header.Get("Authorization")) {
			fail(ports.ErrUnauthorized)
			return
		}
		now := s.app.Clock().Now()
		mu.Lock()
		if start.IsZero() || now.Sub(start) >= time.Minute {
			start = now
			count = 0
		}
		allowed := count < 60
		if allowed {
			count++
		}
		mu.Unlock()
		if !allowed {
			_ = mpBusinessResponse{status: 429, problem: true, body: problemResponse(429, "RATE_LIMITED", "webhook rate limit reached", GetRequestID(r.Context()))}.write(w)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			_ = mpBusinessResponse{status: 429, problem: true, body: problemResponse(429, "RATE_LIMITED", "webhook capacity reached", GetRequestID(r.Context()))}.write(w)
			return
		}
		content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || content != "application/json" {
			fail(ports.ErrRejected)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			fail(ports.ErrRejected)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		v, e := s.seerrSvc.Receive(ctx, r.Header.Get("Authorization"), raw)
		if e != nil {
			fail(e)
			return
		}
		_ = mpBusinessResponse{status: 202, body: v}.write(w)
	})
}

// StrictMPBusinessJSON belongs inside the normal authenticated chain, before
// generated binding. It rejects duplicate/case-alias/unknown command fields.
func StrictMPBusinessJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		config := r.Method == http.MethodPut && r.URL.Path == "/api/v1/settings/seerr"
		candidate := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/library/files/") && strings.Contains(r.URL.Path, "/subtitle-candidates/")
		if !config && !candidate {
			next.ServeHTTP(w, r)
			return
		}
		fail := func(e error) { _ = mpBusinessError(r.Context(), e).write(w) }
		if e := mediaRequestActorError(GetActor(r.Context()), true); e != nil {
			fail(e)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		raw, e := io.ReadAll(r.Body)
		if e != nil || seerrusecase.CheckJSON(raw) != nil {
			fail(ports.ErrRejected)
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			fail(ports.ErrRejected)
			return
		}
		allowed := map[string]bool{"expected_mapping_revision": true}
		required := []string{"expected_mapping_revision"}
		if config {
			allowed = map[string]bool{"enabled": true, "expected_version": true, "owner_id": true, "credential": true}
			required = []string{"enabled", "expected_version", "owner_id"}
		}
		for k, v := range fields {
			if !allowed[k] || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				fail(ports.ErrRejected)
				return
			}
		}
		for _, k := range required {
			if _, ok := fields[k]; !ok {
				fail(ports.ErrRejected)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/queries") && len(r.Header.Values("Idempotency-Key")) != 1 {
			fail(ports.ErrRejected)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		next.ServeHTTP(w, r)
	})
}
