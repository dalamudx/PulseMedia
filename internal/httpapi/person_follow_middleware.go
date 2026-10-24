package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

func personFollowRoute(path string) bool {
	return path == "/api/v1/person-follows" || strings.HasPrefix(path, "/api/v1/person-follows/")
}
func personFollowNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if personFollowRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func personFollowJSONFields(raw []byte, required map[string]bool) map[string]json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !uniqueEndpointJSONValue(decoder, 0, required) {
		return nil
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}
func strictPersonFollowJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !personFollowRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := personFollowActorError(GetActor(r.Context())); err != nil {
			status, code := http.StatusForbidden, "FORBIDDEN"
			if GetActor(r.Context()) == nil {
				status, code = http.StatusUnauthorized, "UNAUTHORIZED"
			}
			WriteJSONError(w, status, code, "current account session required", GetRequestID(r.Context()))
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid person follow request", GetRequestID(r.Context()))
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			fail()
			return
		}
		if r.Method != http.MethodPut {
			next.ServeHTTP(w, r)
			return
		}
		keys := r.Header.Values("Idempotency-Key")
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
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 16<<10))
		if err != nil {
			fail()
			return
		}
		fields := personFollowJSONFields(raw, map[string]bool{"expected_version": true, "settings": true})
		if fields == nil {
			fail()
			return
		}
		settings := personFollowJSONFields(fields["settings"], map[string]bool{"person_id": true, "role": true, "enabled": true, "min_release_date": true, "interval_minutes": true, "quality_policy_id": true, "quality_policy_revision": true, "movie_root_id": false, "tv_root_id": false, "bindings": false})
		if settings == nil {
			fail()
			return
		}
		if bindings, ok := settings["bindings"]; ok && personFollowJSONFields(bindings, map[string]bool{"indexer_ids": true, "downloader_id": false}) == nil {
			fail()
			return
		}
		var body SavePersonFollowJSONRequestBody
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
