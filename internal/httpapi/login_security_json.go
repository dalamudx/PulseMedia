package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

// StrictLoginSecurityJSON is public admission: it must run before authentication,
// independently from the cookie-only account security middleware.
func StrictLoginSecurityJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		// Public login admission precedes authenticated middleware. Reject a
		// foreign browser origin before parsing or validating credential input.
		if len(r.Header.Values("Origin")) > 0 && !sameRequestOrigin(r) {
			WriteJSONError(w, http.StatusForbidden, "CSRF_REJECTED", "same-origin request required", GetRequestID(r.Context()))
			return
		}
		fail := func() {
			WriteJSONError(w, 400, "INVALID_LOGIN_REQUEST", "invalid login request", GetRequestID(r.Context()))
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" || len(r.Header.Values("Content-Type")) != 1 || r.Body == nil {
			fail()
			return
		}
		originalBody := r.Body
		defer func() { _ = originalBody.Close() }()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			fail()
			return
		}
		fields := map[string]string{}
		for d.More() {
			token, err = d.Token()
			name, ok := token.(string)
			if err != nil || !ok {
				fail()
				return
			}
			switch name {
			case "username", "password", "otp", "recovery_code":
			default:
				fail()
				return
			}
			if _, exists := fields[name]; exists {
				fail()
				return
			}
			var raw json.RawMessage
			if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				fail()
				return
			}
			var value string
			if json.Unmarshal(raw, &value) != nil {
				fail()
				return
			}
			fields[name] = value
		}
		if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
			fail()
			return
		}
		username, userOK := fields["username"]
		password, passwordOK := fields["password"]
		if !userOK || !passwordOK || len(username) > 80 || len(password) > 1024 {
			fail()
			return
		}
		otp, hasOTP := fields["otp"]
		recovery, hasRecovery := fields["recovery_code"]
		if hasOTP && hasRecovery {
			fail()
			return
		}
		if hasOTP {
			if len(otp) != 6 {
				fail()
				return
			}
			for _, digit := range otp {
				if digit < '0' || digit > '9' {
					fail()
					return
				}
			}
		}
		if hasRecovery && (len(recovery) == 0 || len(recovery) > 64) {
			fail()
			return
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
