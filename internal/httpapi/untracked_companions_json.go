package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"mime"
	"net/http"
	"strings"
)

const untrackedCompanionPrefix = "/api/v1/library/untracked-organizations/"

func UntrackedCompanionNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, untrackedCompanionPrefix) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func StrictUntrackedCompanionJSON(next http.Handler) http.Handler {
	return UntrackedCompanionNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, untrackedCompanionPrefix), "/")
		if !strings.HasPrefix(r.URL.Path, untrackedCompanionPrefix) || len(parts) < 2 || parts[1] != "companions" {
			next.ServeHTTP(w, r)
			return
		}
		if err := untrackedScopeActorError(GetActor(r.Context())); err != nil {
			p := untrackedCompanionProblem(r.Context(), err)
			_ = p.write(w)
			return
		}
		if !validUntrackedCompanionTransport(w, r) {
			WriteJSONError(w, 400, "INVALID_REQUEST", "invalid companion request", GetRequestID(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func strictUntrackedCompanionJSON(next http.Handler) http.Handler {
	return StrictUntrackedCompanionJSON(next)
}
func untrackedCompanionNoStore(next http.Handler) http.Handler {
	return UntrackedCompanionNoStore(next)
}

func validUntrackedCompanionTransport(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, untrackedCompanionPrefix), "/")
	if len(parts) < 2 || parts[1] != "companions" || !canonicalUntrackedReviewID(parts[0]) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	selection := r.Method == http.MethodPost && len(parts) == 2
	if !selection {
		valid := r.Method == http.MethodGet && len(parts) == 2 || r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "discover" || r.Method == http.MethodPost && len(parts) == 4 && canonicalUntrackedReviewID(parts[2]) && parts[3] == "execute"
		if !valid || len(r.Header.Values("Idempotency-Key")) != 0 {
			return false
		}
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
			body, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(body) != 0 {
				return false
			}
		}
		return true
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
		return false
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		return false
	}
	kind, _, err := mime.ParseMediaType(types[0])
	if err != nil || kind != "application/json" {
		return false
	}
	original := r.Body
	defer func() { _ = original.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !uniqueEndpointJSONValue(decoder, 0, map[string]bool{"candidate_keys": true, "action": true}) {
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return false
	}
	var input ports.UntrackedCompanionSelection
	if json.Unmarshal(raw, &input) != nil || len(input.CandidateKeys) < 1 || len(input.CandidateKeys) > 32 || (input.Action != "copy" && input.Action != "hardlink") {
		return false
	}
	seen := map[string]bool{}
	for _, key := range input.CandidateKeys {
		if !validUntrackedReviewHash(key) || seen[key] {
			return false
		}
		seen[key] = true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	return true
}
