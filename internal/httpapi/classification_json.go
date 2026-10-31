package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
)

// StrictClassificationJSON must run after authentication/CSRF and before generated decoding.
func StrictClassificationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publish := r.Method == http.MethodPut && r.URL.Path == "/api/v1/classification"
		preview := r.Method == http.MethodPost && r.URL.Path == "/api/v1/classification/previews"
		if !publish && !preview {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		fail := func(status int, code, message string) {
			WriteJSONError(w, status, code, message, GetRequestID(r.Context()))
		}
		actor := GetActor(r.Context())
		if actor == nil {
			fail(401, "UNAUTHORIZED", "authentication required")
			return
		}
		if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || publish && !actor.IsAdmin() {
			fail(403, "FORBIDDEN", "classification request is not permitted")
			return
		}
		if publish {
			keys := r.Header.Values("Idempotency-Key")
			if len(keys) != 1 || !classificationRequestKey(keys[0]) {
				fail(400, "INVALID_REQUEST", "invalid classification request")
				return
			}
		}
		fields := map[string]bool{"version": true, "media_id": false, "media_type": false, "tmdb_id": false}
		var body any = &PreviewClassificationJSONRequestBody{}
		if publish {
			fields = map[string]bool{"expected_version": true, "rules": true}
			body = &PublishClassificationJSONRequestBody{}
		}
		invalid := func() { fail(400, "INVALID_REQUEST", "invalid classification request") }
		if r.Body == nil {
			invalid()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, e := io.ReadAll(http.MaxBytesReader(w, original, 128<<10))
		if e != nil {
			invalid()
			return
		}
		tokens := json.NewDecoder(bytes.NewReader(raw))
		tokens.UseNumber()
		if !uniqueEndpointJSONValue(tokens, 0, fields) {
			invalid()
			return
		}
		if _, e = tokens.Token(); e != io.EOF {
			invalid()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil || publish && !classificationRuleJSONShape(raw) {
			invalid()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
func classificationRuleJSONShape(raw []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	var rules []map[string]json.RawMessage
	if json.Unmarshal(root["rules"], &rules) != nil || rules == nil || len(rules) > 64 {
		return false
	}
	for _, rule := range rules {
		if len(rule) != 6 {
			return false
		}
		for _, key := range []string{"id", "media_type", "genres", "languages", "countries", "root_id"} {
			if _, ok := rule[key]; !ok {
				return false
			}
		}
	}
	return true
}
func classificationRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 160 {
		return false
	}
	for _, c := range key {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' && c != ':' {
			return false
		}
	}
	return true
}
