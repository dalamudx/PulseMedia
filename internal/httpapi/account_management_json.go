package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func accountManagementPath(path string) bool {
	return path == "/api/v1/accounts" || strings.HasPrefix(path, "/api/v1/accounts/")
}

func accountPasswordResetPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/accounts/") && strings.HasSuffix(path, "/password")
}
func accountSecurityPath(path string) bool {
	return path == "/api/v1/accounts" || strings.HasPrefix(path, "/api/v1/accounts/") || path == "/api/v1/auth/password" || path == "/api/v1/auth/sessions" || strings.HasPrefix(path, "/api/v1/auth/sessions/") || path == "/api/v1/auth/mfa" || strings.HasPrefix(path, "/api/v1/auth/mfa/") || strings.HasPrefix(path, "/api/v1/auth/security/commands/")
}

// StrictAccountManagementJSON runs before generated decoding, inside authentication.
// It also marks early admission failures no-store, including /accounts responses.
func StrictAccountManagementJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !accountSecurityPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		actor := GetActor(r.Context())
		if actor == nil {
			WriteJSONError(w, 401, "UNAUTHORIZED", "authentication required", GetRequestID(r.Context()))
			return
		}
		cookies := 0
		for _, cookie := range r.Cookies() {
			if cookie.Name == "pulsemedia_session" {
				cookies++
			}
		}
		if cookies > 1 || len(r.Header.Values("Cookie")) > 1 {
			WriteJSONError(w, 400, "INVALID_ACCOUNT_COMMAND", "invalid session cookie", GetRequestID(r.Context()))
			return
		}
		if cookies != 1 || r.Header.Get("Authorization") != "" || actor.Scopes != nil {
			WriteJSONError(w, 403, "FORBIDDEN", "cookie session required", GetRequestID(r.Context()))
			return
		}
		if accountManagementPath(r.URL.Path) && actor.Role != ports.RoleAdmin {
			WriteJSONError(w, http.StatusForbidden, "FORBIDDEN", "administrator session required", GetRequestID(r.Context()))
			return
		}
		if r.Method != "POST" && r.Method != "PUT" {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, 400, "INVALID_ACCOUNT_COMMAND", "invalid account security request", GetRequestID(r.Context()))
		}
		key := r.Header.Get("Idempotency-Key")
		if len(r.Header.Values("Idempotency-Key")) != 1 || key == "" || len(key) > 160 || strings.TrimSpace(key) != key || strings.ContainsAny(key, ",\r\n") {
			fail()
			return
		}
		var required, optional []string
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/accounts":
			required = []string{"username", "password", "role"}
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/accounts/"):
			required = []string{"role", "is_active"}
		case r.URL.Path == "/api/v1/auth/password" || accountPasswordResetPath(r.URL.Path):
			required = []string{"current_password", "new_password"}
			optional = []string{"otp", "recovery_code"}
		case r.URL.Path == "/api/v1/auth/mfa/setup":
			required = []string{"current_password"}
		case r.URL.Path == "/api/v1/auth/mfa/confirm":
			required = []string{"challenge_id", "otp"}
		case r.URL.Path == "/api/v1/auth/mfa/disable" || r.URL.Path == "/api/v1/auth/mfa/recovery-codes" || r.URL.Path == "/api/v1/auth/sessions/revoke-others":
			required = []string{"current_password"}
			optional = []string{"otp", "recovery_code"}
		case strings.HasPrefix(r.URL.Path, "/api/v1/auth/sessions/") && strings.HasSuffix(r.URL.Path, "/revoke"):
			if r.Body != nil {
				body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
				_ = r.Body.Close()
				if e != nil || len(bytes.TrimSpace(body)) != 0 {
					fail()
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(nil))
			}
			next.ServeHTTP(w, r)
			return
		default:
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || mediaType != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
			fail()
			return
		}
		originalBody := r.Body
		defer func() { _ = originalBody.Close() }()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		token, e := d.Token()
		if e != nil || token != json.Delim('{') {
			fail()
			return
		}
		fields := map[string]json.RawMessage{}
		allowed := map[string]bool{}
		for _, k := range append(required, optional...) {
			allowed[k] = true
		}
		for d.More() {
			token, e = d.Token()
			name, ok := token.(string)
			if e != nil || !ok || !allowed[name] {
				fail()
				return
			}
			if _, exists := fields[name]; exists {
				fail()
				return
			}
			var value json.RawMessage
			if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				fail()
				return
			}
			if name == "is_active" {
				var b bool
				if json.Unmarshal(value, &b) != nil {
					fail()
					return
				}
			} else {
				var valueString string
				if json.Unmarshal(value, &valueString) != nil {
					fail()
					return
				}
			}
			fields[name] = value
		}
		if token, e = d.Token(); e != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
			fail()
			return
		}
		for _, k := range required {
			if _, ok := fields[k]; !ok {
				fail()
				return
			}
		}
		raw, e := json.Marshal(fields)
		if e != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
