package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetCompleteTVAcquisitionService(service *acquisition.CompleteTVService) {
	s.completeTVAcquisitionSvc = service
}

type completeTVAcquisitionResponse struct{ episodeAcquisitionResponse }

func (r completeTVAcquisitionResponse) VisitAcquireCompleteTVCoverageResponse(w http.ResponseWriter) error {
	return r.VisitAcquireReviewedEpisodeResponse(w)
}

func (s *ServerHandler) AcquireCompleteTVCoverage(ctx context.Context, r AcquireCompleteTVCoverageRequestObject) (AcquireCompleteTVCoverageResponseObject, error) {
	fail := func(e error) (AcquireCompleteTVCoverageResponseObject, error) {
		if errors.Is(e, ports.ErrCompleteTVCoverageAcquisitionConflict) || errors.Is(e, ports.ErrAcquisitionUnitAlreadyReserved) {
			e = ports.ErrEpisodeSourceConflict
		}
		return completeTVAcquisitionResponse{episodeAcquisitionResponse{episodeSourceFailure(ctx, e)}}, nil
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized)
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return fail(ports.ErrForbidden)
	}
	if s.completeTVAcquisitionSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidEpisodeMapping)
	}
	command := ports.AcquireCompleteTVCoverageCommand{SubscriptionID: r.Id, ReviewID: r.Body.ReviewId, ExpectedInputStamp: r.Body.ExpectedInputStamp, ExpectedMaterialStamp: r.Body.ExpectedMaterialStamp, Confirmed: bool(r.Body.Confirmed)}
	out, err := s.completeTVAcquisitionSvc.Acquire(ctx, a, command, r.Params.IdempotencyKey)
	if err != nil {
		return fail(err)
	}
	return completeTVAcquisitionResponse{episodeAcquisitionResponse{episodeSourceResponse{body: out}}}, nil
}

// Validate the original wire representation before the generated decoder can discard
// duplicate or unknown authority. Only these four exact, case-sensitive names exist.
func strictCompleteTVAcquisitionJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/complete-tv-acquisitions") {
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
		if err != nil || contentType != "application/json" || key == "" || len(key) > 130 || strings.TrimSpace(key) != key || strings.Contains(key, ",") || r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		token, err := dec.Token()
		if err != nil || token != json.Delim('{') {
			fail()
			return
		}
		fields := map[string]json.RawMessage{}
		for dec.More() {
			token, err = dec.Token()
			name, ok := token.(string)
			if err != nil || !ok {
				fail()
				return
			}
			switch name {
			case "review_id", "expected_input_stamp", "expected_material_stamp", "confirmed":
			default:
				fail()
				return
			}
			if _, exists := fields[name]; exists {
				fail()
				return
			}
			var value json.RawMessage
			if dec.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				fail()
				return
			}
			fields[name] = value
		}
		if token, err = dec.Token(); err != nil || token != json.Delim('}') || dec.Decode(new(any)) != io.EOF || len(fields) != 4 {
			fail()
			return
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			fail()
			return
		}
		var body AcquireCompleteTVCoverageJSONRequestBody
		if json.Unmarshal(raw, &body) != nil || body.ReviewId == uuid.Nil || !bool(body.Confirmed) || !completeTVHTTPStamp(body.ExpectedInputStamp) || !completeTVHTTPStamp(body.ExpectedMaterialStamp) {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
func completeTVHTTPStamp(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
