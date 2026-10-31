package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetadataDiagnosticDirectNilBodyIsRejected(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://diagnostic.test/api/v1/settings/metadata-diagnostics", nil)
	request.Body = nil
	request.Header.Set("Origin", "http://diagnostic.test")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "pulsemedia_" + "session", Value: "fixture-session"})
	ctx := context.WithValue(request.Context(), actorKey, &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleAdmin})
	ctx = context.WithValue(ctx, rawTokenKey, "fixture-session")
	response := httptest.NewRecorder()
	calls := 0
	StrictMetadataDiagnosticJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })).ServeHTTP(response, request.WithContext(ctx))
	if response.Code != 400 || calls != 0 {
		t.Fatalf("nil body code=%d downstream=%d", response.Code, calls)
	}
}
