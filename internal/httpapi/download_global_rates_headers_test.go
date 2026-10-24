package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadGlobalRatesNoStore(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		for _, path := range []string{"/api/v1/download-clients/global-rates", "/api/v1/download-clients/global-rates/"} {
			recorder := httptest.NewRecorder()
			downloadGlobalRatesNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != status || recorder.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("private observation or error cacheable", status, path)
			}
		}
	}
	recorder := httptest.NewRecorder()
	downloadGlobalRatesNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/download-clients", nil))
	if recorder.Header().Get("Cache-Control") != "" {
		t.Fatal("unrelated response changed")
	}
}
