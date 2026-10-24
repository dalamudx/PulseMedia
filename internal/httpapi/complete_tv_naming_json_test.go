package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteTVNamingVersionStrictJSON(t *testing.T) {
	base := `"operation_id":"11111111-1111-4111-8111-111111111111","tracking_job_id":"22222222-2222-4222-8222-222222222222"`
	for _, test := range []struct {
		name, extra string
		status      int
	}{
		{"legacy", "", http.StatusNoContent}, {"zero", `,"naming_version":0`, http.StatusNoContent}, {"positive", `,"naming_version":7`, http.StatusNoContent},
		{"negative", `,"naming_version":-1`, http.StatusBadRequest}, {"null", `,"naming_version":null`, http.StatusBadRequest}, {"string", `,"naming_version":"7"`, http.StatusBadRequest}, {"fraction", `,"naming_version":1.5`, http.StatusBadRequest}, {"object", `,"naming_version":{}`, http.StatusBadRequest}, {"overflow", `,"naming_version":9223372036854775808`, http.StatusBadRequest}, {"case", `,"Naming_Version":7`, http.StatusBadRequest}, {"duplicate", `,"naming_version":7,"naming_version":7`, http.StatusBadRequest}, {"escaped_duplicate", `,"naming_version":7,"naming_\u0076ersion":7`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/download-tasks/33333333-3333-4333-8333-333333333333/complete-tv-organizations", strings.NewReader("{"+base+test.extra+"}"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "original-key")
			w := httptest.NewRecorder()
			strictCompleteTVOrganizationJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, request)
			if w.Code != test.status {
				t.Fatalf("status %d expected %d", w.Code, test.status)
			}
		})
	}
}
