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

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetTVSelectionService(service *acquisition.TVSelectionService) {
	s.tvSelectionSvc = service
}

type tvSelectionResponse struct{ episodeSourceResponse }

func (r tvSelectionResponse) VisitPreviewTVSelectionResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *ServerHandler) PreviewTVSelection(ctx context.Context, r PreviewTVSelectionRequestObject) (PreviewTVSelectionResponseObject, error) {
	fail := func(e error) (PreviewTVSelectionResponseObject, error) {
		if errors.Is(e, quality.ErrInvalidTVSelection) {
			e = ports.ErrInvalidEpisodeMapping
		}
		if errors.Is(e, ports.ErrTVSelectionChanged) {
			e = ports.ErrEpisodeSourceConflict
		}
		return tvSelectionResponse{episodeSourceFailure(ctx, e)}, nil
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized)
	}
	if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return fail(ports.ErrForbidden)
	}
	if r.Body == nil {
		return fail(quality.ErrInvalidTVSelection)
	}
	if s.tvSelectionSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	b := r.Body
	request := ports.TVSelectionRequest{SubscriptionID: r.Id, Season: b.Season, PackPolicy: quality.PackPolicy{Mode: quality.PackMode(b.PackPolicy.Mode), MinMissingRatio: b.PackPolicy.MinMissingRatio}}
	for _, ref := range b.Packs {
		request.Packs = append(request.Packs, ports.TVSelectionCandidateRef{CandidateID: ref.CandidateId, ReviewID: ref.ReviewId})
	}
	for _, ref := range b.Episodes {
		request.Episodes = append(request.Episodes, ports.TVSelectionCandidateRef{CandidateID: ref.CandidateId, ReviewID: ref.ReviewId})
	}
	if b.Expected != nil {
		v := b.Expected
		actorID, err := uuid.Parse(a.ID)
		if err != nil {
			return fail(ports.ErrForbidden)
		}
		request.Expected = &ports.TVSelectionVersion{ActorID: actorID, SubscriptionID: r.Id, MediaID: v.MediaId, IntentVersion: v.IntentVersion, MediaVersion: v.MediaVersion, PolicyID: v.PolicyId, PolicyRevision: v.PolicyRevision, ScopeStamp: v.ScopeStamp, ConfigurationStamp: v.ConfigurationStamp, SelectionPolicyStamp: v.SelectionPolicyStamp}
	}
	out, err := s.tvSelectionSvc.Preview(ctx, a, request)
	if err != nil {
		return fail(err)
	}
	v := out.Version
	excluded := make([]map[string]any, 0, len(out.Excluded))
	for _, e := range out.Excluded {
		excluded = append(excluded, map[string]any{"candidate_id": e.CandidateID, "reason": e.Reason})
	}
	body := map[string]any{"status": out.Status, "execution_allowed": false,
		"version":  map[string]any{"media_id": v.MediaID, "intent_version": v.IntentVersion, "media_version": v.MediaVersion, "policy_id": v.PolicyID, "policy_revision": v.PolicyRevision, "scope_stamp": v.ScopeStamp, "configuration_stamp": v.ConfigurationStamp, "selection_policy_stamp": v.SelectionPolicyStamp},
		"decision": map[string]any{"kind": out.Decision.Kind, "candidate_ids": out.Decision.CandidateIDs, "reason": out.Decision.Reason, "score": out.Decision.Score}, "excluded": excluded}
	return tvSelectionResponse{episodeSourceResponse{body: body}}, nil
}

// Check the original representation before generated decoding can erase duplicate,
// case-aliased or unknown keys, including inside policy, references and fences.
func selectionJSONObject(raw []byte, required []string, optional string) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		allowed := optional != "" && name == optional
		for _, key := range required {
			allowed = allowed || key == name
		}
		if _, duplicate := fields[name]; duplicate || !allowed {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		fields[name] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, false
	}
	for _, key := range required {
		if _, found := fields[key]; !found {
			return nil, false
		}
	}
	return fields, true
}
func strictTVSelectionJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/tv-selection-preview") {
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
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
		if err != nil {
			fail()
			return
		}
		fields, ok := selectionJSONObject(raw, []string{"season", "pack_policy", "packs", "episodes"}, "expected")
		if !ok {
			fail()
			return
		}
		if _, ok = selectionJSONObject(fields["pack_policy"], []string{"mode", "min_missing_ratio"}, ""); !ok {
			fail()
			return
		}
		count := 0
		for _, name := range []string{"packs", "episodes"} {
			var refs []json.RawMessage
			if json.Unmarshal(fields[name], &refs) != nil || refs == nil {
				fail()
				return
			}
			count += len(refs)
			if count > 100 {
				fail()
				return
			}
			for _, ref := range refs {
				if _, ok = selectionJSONObject(ref, []string{"candidate_id", "review_id"}, ""); !ok {
					fail()
					return
				}
			}
		}
		if expected, exists := fields["expected"]; exists {
			if _, ok = selectionJSONObject(expected, []string{"media_id", "intent_version", "media_version", "policy_id", "policy_revision", "scope_stamp", "configuration_stamp", "selection_policy_stamp"}, ""); !ok {
				fail()
				return
			}
		}
		var b PreviewTVSelectionJSONRequestBody
		if json.Unmarshal(raw, &b) != nil {
			fail()
			return
		}
		if b.Expected != nil {
			v := b.Expected
			if v.MediaId == uuid.Nil || v.IntentVersion < 1 || v.MediaVersion < 1 || v.PolicyRevision < 1 || len(v.PolicyId) > 240 || strings.TrimSpace(v.PolicyId) == "" || !completeTVHTTPStamp(v.ScopeStamp) || !completeTVHTTPStamp(v.ConfigurationStamp) || !completeTVHTTPStamp(v.SelectionPolicyStamp) {
				fail()
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
