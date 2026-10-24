package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

func webPushRoute(path string) bool { return strings.HasPrefix(path, "/api/v1/web-push/") }
func webPushNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if webPushRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func strictWebPushJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webPushRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := webPushActorError(GetActor(r.Context())); err != nil {
			status, code := http.StatusForbidden, "FORBIDDEN"
			if GetActor(r.Context()) == nil {
				status, code = http.StatusUnauthorized, "UNAUTHORIZED"
			}
			WriteJSONError(w, status, code, "current account session required", GetRequestID(r.Context()))
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid browser notification request", GetRequestID(r.Context()))
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			fail()
			return
		}
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		var required map[string]bool
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/web-push/devices":
			required = map[string]bool{"id": true, "vapid_version": true, "subscription": true}
			body = &RegisterWebPushDeviceJSONRequestBody{}
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/revocation"):
			required = map[string]bool{"expected_updated_at": true}
			body = &RevokeWebPushDeviceJSONRequestBody{}
		default:
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
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 8192))
		if err != nil {
			fail()
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if !uniqueEndpointJSONValue(d, 0, required) {
			fail()
			return
		}
		if _, err = d.Token(); err != io.EOF {
			fail()
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			fail()
			return
		}
		if sub, ok := fields["subscription"]; ok {
			nested := json.NewDecoder(bytes.NewReader(sub))
			if !uniqueEndpointJSONValue(nested, 0, map[string]bool{"endpoint": true, "p256dh": true, "auth": true}) {
				fail()
				return
			}
		}
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(body) != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
