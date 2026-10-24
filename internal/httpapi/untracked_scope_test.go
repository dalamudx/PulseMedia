package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const untrackedScopeID = "11111111-1111-4111-8111-111111111111"
const untrackedScopePath = "/api/v1/library/untracked-scopes/" + untrackedScopeID
const untrackedScopeBody = `{"enabled":false,"expected_version":0,"client_id":"33333333-3333-4333-8333-333333333333","expected_client_version":"2026-01-01T00:00:00Z","remote_prefix":"/downloads","target_root_id":"44444444-4444-4444-8444-444444444444","expected_root_generation":1}`

type untrackedScopeFake struct {
	config       *ports.UntrackedScope
	receipt      *ports.UntrackedScopeReceipt
	err          error
	calls, saves int
	actor        ports.Actor
	id           uuid.UUID
	input        ports.UntrackedScopeInput
	key          string
}

func (f *untrackedScopeFake) track(actor *ports.Actor, id uuid.UUID) {
	f.calls++
	f.actor = *actor
	f.id = id
}
func (f *untrackedScopeFake) Read(_ context.Context, actor *ports.Actor, id uuid.UUID) (*ports.UntrackedScope, error) {
	f.track(actor, id)
	return f.config, f.err
}
func (f *untrackedScopeFake) Save(_ context.Context, actor *ports.Actor, id uuid.UUID, input ports.UntrackedScopeInput, key string) (*ports.UntrackedScopeReceipt, error) {
	f.track(actor, id)
	f.saves++
	f.input, f.key = input, key
	return f.receipt, f.err
}
func (f *untrackedScopeFake) Receipt(_ context.Context, actor *ports.Actor, id uuid.UUID, key string) (*ports.UntrackedScopeReceipt, error) {
	f.track(actor, id)
	f.key = key
	return f.receipt, f.err
}
func untrackedScopeAdmin() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "22222222-2222-4222-8222-222222222222", Role: ports.RoleAdmin}
}
func untrackedScopeFixture() (*ServerHandler, *untrackedScopeFake) {
	s, f := &ServerHandler{}, &untrackedScopeFake{}
	s.SetUntrackedScopeService(f)
	return s, f
}
func untrackedScopeRouter(s *ServerHandler, actor *ports.Actor) http.Handler {
	// Main owns this production middleware and its registration in NewRouter.
	badRequest := func(w http.ResponseWriter, r *http.Request, _ error) {
		WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
	}
	strict := NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{RequestErrorHandlerFunc: badRequest})
	h := HandlerWithOptions(strict, StdHTTPServerOptions{ErrorHandlerFunc: badRequest, Middlewares: []MiddlewareFunc{strictUntrackedScopeJSON}})
	return untrackedScopeNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		h.ServeHTTP(w, r)
	}))
}
func untrackedScopeCall(h http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func untrackedScopeStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	if status != 200 && !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal("expected problem response", w.Header())
	}
}

func TestUntrackedScopeRoundTrip(t *testing.T) {
	s, f := untrackedScopeFixture()
	h := untrackedScopeRouter(s, untrackedScopeAdmin())
	w := untrackedScopeCall(h, "GET", untrackedScopePath, "")
	untrackedScopeStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal(w.Body.String())
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := ports.UntrackedScope{
		ID: uuid.MustParse(untrackedScopeID), OwnerID: uuid.MustParse(untrackedScopeAdmin().ID), Enabled: true, Version: 1,
		ClientID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), ClientVersion: now, ClientStamp: strings.Repeat("a", 64),
		RemotePrefix: "/downloads", LocalRoot: "/configured/downloads", SourceIdentity: ports.FileIdentity{Device: 11, Inode: 12, Size: 9191, Mtime: 9292},
		TargetRootID: uuid.MustParse("44444444-4444-4444-8444-444444444444"), TargetRootPath: "/configured/movies", TargetRootGeneration: 1,
		TargetMediaType: "movie", TargetMarker: "library-marker", TargetBaselineScanID: uuid.MustParse("55555555-5555-4555-8555-555555555555"),
		TargetIdentity: ports.FileIdentity{Device: 21, Inode: 22, Size: 9393, Mtime: 9494}, CreatedAt: now, UpdatedAt: now,
	}
	f.config = &c
	// Even a faulty service cannot turn configuration into execution authority.
	f.receipt = &ports.UntrackedScopeReceipt{CommandKey: "key:1_.-", Scope: c, ExecutionAllowed: true}
	w = untrackedScopeCall(h, "PUT", untrackedScopePath, untrackedScopeBody, "key:1_.-")
	untrackedScopeStatus(t, w, 200)
	original := w.Body.String()
	var expected ports.UntrackedScopeInput
	if err := json.Unmarshal([]byte(untrackedScopeBody), &expected); err != nil {
		t.Fatal(err)
	}
	if f.saves != 1 || f.id != c.ID || f.key != "key:1_.-" || !reflect.DeepEqual(f.input, expected) || !reflect.DeepEqual(f.actor, *untrackedScopeAdmin()) {
		t.Fatalf("incorrect input mapping: %+v", f)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || string(response["execution_allowed"]) != "false" || string(response["command_key"]) != `"key:1_.-"` {
		t.Fatal(original)
	}
	var scope map[string]json.RawMessage
	if err := json.Unmarshal(response["scope"], &scope); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"id", "owner_id", "enabled", "version", "client_id", "client_version", "client_stamp", "remote_prefix", "local_root", "source_identity", "target_root_id", "target_root_path", "target_root_generation", "target_media_type", "target_marker", "target_baseline_scan_id", "target_identity", "created_at", "updated_at"}
	if len(scope) != len(wantFields) {
		t.Fatal("unexpected scope fields", original)
	}
	for _, field := range wantFields {
		if _, ok := scope[field]; !ok {
			t.Fatal("missing scope field", field)
		}
	}
	for field, want := range map[string]map[string]int64{"source_identity": {"device": 11, "inode": 12}, "target_identity": {"device": 21, "inode": 22}} {
		var identity map[string]int64
		if json.Unmarshal(scope[field], &identity) != nil || !reflect.DeepEqual(identity, want) {
			t.Fatal("identity leaked dynamic attributes", field, string(scope[field]))
		}
	}
	var got UntrackedScope
	if json.Unmarshal(response["scope"], &got) != nil || !reflect.DeepEqual(got, untrackedScopeResponse(c)) {
		t.Fatal("configuration mapping mismatch", original)
	}
	current := c
	current.Version = 2
	f.config = &current
	w = untrackedScopeCall(h, "GET", untrackedScopePath+"/commands/key:1_.-", "")
	untrackedScopeStatus(t, w, 200)
	if w.Body.String() != original || f.saves != 1 {
		t.Fatal("receipt must retain original scope without Save", w.Body.String())
	}
	w = untrackedScopeCall(h, "GET", untrackedScopePath, "")
	untrackedScopeStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"version":2`) || f.saves != 1 {
		t.Fatal(w.Body.String())
	}
}

func TestUntrackedScopeSubjects(t *testing.T) {
	for _, tt := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: untrackedScopeAdmin().ID, Role: ports.RoleMember}, 403},
		{"token", &ports.Actor{Type: "api_token", ID: untrackedScopeAdmin().ID, Role: ports.RoleAdmin}, 403},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: untrackedScopeAdmin().ID, Role: ports.RoleAdmin}, 403},
		{"empty-scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: untrackedScopeAdmin().ID, Role: ports.RoleAdmin, Scopes: []string{}}, 403},
		{"scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: untrackedScopeAdmin().ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}, 403},
		{"invalid-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin}, 403},
		{"nil-id", &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleAdmin}, 403},
		{"compact-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "22222222222242228222222222222222", Role: ports.RoleAdmin}, 403},
		{"uppercase-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", Role: ports.RoleAdmin}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f := untrackedScopeFixture()
			h := untrackedScopeRouter(s, tt.actor)
			untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath, ""), tt.status)
			untrackedScopeStatus(t, untrackedScopeCall(h, "PUT", untrackedScopePath, untrackedScopeBody, "key"), tt.status)
			untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+"/commands/key", ""), tt.status)
			if f.calls != 0 || f.saves != 0 {
				t.Fatal("forbidden service call", f.calls, f.saves)
			}
		})
	}
}

func TestUntrackedScopeStrictZeroSave(t *testing.T) {
	bodies := map[string]string{"empty": "", "null": "null", "array": "[]", "object": "{}", "second-object": untrackedScopeBody + "{}", "second-null": untrackedScopeBody + "null", "junk": untrackedScopeBody + "junk", "oversized": untrackedScopeBody + strings.Repeat(" ", 4096)}
	for _, field := range []string{"root_path", "RootPath", "source_identity", "SourceIdentity", "scope_owner", "ScopeOwner", "owner_id", "execution_allowed", "ExecutionAllowed", "RootID", "root_id", "targetRootID", "local_root", "client_stamp", "password", "url", "ciphertext"} {
		bodies["unknown-"+field] = strings.TrimSuffix(untrackedScopeBody, "}") + fmt.Sprintf(`,%q:"private-secret"}`, field)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(untrackedScopeBody), &fields); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		missing := make(map[string]json.RawMessage)
		for key, item := range fields {
			if key != name {
				missing[key] = item
			}
		}
		raw, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		bodies["missing-"+name] = string(raw)
		bodies["duplicate-"+name] = strings.TrimSuffix(untrackedScopeBody, "}") + fmt.Sprintf(`,%q:%s}`, name, value)
		bodies["uppercase-"+name] = strings.Replace(untrackedScopeBody, name, strings.ToUpper(name), 1)
		for _, invalid := range []string{"null", "[]", "{}"} {
			bodies[name+"-"+invalid] = strings.Replace(untrackedScopeBody, fmt.Sprintf(`%q:%s`, name, value), fmt.Sprintf(`%q:%s`, name, invalid), 1)
		}
	}
	for _, pair := range [][2]string{
		{`"expected_version":0`, `"expected_version":-1`}, {`"expected_version":0`, `"expected_version":0.0`},
		{`"expected_root_generation":1`, `"expected_root_generation":0`}, {`"expected_root_generation":1`, `"expected_root_generation":1e0`},
		{`false`, `"false"`}, {`2026-01-01T00:00:00Z`, `not-a-date`},
		{`33333333-3333-4333-8333-333333333333`, `00000000-0000-0000-0000-000000000000`},
		{`44444444-4444-4444-8444-444444444444`, `00000000-0000-0000-0000-000000000000`},
	} {
		bodies[pair[1]] = strings.Replace(untrackedScopeBody, pair[0], pair[1], 1)
	}
	for _, prefix := range []string{"", "relative", "/a/../b", "/a/./b", "//downloads", "/downloads/", "/a\\b", "/a\x00b", "/a\nb"} {
		raw, err := json.Marshal(prefix)
		if err != nil {
			t.Fatal(err)
		}
		bodies["prefix-"+prefix] = strings.Replace(untrackedScopeBody, `"/downloads"`, string(raw), 1)
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			s, f := untrackedScopeFixture()
			w := untrackedScopeCall(untrackedScopeRouter(s, untrackedScopeAdmin()), "PUT", untrackedScopePath, body, "key")
			untrackedScopeStatus(t, w, 400)
			if f.saves != 0 || f.calls != 0 || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatal("invalid request reached service or leaked input", f.saves, f.calls, w.Body.String())
			}
		})
	}
}

func TestUntrackedScopeInvalidTransportZeroSave(t *testing.T) {
	s, f := untrackedScopeFixture()
	h := untrackedScopeRouter(s, untrackedScopeAdmin())
	for _, keys := range [][]string{nil, {""}, {"a,b"}, {"a b"}, {"é"}, {strings.Repeat("a", 161)}, {"one", "two"}} {
		untrackedScopeStatus(t, untrackedScopeCall(h, "PUT", untrackedScopePath, untrackedScopeBody, keys...), 400)
		if f.saves != 0 {
			t.Fatal("invalid command key reached Save")
		}
	}
	for _, id := range []string{"bad", uuid.Nil.String(), "11111111111141118111111111111111", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		untrackedScopeStatus(t, untrackedScopeCall(h, "PUT", strings.Replace(untrackedScopePath, untrackedScopeID, id, 1), untrackedScopeBody, "key"), 400)
		if f.saves != 0 {
			t.Fatal("invalid scope id reached Save")
		}
	}
	for _, suffix := range []string{"?", "?unexpected=1"} {
		untrackedScopeStatus(t, untrackedScopeCall(h, "PUT", untrackedScopePath+suffix, untrackedScopeBody, "key"), 400)
		if f.saves != 0 {
			t.Fatal("unexpected query reached Save")
		}
	}
	for _, tail := range []string{"", "/commands/key"} {
		untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+tail, "{}"), 400)
		untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+tail, "", "extra"), 400)
		untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+tail+"?unexpected=1", ""), 400)
	}
	untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+"/commands/a%20b", ""), 400)
	if f.calls != 0 || f.saves != 0 {
		t.Fatal("invalid transport reached service", f.calls, f.saves)
	}
}

func TestUntrackedScopeSafeErrors(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{
		{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrRejected, 400},
		{ports.ErrNotFound, 404}, {ports.ErrEndpointVersionConflict, 409}, {ports.ErrUnavailable, 503},
		{context.Canceled, 503}, {context.DeadlineExceeded, 503},
		{fmt.Errorf("private-secret: %w", context.Canceled), 503},
		{errors.New("https://private Cookie=secret postgres://password ciphertext"), 503},
	} {
		s, f := untrackedScopeFixture()
		f.err = tt.err
		h := untrackedScopeRouter(s, untrackedScopeAdmin())
		for _, w := range []*httptest.ResponseRecorder{
			untrackedScopeCall(h, "GET", untrackedScopePath, ""),
			untrackedScopeCall(h, "PUT", untrackedScopePath, untrackedScopeBody, "key"),
			untrackedScopeCall(h, "GET", untrackedScopePath+"/commands/key", ""),
		} {
			untrackedScopeStatus(t, w, tt.status)
			for _, secret := range []string{"private", "secret", "postgres", "password", "ciphertext", "Cookie"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("error detail leaked", w.Body.String())
				}
			}
		}
	}
}

func TestUntrackedScopeMissingServiceAndNilReceipt(t *testing.T) {
	for _, missing := range []bool{true, false} {
		s, f := untrackedScopeFixture()
		if missing {
			s = &ServerHandler{}
		}
		h := untrackedScopeRouter(s, untrackedScopeAdmin())
		untrackedScopeStatus(t, untrackedScopeCall(h, "PUT", untrackedScopePath, untrackedScopeBody, "key"), 503)
		untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath+"/commands/key", ""), 503)
		if missing {
			untrackedScopeStatus(t, untrackedScopeCall(h, "GET", untrackedScopePath, ""), 503)
			if f.calls != 0 || f.saves != 0 {
				t.Fatal("missing dependency called")
			}
		} else if f.saves != 1 {
			t.Fatal("valid save was not dispatched")
		}
	}
}
