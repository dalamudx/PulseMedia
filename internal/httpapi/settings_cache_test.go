package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSettingsNoStore(t *testing.T) {
	for _, path := range []string{"/api/v1/settings", "/api/v1/settings/fanart.api_key", "/api/v1/settings/fanart.artwork", "/api/v1/settings/fanart.api_key/commands/original", "/api/v1/settings/tmdb.api_key", "/api/v1/settings-other"} {
		t.Run(path, func(t *testing.T) {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusForbidden, http.StatusServiceUnavailable} {
				handler := settingsNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				want := "private, no-store"
				if path == "/api/v1/settings-other" {
					want = ""
				}
				if w.Code != status || w.Header().Get("Cache-Control") != want {
					t.Fatalf("status/cache mismatch: %d %q", w.Code, w.Header().Get("Cache-Control"))
				}
			}
		})
	}
}
