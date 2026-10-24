package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
)

const cookieCloudConfigPath = "/api/v1/integrations/cookiecloud/config"

func cookieCloudConfigRoute(path string) bool {
	return path == cookieCloudConfigPath || strings.HasPrefix(path, cookieCloudConfigPath+"/")
}
func cookieCloudConfigNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookieCloudConfigRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func validCookieCloudCommandKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ':', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
func strictCookieCloudConfigJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cookieCloudConfigRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := siteCheckinConfigActorError(GetActor(r.Context())); err != nil {
			p := cookieCloudConfigProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid CookieCloud configuration request", GetRequestID(r.Context()))
		}
		tail := strings.TrimPrefix(r.URL.Path, cookieCloudConfigPath)
		receipt := strings.HasPrefix(tail, "/commands/") && validCookieCloudCommandKey(strings.TrimPrefix(tail, "/commands/"))
		if (tail != "" && !receipt) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
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
				body, err := io.ReadAll(io.LimitReader(r.Body, 1))
				if err != nil || len(body) != 0 {
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
		if !uniqueEndpointJSONValue(decoder, 0, map[string]bool{"expected_version": true, "enabled": true, "interval_seconds": true, "connection": false}) {
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
		if connection, exists := fields["connection"]; exists {
			child := json.NewDecoder(bytes.NewReader(connection))
			child.UseNumber()
			if !uniqueEndpointJSONValue(child, 0, map[string]bool{"server_url": true, "user_key": true, "password": true}) {
				fail()
				return
			}
		}
		var input CookieCloudConfigInput
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.ExpectedVersion < 0 || input.ExpectedVersion == math.MaxInt64 || input.IntervalSeconds < 60 || input.IntervalSeconds > 86400 {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
