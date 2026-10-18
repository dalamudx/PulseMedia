package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpgradePreviewDoesNotInvalidateItsOwnComparison(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		status             int
		reset              bool
	}{
		{"readonly preview", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/upgrade-previews", http.StatusOK, false},
		{"reservation", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/upgrade-reservations", http.StatusAccepted, true},
		{"preparation", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/upgrade-preparations", http.StatusAccepted, true},
		{"send", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/upgrade-sends", http.StatusAccepted, true},
		{"preview-like nested mutation", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/commands/upgrade-previews", http.StatusOK, true},
		{"preview-like different resource", http.MethodPost, "/api/v1/downloaders/10000000-0000-4000-8000-000000000001/upgrade-previews", http.StatusOK, true},
		{"failed mutation", http.MethodPost, "/api/v1/subscriptions/10000000-0000-4000-8000-000000000001/upgrade-sends", http.StatusConflict, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewSSEHub()
			defer h.Close()
			before := h.sequence
			handler := activityChanges(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
			if got := h.sequence > before; got != tc.reset {
				t.Fatalf("reset=%t want %t", got, tc.reset)
			}
		})
	}
}
