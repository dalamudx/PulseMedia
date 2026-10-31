package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
)

type inspectionHTTPRepository struct {
	calls   int
	failure error
}

func (r *inspectionHTTPRepository) PrepareSeedingInspection(_ context.Context, _ *ports.Actor, op uuid.UUID) (*ports.PreparedSeedingInspection, error) {
	r.calls++
	if r.failure != nil {
		return nil, r.failure
	}
	return &ports.PreparedSeedingInspection{Reader: inspectionHTTPReader{}, InfoHash: strings.Repeat("a", 40), OperationTag: "pulsemedia:op:" + op.String(), SavePath: "/secret-private-downloads", Validate: func(context.Context) error { return nil }, Release: func() {}}, nil
}

type inspectionHTTPReader struct{}

func (inspectionHTTPReader) ReadSeedingTorrent(_ context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: "/secret-private-downloads"}, nil
}
func (inspectionHTTPReader) ReadSeedingFreeSpace(context.Context, string) (ports.SeedingPathSpace, error) {
	panic("unexpected free space read")
}

func TestSeedingInspectionStrictRequests(t *testing.T) {
	const path = "/api/v1/operations/12345678-1234-4234-8234-123456789abc/seeding-observations"
	for _, body := range []string{"", " \n", `{}`, "{ \t\r\n }"} {
		h := StrictSeedingInspectionRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "session"})
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 204 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("valid input rejected", w.Code, body)
		}
	}
	for _, tc := range []struct {
		name, body, suffix, method, auth, cookie string
		status                                   int
	}{
		{name: "fields", body: `{"owner":"secret"}`, status: 400}, {name: "null", body: `null`, status: 400}, {name: "array", body: `[]`, status: 400}, {name: "trailing", body: `{} {}`, status: 400}, {name: "too large", body: strings.Repeat(" ", 1025), status: 400},
		{name: "query", suffix: "?secret=private", status: 400}, {name: "empty query", suffix: "?", status: 400}, {name: "trailing path", suffix: "/", status: 400}, {name: "read get", method: "GET", status: 405},
		{name: "bearer", auth: "Bearer private", status: 403}, {name: "other authorization", auth: "Basic private", status: 403}, {name: "missing cookie", cookie: "none", status: 401}, {name: "duplicate cookie", cookie: "double", status: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := StrictSeedingInspectionRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("rejected request dispatched") }))
			method := tc.method
			if method == "" {
				method = "POST"
			}
			req := httptest.NewRequest(method, path+tc.suffix, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.cookie != "none" {
				req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "session"})
			}
			if tc.cookie == "double" {
				req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "other"})
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "private") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	// Outer wrapping preserves no-store when CSRF or authentication rejects first.
	for _, status := range []int{401, 403, 503} {
		h := StrictSeedingInspectionRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		req := httptest.NewRequest("POST", path, nil)
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "session"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("early denial cacheable")
		}
	}
}
func TestSeedingInspectionGeneratedHTTPDispatch(t *testing.T) {
	op := uuid.New()
	repo := &inspectionHTTPRepository{}
	s := &ServerHandler{}
	s.SetSeedingInspectionService(downloadcase.NewSeedingInspectionService(repo, clock.New()))
	h := StrictSeedingInspectionRequests(HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{}))
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/operations/"+op.String()+"/seeding-observations", strings.NewReader("{}"))
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "session"})
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), actorKey, actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := call()
	var result ports.SeedingInspection
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ShareRatio != nil || result.SeedingSeconds != nil || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	actor.Scopes = []string{"operations:read"}
	w = call()
	if w.Code != 403 || repo.calls != 1 {
		t.Fatal("API token reached read", w.Code)
	}
	actor.Scopes = nil
	repo.failure = errors.New("secret endpoint SQLSTATE pgx")
	w = call()
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "SQLSTATE") {
		t.Fatal("private failure exposed", w.Code, w.Body.String())
	}
}
