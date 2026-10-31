package httpapi

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type externalBatchHTTPFake struct {
	creates, reads, confirms, organizes int
	failure                             error
}

func (f *externalBatchHTTPFake) Create(context.Context, *ports.Actor, uuid.UUID, ports.ExternalBatchInput, string) (*ports.ExternalBatchView, error) {
	f.creates++
	return &ports.ExternalBatchView{}, nil
}
func (f *externalBatchHTTPFake) Read(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.ExternalBatchView, error) {
	f.reads++
	return &ports.ExternalBatchView{}, nil
}
func (f *externalBatchHTTPFake) Confirm(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int) (*ports.UntrackedConfirmationReceipt, error) {
	f.confirms++
	return &ports.UntrackedConfirmationReceipt{}, nil
}
func (f *externalBatchHTTPFake) Organize(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, int) (*ports.UntrackedOrganizationReceipt, error) {
	f.organizes++
	return &ports.UntrackedOrganizationReceipt{}, f.failure
}
func TestExternalBatchHTTPStrictTransport(t *testing.T) {
	scope, batch, file, review := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	selection := ports.ExternalBatchSelection{FileID: uuid.MustParse(file), Confirmation: ports.UntrackedConfirmationInput{ReviewID: uuid.MustParse(review), MaterialHash: strings.Repeat("a", 64), ScopeVersion: 1, FileRevision: 2, CandidateKey: "tmdb.tv:123", Action: "hardlink", TargetRelativePath: "Series/S01E07.mkv"}}
	validRaw, _ := json.Marshal(ports.ExternalBatchInput{Rows: []ports.ExternalBatchSelection{selection}})
	body := string(validRaw)
	base := "/api/v1/library/untracked-scopes/" + scope
	cases := []struct {
		name, method, path, body, key string
		status                        int
	}{
		{"preview", "POST", base + "/external-batches", body, "original", 200},
		{"get", "GET", base + "/external-batches/" + batch, "", "", 200},
		{"original", "GET", base + "/external-batch-commands/original", "", "", 200},
		{"confirm", "POST", base + "/external-batches/" + batch + "/rows/0/confirm", `{"confirmed":true}`, "", 200},
		{"organize", "POST", base + "/external-batches/" + batch + "/rows/1/organize", `{"confirmed":true}`, "", 202},
		{"missing_key", "POST", base + "/external-batches", body, "", 400},
		{"extra", "POST", base + "/external-batches", strings.TrimSuffix(body, "}") + `,"force":true}`, "original", 400},
		{"duplicate_root", "POST", base + "/external-batches", `{"rows":[],"rows":[]}`, "original", 400},
		{"duplicate_row", "POST", base + "/external-batches", strings.Replace(body, `"file_id":`, `"file_id":"`+file+`","file_id":`, 1), "original", 400},
		{"duplicate_inner", "POST", base + "/external-batches", strings.Replace(body, `"scope_version":1`, `"scope_version":1,"scope_version":1`, 1), "original", 400},
		{"null", "POST", base + "/external-batches", `{"rows":null}`, "original", 400},
		{"trailing", "POST", base + "/external-batches", body + ` {}`, "original", 400},
		{"unknown_confirmation", "POST", base + "/external-batches", strings.Replace(body, `"action":"hardlink"`, `"action":"hardlink","force":true`, 1), "original", 400},
		{"false_confirm", "POST", base + "/external-batches/" + batch + "/rows/0/confirm", `{"confirmed":false}`, "", 400},
		{"duplicate_confirm", "POST", base + "/external-batches/" + batch + "/rows/0/confirm", `{"confirmed":true,"confirmed":true}`, "", 400},
		{"row_bound", "POST", base + "/external-batches/" + batch + "/rows/20/confirm", `{"confirmed":true}`, "", 400},
		{"row_alias", "POST", base + "/external-batches/" + batch + "/rows/00/confirm", `{"confirmed":true}`, "", 400},
		{"get_body", "GET", base + "/external-batches/" + batch, `{}`, "", 400},
		{"get_key", "GET", base + "/external-batches/" + batch, "", "unexpected", 400},
		{"query", "GET", base + "/external-batches/" + batch + "?force=1", "", "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &externalBatchHTTPFake{}
			mux := http.NewServeMux()
			RegisterExternalBatchRoutes(mux, fake)
			r := httptest.NewRequest(tc.method, "http://owned.test"+tc.path, strings.NewReader(tc.body))
			r = r.WithContext(context.WithValue(r.Context(), actorKey, untrackedScopeAdmin()))
			r.Header.Set("Origin", "http://owned.test")
			if tc.method == "POST" {
				r.Header.Set("Content-Type", "application/json")
			}
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable batch")
			}
			if tc.status >= 400 && fake.creates+fake.reads+fake.confirms+fake.organizes != 0 {
				t.Fatal("invalid transport called application")
			}
			if tc.method == "GET" && fake.creates+fake.confirms+fake.organizes != 0 {
				t.Fatal("GET mutation")
			}
		})
	}
}

func TestExternalBatchHTTPConflictClassification(t *testing.T) {
	for _, failure := range []error{ports.ErrUntrackedReviewStale, ports.ErrUntrackedOrganizationStale, ports.ErrEndpointVersionConflict} {
		t.Run(failure.Error(), func(t *testing.T) {
			fake := &externalBatchHTTPFake{failure: failure}
			mux := http.NewServeMux()
			RegisterExternalBatchRoutes(mux, fake)
			r := httptest.NewRequest("POST", "http://owned.test/api/v1/library/untracked-scopes/"+uuid.NewString()+"/external-batches/"+uuid.NewString()+"/rows/0/organize", strings.NewReader(`{"confirmed":true}`))
			r.Header.Set("Origin", "http://owned.test")
			r.Header.Set("Content-Type", "application/json")
			r = r.WithContext(context.WithValue(r.Context(), actorKey, untrackedScopeAdmin()))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			var problem struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(w.Body.Bytes(), &problem) != nil || w.Code != 409 || problem.Code != "SOURCE_CHANGED" || w.Header().Get("Content-Type") != "application/problem+json" || fake.organizes != 1 {
				t.Fatalf("conflict contract: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
func TestExternalBatchHTTPCurrentSessionBoundary(t *testing.T) {
	for _, kind := range []string{"anonymous", "member", "token", "authorization", "origin"} {
		t.Run(kind, func(t *testing.T) {
			fake := &externalBatchHTTPFake{}
			mux := http.NewServeMux()
			RegisterExternalBatchRoutes(mux, fake)
			r := httptest.NewRequest("POST", "http://owned.test/api/v1/library/untracked-scopes/"+uuid.NewString()+"/external-batches/"+uuid.NewString()+"/rows/0/confirm", strings.NewReader(`{"confirmed":true}`))
			a := untrackedScopeAdmin()
			switch kind {
			case "anonymous":
				a = nil
			case "member":
				a.Role = ports.RoleMember
			case "token":
				a.Scopes = []string{"admin"}
			case "authorization":
				r.Header.Set("Authorization", "Bearer forbidden")
			}
			if kind != "origin" {
				r.Header.Set("Origin", "http://owned.test")
			}
			r.Header.Set("Content-Type", "application/json")
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("boundary status %d", w.Code)
			}
			if fake.confirms != 0 {
				t.Fatal("boundary crossed")
			}
		})
	}
}
