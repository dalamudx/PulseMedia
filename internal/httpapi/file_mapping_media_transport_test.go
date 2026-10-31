package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFileMappingMediaTransport(t *testing.T) {
	for _, tc := range []struct {
		query string
		valid bool
	}{
		{"?media_query=Old&media_type=tv&tmdb_id=1&media_id=00000000-0000-0000-0000-000000000001&media_cursor=opaque_page-1", true},
		{"?media_query=100%25_literal", true},
		{"?media_query=%E5%8E%9F%E5%A7%8B", true},
		{"?media_query=a&media_query=b", false}, {"?media_type=tv&media_type=movie", false},
		{"?media_type=music", false}, {"?media_query=%FF", false}, {"?media_query=%0A", false},
		{"?media_query=%20trim", false}, {"?media_query=" + strings.Repeat("a", 121), false},
		{"?tmdb_id=0", false}, {"?tmdb_id=-1", false}, {"?tmdb_id=01", false}, {"?tmdb_id=%2B1", false},
		{"?tmdb_id=9223372036854775808", false}, {"?media_id=00000000-0000-0000-0000-000000000000", false},
		{"?media_cursor=a&media_cursor=b", false}, {"?media_cursor=not%2Furlsafe", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			called := false
			h := fileMappingOptionsNoStore(strictFileMappingOptions(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })))
			r := httptest.NewRequest("GET", fileMappingOptionsPath+tc.query, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called != tc.valid || w.Header().Get("Cache-Control") != "private, no-store" || !tc.valid && w.Code != 400 {
				t.Fatal(called, w.Code, w.Header())
			}
		})
	}
}
