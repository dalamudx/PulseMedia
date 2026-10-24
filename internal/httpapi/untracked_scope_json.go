package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const untrackedScopePrefix = "/api/v1/library/untracked-scopes"

func untrackedScopeRoute(path string) bool {
	return path == untrackedScopePrefix || strings.HasPrefix(path, untrackedScopePrefix+"/")
}

func untrackedScopeNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if untrackedScopeRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func strictUntrackedScopeJSON(next http.Handler) http.Handler {
	return untrackedScopeNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !untrackedScopeRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := untrackedScopeActorError(GetActor(r.Context())); err != nil {
			p := untrackedScopeProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid untracked scope request", GetRequestID(r.Context()))
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, untrackedScopePrefix+"/"), "/")
		if untrackedOrganizationPath(parts) {
			if !validUntrackedOrganizationTransport(w, r, parts) {
				fail()
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(parts) >= 3 && parts[1] == "discoveries" {
			if !validUntrackedReviewTransport(w, r, parts) {
				fail()
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(parts) == 2 && parts[1] == "discoveries" {
			if !validUntrackedDiscoveryTransport(r, parts[0]) {
				fail()
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(parts) != 1 && len(parts) != 3 {
			fail()
			return
		}
		id, err := uuid.Parse(parts[0])
		if err != nil || id == uuid.Nil || id.String() != parts[0] || r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail()
			return
		}
		if len(parts) == 3 && (parts[1] != "commands" || !validDoubanSyncCommandKey(parts[2])) {
			fail()
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if r.Method == http.MethodGet {
			if len(keys) != 0 {
				fail()
				return
			}
			if r.Body != nil {
				defer func() { _ = r.Body.Close() }()
				b, e := io.ReadAll(io.LimitReader(r.Body, 1))
				if e != nil || len(b) != 0 {
					fail()
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPut || len(parts) != 1 {
			next.ServeHTTP(w, r)
			return
		}
		if len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
		if err != nil {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		fields := map[string]bool{"enabled": true, "expected_version": true, "client_id": true, "expected_client_version": true, "remote_prefix": true, "target_root_id": true, "expected_root_generation": true}
		if !uniqueEndpointJSONValue(decoder, 0, fields) {
			fail()
			return
		}
		if _, err = decoder.Token(); err != io.EOF {
			fail()
			return
		}
		var in ports.UntrackedScopeInput
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || !validUntrackedScopeInput(in) {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	}))
}
