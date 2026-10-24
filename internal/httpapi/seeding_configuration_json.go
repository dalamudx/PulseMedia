package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
)

const seedingConfigPrefix = "/api/v1/download-clients/"

func seedingConfigRoute(path string) bool {
	return strings.HasPrefix(path, seedingConfigPrefix) && strings.Contains(strings.TrimPrefix(path, seedingConfigPrefix), "/seeding/config")
}
func seedingConfigNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seedingConfigRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func strictSeedingConfigJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !seedingConfigRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := siteCheckinConfigActorError(GetActor(r.Context())); err != nil {
			p := seedingConfigProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid seeding configuration request", GetRequestID(r.Context()))
		}
		clientRaw, tail, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, seedingConfigPrefix), "/seeding/config")
		client, err := uuid.Parse(clientRaw)
		receipt := strings.HasPrefix(tail, "/commands/") && validCookieCloudCommandKey(strings.TrimPrefix(tail, "/commands/"))
		if !ok || err != nil || client == uuid.Nil || client.String() != clientRaw || (tail != "" && !receipt) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
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
				raw, e := io.ReadAll(io.LimitReader(r.Body, 1))
				if e != nil || len(raw) != 0 {
					fail()
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPut || tail != "" {
			next.ServeHTTP(w, r)
			return
		}
		if len(keys) != 1 || !validCookieCloudCommandKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/json" {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 32768))
		if err != nil {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if !uniqueEndpointJSONValue(decoder, 0, map[string]bool{"expected_version": true, "expected_client_updated_at": true, "enabled": true, "interval_seconds": true, "rule": true}) {
			fail()
			return
		}
		if _, err = decoder.Token(); err != io.EOF {
			fail()
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			fail()
			return
		}
		for _, value := range fields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				fail()
				return
			}
		}
		child := json.NewDecoder(bytes.NewReader(fields["rule"]))
		child.UseNumber()
		if !uniqueEndpointJSONValue(child, 0, map[string]bool{"mode": true, "delete_files": true, "min_seeding_seconds": false, "min_share_ratio": false, "low_free_bytes": false}) {
			fail()
			return
		}
		var ruleFields map[string]json.RawMessage
		if json.Unmarshal(fields["rule"], &ruleFields) != nil {
			fail()
			return
		}
		for _, value := range ruleFields {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				fail()
				return
			}
		}
		var input SeedingConfigInput
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.ExpectedVersion < 0 || input.ExpectedVersion == math.MaxInt64 || input.ExpectedClientUpdatedAt.IsZero() || input.IntervalSeconds < 60 || input.IntervalSeconds > 86400 || seedingRuleInput(input.Rule).Validate() != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
