package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubtitleCandidateCommandsCannotSupplyProviderOrURL(t *testing.T) {
	for _, body := range []string{`{"expected_mapping_revision":7}`, `{"expected_mapping_revision":7,"provider":"arbitrary"}`, `{"expected_mapping_revision":7,"url":"https://untrusted.invalid/private"}`, `{"expected_mapping_revision":7,"content":"untrusted bytes"}`, `{"Expected_mapping_revision":7}`, `{"expected_mapping_revision":7,"expected_mapping_revision":8}`} {
		calls := 0
		handler := StrictMPBusinessJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) }))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/library/files/01960000-0000-7000-8000-000000000001/subtitle-candidates/queries", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "owned-command")
		r = r.WithContext(context.WithValue(r.Context(), actorKey, mediaRequestFixtureActor(ports.RoleAdmin)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if body == `{"expected_mapping_revision":7}` {
			if w.Code != 204 || calls != 1 {
				t.Fatal("valid original-file command rejected")
			}
		} else if w.Code != 400 || calls != 0 {
			t.Fatal("untrusted retrieval authority accepted", w.Code)
		}
	}
}
