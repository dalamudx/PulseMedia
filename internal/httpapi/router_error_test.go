package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
)

type failingHealthServer struct{ httpapi.StrictServerInterface }

func (failingHealthServer) GetHealthz(context.Context, httpapi.GetHealthzRequestObject) (httpapi.GetHealthzResponseObject, error) {
	return nil, errors.New("adapter failed: sensitive-download-passkey")
}

func TestRouterNeverExposesStrictOrParseErrors(t *testing.T) {
	a, _, _, _, _, _, _, _ := setupOperationHTTP(t)
	router := httpapi.NewRouter(a, failingHealthServer{}, nil, nil)

	internal := httptest.NewRecorder()
	router.ServeHTTP(internal, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if internal.Code != http.StatusInternalServerError || strings.Contains(internal.Body.String(), "sensitive-download-passkey") {
		t.Fatalf("strict error leaked: status=%d body=%s", internal.Code, internal.Body.String())
	}
	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/operations/not-a-uuid", nil))
	if invalid.Code != http.StatusBadRequest || strings.Contains(invalid.Body.String(), "not-a-uuid") {
		t.Fatalf("parameter parser leaked raw input: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
