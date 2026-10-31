package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFileMappingOptionsTransport(t *testing.T) {
	root := "00000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		name, method, query, body, key string
		valid                          bool
	}{
		{"first", "GET", "", "", "", true},
		{"root_page", "GET", "?root_id=" + root + "&cursor=opaque_page-1", "", "", true},
		{"duplicate_root", "GET", "?root_id=" + root + "&root_id=" + root, "", "", false},
		{"duplicate_cursor", "GET", "?cursor=one&cursor=two", "", "", false},
		{"zero_root", "GET", "?root_id=00000000-0000-0000-0000-000000000000", "", "", false},
		{"unknown", "GET", "?limit=1000", "", "", false},
		{"empty", "GET", "?cursor=", "", "", false},
		{"long", "GET", "?cursor=" + strings.Repeat("x", 513), "", "", false},
		{"malformed_encoding", "GET", "?cursor=%zz", "", "", false},
		{"body", "GET", "", "null", "", false},
		{"command_key", "GET", "", "", "original-key", false},
		{"head", "HEAD", "", "", "", false},
		{"post", "POST", "", "", "", false},
		{"force_query", "GET", "?", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, fileMappingOptionsPath+tc.query, strings.NewReader(tc.body))
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			called := false
			h := fileMappingOptionsNoStore(strictFileMappingOptions(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) })))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called != tc.valid || w.Header().Get("Cache-Control") != "private, no-store" || (!tc.valid && w.Code != 400) {
				t.Fatal(called, w.Code, w.Header())
			}
		})
	}
}
