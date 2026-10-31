package httpapi

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/ports"
	classcase "github.com/dalamudx/PulseMedia/internal/usecase/classification"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type classificationHTTPRepo struct{ calls int }

func (r *classificationHTTPRepo) ReadClassification(context.Context, *ports.Actor) (*ports.ClassificationDocument, error) {
	r.calls++
	return &ports.ClassificationDocument{Version: 1, Rules: []classification.Rule{}}, nil
}
func (r *classificationHTTPRepo) PublishClassification(context.Context, *ports.Actor, ports.ClassificationPublish, string) (*ports.ClassificationDocument, error) {
	r.calls++
	return &ports.ClassificationDocument{Version: 1, Rules: []classification.Rule{}}, nil
}
func (r *classificationHTTPRepo) PreviewClassification(context.Context, *ports.Actor, ports.ClassificationPreviewInput) (*ports.ClassificationPreview, error) {
	r.calls++
	return &ports.ClassificationPreview{Version: 1, Result: classification.Result{Status: "insufficient_evidence", Matches: []classification.Match{}, Reasons: []string{"trusted_metadata_missing"}}}, nil
}
func TestClassificationHTTPStrictAuthorityAndCSRF(t *testing.T) {
	rule := `{"id":"movie","media_type":"movie","genres":[],"languages":[],"countries":[],"root_id":"00000000-0000-4000-8000-000000000001"}`
	valid := `{"expected_version":0,"rules":[` + rule + `]}`
	cases := []struct {
		name, method, path, body, role string
		scopes                         []string
		origin                         string
		keys                           []string
		status                         int
	}{
		{name: "admin publish", body: valid, role: "admin", origin: "http://pulse.test", keys: []string{"original"}, status: 200},
		{name: "member publish", body: valid, role: "member", origin: "http://pulse.test", keys: []string{"original"}, status: 403},
		{name: "read token publish", body: valid, role: "admin", scopes: []string{"read"}, origin: "http://pulse.test", keys: []string{"original"}, status: 403},
		{name: "no origin", body: valid, role: "admin", keys: []string{"original"}, status: 403},
		{name: "foreign origin", body: valid, role: "admin", origin: "http://other.test", keys: []string{"original"}, status: 403},
		{name: "missing key", body: valid, role: "admin", origin: "http://pulse.test", status: 400},
		{name: "duplicate header", body: valid, role: "admin", origin: "http://pulse.test", keys: []string{"original", "original"}, status: 400},
		{name: "null", body: `null`, status: 400}, {name: "duplicate root", body: `{"expected_version":0,"expected_version":1,"rules":[]}`, status: 400},
		{name: "case alias", body: `{"Expected_Version":0,"rules":[]}`, status: 400},
		{name: "unknown root", body: `{"expected_version":0,"rules":[],"url":"unsafe"}`, status: 400},
		{name: "duplicate rule", body: `{"expected_version":0,"rules":[` + strings.Replace(rule, `"id":"movie"`, `"id":"movie","id":"other"`, 1) + `]}`, status: 400},
		{name: "case rule", body: `{"expected_version":0,"rules":[` + strings.Replace(rule, `"id":`, `"ID":`, 1) + `]}`, status: 400},
		{name: "null dimension", body: `{"expected_version":0,"rules":[` + strings.Replace(rule, `"genres":[]`, `"genres":null`, 1) + `]}`, status: 400},
		{name: "wrong dimension", body: `{"expected_version":0,"rules":[` + strings.Replace(rule, `"genres":[]`, `"genres":[1]`, 1) + `]}`, status: 400},
		{name: "extra json", body: valid + `{}`, status: 400},
		{name: "member preview", method: "POST", path: "/api/v1/classification/previews", body: `{"version":1,"tmdb_id":1,"media_type":"movie"}`, role: "member", status: 200},
		{name: "read token preview", method: "POST", path: "/api/v1/classification/previews", body: `{"version":1,"tmdb_id":1,"media_type":"movie"}`, role: "member", scopes: []string{"read"}, status: 403},
		{name: "preview unknown", method: "POST", path: "/api/v1/classification/previews", body: `{"version":1,"tmdb_id":1,"media_type":"movie","extra":true}`, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &classificationHTTPRepo{}
			server := &ServerHandler{}
			server.SetClassificationService(classcase.NewService(repo))
			handler := CSRFMiddleware()(APITokenScopeMiddleware()(StrictClassificationJSON(Handler(NewStrictHandler(server, nil)))))
			method, path := tc.method, tc.path
			if method == "" {
				method = "PUT"
			}
			if path == "" {
				path = "/api/v1/classification"
			}
			req := httptest.NewRequest(method, "http://pulse.test"+path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "owned"})
			origin := tc.origin
			if origin == "" && tc.name != "no origin" {
				origin = "http://pulse.test"
			}
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			keys := tc.keys
			if keys == nil && tc.name != "missing key" {
				keys = []string{"original"}
			}
			for _, k := range keys {
				req.Header.Add("Idempotency-Key", k)
			}
			role := tc.role
			if role == "" {
				role = "admin"
			}
			a := &ports.Actor{Type: ports.ActorTypeUser, ID: "00000000-0000-4000-8000-000000000002", Role: role, Scopes: tc.scopes}
			req = req.WithContext(context.WithValue(req.Context(), actorKey, a))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatal(fmt.Sprint(w.Code, " ", w.Body.String()))
			}
			if tc.status == 200 && repo.calls != 1 || tc.status != 200 && repo.calls != 0 {
				t.Fatal("unexpected application access", repo.calls)
			}
		})
	}
}
