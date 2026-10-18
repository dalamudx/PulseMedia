package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/media_library"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failedCatalogReader struct{}

func (failedCatalogReader) ListLibraryCatalog(context.Context, *ports.Actor, ports.LibraryCatalogRequest) (*ports.LibraryCatalogPage, error) {
	return nil, errors.New("postgres://fixture-passkey@private-root/query SQLSTATE secret")
}
func TestLibraryCatalogPublicFailure(t *testing.T) {
	handler := &ServerHandler{libraryCatalogSvc: media_library.NewService(failedCatalogReader{})}
	for _, tc := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{{"missing-session", nil, 401}, {"current-member", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, 503}, {"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: uuid.NewString(), Role: ports.RoleAdmin}, 403}, {"no-read-token", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember, Scopes: []string{"write"}}, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.actor != nil {
				ctx = context.WithValue(ctx, actorKey, tc.actor)
			}
			response, e := handler.ListLibraryCatalog(ctx, ListLibraryCatalogRequestObject{})
			if e != nil {
				t.Fatal(e)
			}
			w := httptest.NewRecorder()
			if e = response.VisitListLibraryCatalogResponse(w); e != nil {
				t.Fatal(e)
			}
			if w.Code != tc.status || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Fatal("safe response", w.Code, w.Body.String())
			}
			for _, secret := range []string{"postgres://", "passkey", "private-root", "SQLSTATE"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("leaked external error", secret)
				}
			}
			if tc.status == http.StatusServiceUnavailable && !strings.Contains(w.Body.String(), "UNAVAILABLE") {
				t.Fatal(w.Body.String())
			}
		})
	}
}

type recordingCatalogReader struct {
	calls int
	limit int
}

func (r *recordingCatalogReader) ListLibraryCatalog(_ context.Context, _ *ports.Actor, request ports.LibraryCatalogRequest) (*ports.LibraryCatalogPage, error) {
	r.calls++
	r.limit = request.Limit
	return &ports.LibraryCatalogPage{}, nil
}

func TestLibraryCatalogExplicitPageBounds(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		limit                 *int
		status, expectedLimit int
	}{
		{"omitted-default", nil, http.StatusOK, 50},
		{"minimum", pointerLibraryLimit(1), http.StatusOK, 1},
		{"maximum", pointerLibraryLimit(100), http.StatusOK, 100},
		{"explicit-zero", pointerLibraryLimit(0), http.StatusBadRequest, 0},
		{"negative", pointerLibraryLimit(-1), http.StatusBadRequest, 0},
		{"over-maximum", pointerLibraryLimit(101), http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &recordingCatalogReader{}
			handler := &ServerHandler{libraryCatalogSvc: media_library.NewService(reader)}
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
			ctx := context.WithValue(context.Background(), actorKey, actor)
			response, err := handler.ListLibraryCatalog(ctx, ListLibraryCatalogRequestObject{Params: ListLibraryCatalogParams{Limit: tc.limit}})
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			if err := response.VisitListLibraryCatalogResponse(recorder); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != tc.status {
				t.Fatalf("status=%d expected=%d", recorder.Code, tc.status)
			}
			expectedCalls := 0
			if tc.status == http.StatusOK {
				expectedCalls = 1
			}
			if reader.calls != expectedCalls || reader.limit != tc.expectedLimit {
				t.Fatalf("reader calls=%d limit=%d", reader.calls, reader.limit)
			}
		})
	}
}
func pointerLibraryLimit(n int) *int { return &n }
