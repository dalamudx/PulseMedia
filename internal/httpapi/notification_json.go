package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

func notificationRoute(path string) bool {
	return path == "/api/v1/notification-channels" || strings.HasPrefix(path, "/api/v1/notification-channels/")
}
func notificationNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if notificationRoute(r.URL.Path) || r.URL.Path == "/api/v1/integrations/deliveries" {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func strictNotificationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !notificationRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if e := siteCheckinConfigActorError(GetActor(r.Context())); e != nil {
			status := http.StatusForbidden
			if GetActor(r.Context()) == nil {
				status = http.StatusUnauthorized
			}
			WriteJSONError(w, status, "FORBIDDEN", "administrator session required", GetRequestID(r.Context()))
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid notification channel request", GetRequestID(r.Context()))
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			fail()
			return
		}
		var required map[string]bool
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/notification-channels":
			required = map[string]bool{"name": true, "channel_type": true, "config": true}
			body = &CreateNotificationChannelJSONRequestBody{}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/replacement"):
			required = map[string]bool{"expected_updated_at": true, "name": true, "channel_type": true, "config": true, "accept_old_pending": true}
			body = &ReplaceNotificationChannelJSONRequestBody{}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/test"):
			required = map[string]bool{"expected_updated_at": true}
			body = &QueueNotificationTestJSONRequestBody{}
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/state"):
			required = map[string]bool{"expected_updated_at": true, "is_enabled": true}
			body = &SetNotificationChannelStateJSONRequestBody{}
		default:
			next.ServeHTTP(w, r)
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !validCookieCloudCommandKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || kind != "application/json" {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, e := io.ReadAll(http.MaxBytesReader(w, original, 16384))
		if e != nil {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if !uniqueEndpointJSONValue(decoder, 0, required) {
			fail()
			return
		}
		if _, e = decoder.Token(); e != io.EOF {
			fail()
			return
		}
		// Nested configuration uses exact canonical keys, not encoding/json's case aliases.
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			fail()
			return
		}
		if config, ok := fields["config"]; ok {
			d := json.NewDecoder(bytes.NewReader(config))
			if !uniqueEndpointJSONValue(d, 0, map[string]bool{"url": false, "secret": false, "bot_token": false, "chat_id": false, "image_url": false, "image_key": false}) {
				fail()
				return
			}
		}
		d := json.NewDecoder(bytes.NewReader(raw))
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
