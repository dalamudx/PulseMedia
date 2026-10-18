package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"strings"
)

type completeTVOrganizationResponse struct{ transferRecoveryResponse }

func (r completeTVOrganizationResponse) VisitConfirmCompleteTVOrganizationResponse(w http.ResponseWriter) error {
	return r.VisitCommitOriginalTransferRecoveryResponse(w)
}

func (s *ServerHandler) ConfirmCompleteTVOrganization(ctx context.Context, r ConfirmCompleteTVOrganizationRequestObject) (ConfirmCompleteTVOrganizationResponseObject, error) {
	fail := func(err error) (ConfirmCompleteTVOrganizationResponseObject, error) {
		return completeTVOrganizationResponse{recoveryFailure(ctx, err)}, nil
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized)
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return fail(ports.ErrForbidden)
	}
	if s.transferSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidEpisodeMapping)
	}
	out, err := s.transferSvc.ConfirmCompleteTVOrganization(ctx, a, ports.CompleteTVOrganizationReferences{TaskID: r.Id, OperationID: r.Body.OperationId, TrackingJobID: r.Body.TrackingJobId}, r.Params.IdempotencyKey)
	if err != nil {
		return fail(err)
	}
	return completeTVOrganizationResponse{transferRecoveryResponse{episodeSourceResponse: episodeSourceResponse{body: out}, accepted: true}}, nil
}

// Check the original wire bytes before the generated decoder discards duplicates
// or unknown fields. Only two scalar identifiers are accepted, including nested input.
func strictCompleteTVOrganizationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/complete-tv-organizations") {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() { WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context())) }
		for _, values := range r.Header {
			if len(values) != 1 {
				fail()
				return
			}
		}
		key := r.Header.Get("Idempotency-Key")
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || key == "" || len(key) > 120 || strings.TrimSpace(key) != key || strings.Contains(key, ",") || r.Body == nil {
			fail()
			return
		}
		originalBody := r.Body
		defer func() { _ = originalBody.Close() }()
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		token, err := dec.Token()
		if err != nil || token != json.Delim('{') {
			fail()
			return
		}
		fields := map[string]string{}
		for dec.More() {
			token, err = dec.Token()
			name, ok := token.(string)
			if err != nil || !ok || (name != "operation_id" && name != "tracking_job_id") {
				fail()
				return
			}
			if _, exists := fields[name]; exists {
				fail()
				return
			}
			var value string
			if dec.Decode(&value) != nil {
				fail()
				return
			}
			id, err := uuid.Parse(value)
			if err != nil || id == uuid.Nil || id.String() != value {
				fail()
				return
			}
			fields[name] = value
		}
		if token, err = dec.Token(); err != nil || token != json.Delim('}') || dec.Decode(new(any)) != io.EOF || len(fields) != 2 {
			fail()
			return
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
