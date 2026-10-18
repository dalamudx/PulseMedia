package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"io"
	"mime"
	"net/http"
	"strings"
)

func (s *ServerHandler) SetUpgradePreviewService(v *acquisition.UpgradePreviewService) {
	s.upgradePreviewSvc = v
}

type upgradePreviewResponse struct{ episodeSourceResponse }

func (r upgradePreviewResponse) VisitPreviewSubscriptionUpgradeResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) PreviewSubscriptionUpgrade(ctx context.Context, r PreviewSubscriptionUpgradeRequestObject) (PreviewSubscriptionUpgradeResponseObject, error) {
	fail := func(e error) (PreviewSubscriptionUpgradeResponseObject, error) {
		return upgradePreviewResponse{episodeSourceFailure(ctx, e)}, nil
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized)
	}
	if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return fail(ports.ErrForbidden)
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidEpisodeMapping)
	}
	if s.upgradePreviewSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	req := ports.UpgradePreviewRequest{SubscriptionID: r.Id, CurrentFileID: r.Body.CurrentFileId, CandidateID: r.Body.CandidateId}
	if r.Body.Expected != nil {
		raw, _ := json.Marshal(r.Body.Expected)
		var v ports.UpgradePreviewVersion
		if json.Unmarshal(raw, &v) != nil {
			return fail(ports.ErrInvalidEpisodeMapping)
		}
		req.Expected = &v
	}
	out, e := s.upgradePreviewSvc.Preview(ctx, a, req)
	if e != nil {
		return fail(e)
	}
	return upgradePreviewResponse{episodeSourceResponse{body: out}}, nil
}
func strictUpgradePreviewJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/upgrade-previews") {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() { WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context())) }
		for _, v := range r.Header {
			if len(v) != 1 {
				fail()
				return
			}
		}
		content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || content != "application/json" || r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
		if e != nil {
			fail()
			return
		}
		fields, ok := selectionJSONObject(raw, []string{"current_file_id", "candidate_id"}, "expected")
		if !ok {
			fail()
			return
		}
		if expected, exists := fields["expected"]; exists {
			if _, ok = selectionJSONObject(expected, []string{"actor_id", "subscription_id", "media_id", "unit_id", "root_id", "intent_version", "policy_id", "policy_revision", "input_stamp"}, ""); !ok {
				fail()
				return
			}
			var v ports.UpgradePreviewVersion
			if json.Unmarshal(expected, &v) != nil || v.IntentVersion < 1 || v.PolicyRevision < 1 || strings.TrimSpace(v.PolicyID) == "" || len(v.PolicyID) > 240 || !completeTVHTTPStamp(v.InputStamp) {
				fail()
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
