package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUntrackedCompanionMiddlewarePreservesAdjacentRoutes(t *testing.T) {
	for _, path := range []string{"/api/v1/library/untracked-organizations/prepare", "/api/v1/library/untracked-organizations/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "/api/v1/library/untracked-scopes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/discoveries/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/reviews"} {
		t.Run(path, func(t *testing.T) {
			const body = `{"scope_version":1,"file_revision":1,"media_type":"movie","tmdb_id":0}`
			called := false
			handler := StrictUntrackedCompanionJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != body {
					t.Fatal("adjacent request body changed", err)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if !called || response.Code != http.StatusNoContent {
				t.Fatal("adjacent route intercepted", response.Code)
			}
		})
	}
}
