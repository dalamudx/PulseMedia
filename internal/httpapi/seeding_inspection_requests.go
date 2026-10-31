package httpapi

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// StrictSeedingInspectionRequests validates the manual native read inside auth/CSRF.
// The router also applies its outer no-store wrapper to early authentication denials.
func StrictSeedingInspectionRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "operations" || parts[4] != "seeding-observations" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		fail := func(status int, code string) {
			writeProblem(w, status, problemResponse(status, code, "invalid seeding inspection request", GetRequestID(r.Context())))
		}
		id, err := uuid.Parse(parts[3])
		if len(parts) != 5 || err != nil || id == uuid.Nil || id.String() != parts[3] || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path {
			fail(http.StatusBadRequest, "INVALID_SEEDING_INSPECTION")
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			fail(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		if len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("X-API-Key")) != 0 {
			fail(http.StatusForbidden, "FORBIDDEN")
			return
		}
		sessions, nonempty := 0, false
		for _, cookie := range r.Cookies() {
			if cookie.Name == "pulsemedia_session" {
				sessions++
				nonempty = cookie.Value != ""
			}
		}
		if sessions != 1 || !nonempty {
			fail(http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		if r.Body == nil {
			r.Body = http.NoBody
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 1024))
		if err != nil {
			fail(http.StatusBadRequest, "INVALID_SEEDING_INSPECTION")
			return
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 {
			content, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if parseErr != nil || content != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
				fail(http.StatusBadRequest, "INVALID_SEEDING_INSPECTION")
				return
			}
			// An empty object may contain JSON whitespace, but no fields or extra values.
			compact := bytes.Map(func(c rune) rune {
				if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
					return -1
				}
				return c
			}, trimmed)
			if !bytes.Equal(compact, []byte("{}")) {
				fail(http.StatusBadRequest, "INVALID_SEEDING_INSPECTION")
				return
			}
		}
		// There is no input to this read; avoid handing optional JSON decoding an EOF.
		r.Body = io.NopCloser(strings.NewReader("{}"))
		r.ContentLength = 2
		r.Header.Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
