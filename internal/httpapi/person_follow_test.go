package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/google/uuid"
)

type personFollowHTTPRepo struct {
	calls  int
	fail   error
	config ports.PersonFollowConfig
	empty  bool
	last   ports.PersonFollowInput
}

func (r *personFollowHTTPRepo) SavePersonFollow(_ context.Context, a *ports.Actor, id uuid.UUID, in ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
	r.calls++
	r.last = in
	if r.fail != nil {
		return nil, r.fail
	}
	r.config.ID = id
	r.config.OwnerID = uuid.MustParse(a.ID)
	r.config.Version = in.ExpectedVersion + 1
	r.config.Settings = in.Settings.Clone()
	return &ports.PersonFollowReceipt{CommandKey: key, Config: r.config}, nil
}
func (r *personFollowHTTPRepo) ReadPersonFollow(context.Context, *ports.Actor, uuid.UUID) (*ports.PersonFollowConfig, error) {
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	c := r.config
	return &c, nil
}
func (r *personFollowHTTPRepo) ListPersonFollows(context.Context, *ports.Actor) ([]ports.PersonFollowConfig, error) {
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	if r.empty {
		return nil, nil
	}
	return []ports.PersonFollowConfig{r.config}, nil
}
func (r *personFollowHTTPRepo) ReadPersonFollowReceipt(_ context.Context, _ *ports.Actor, key string) (*ports.PersonFollowReceipt, error) {
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	return &ports.PersonFollowReceipt{CommandKey: key, Config: r.config}, nil
}
func personFollowHTTPFixture(t *testing.T) (http.Handler, *personFollowHTTPRepo, *ports.Actor, string) {
	t.Helper()
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	root := uuid.New()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	settings := domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}
	repo := &personFollowHTTPRepo{config: ports.PersonFollowConfig{ID: uuid.New(), OwnerID: uuid.MustParse(actor.ID), Version: 1, CreatedAt: now, UpdatedAt: now, Settings: settings}}
	h := &ServerHandler{}
	h.SetPersonFollowService(personfollow.NewConfigService(repo))
	raw, err := json.Marshal(ports.PersonFollowInput{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	return personFollowNoStore(ProblemResponseMiddleware()(strictPersonFollowJSON(Handler(NewStrictHandler(h, nil))))), repo, actor, string(raw)
}
func personFollowHTTPRequest(t *testing.T, h http.Handler, a *ports.Actor, method, path, body string, status int, modify ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "owned-follow")
	if a != nil {
		request = request.WithContext(context.WithValue(request.Context(), actorKey, a))
	}
	for _, change := range modify {
		change(request)
	}
	result := httptest.NewRecorder()
	h.ServeHTTP(result, request)
	if result.Code != status || result.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status/cache %d %v %s", result.Code, result.Header(), result.Body.String())
	}
	if strings.Contains(result.Body.String(), "owned-private") || strings.Contains(result.Body.String(), "SQLSTATE") {
		t.Fatal("private error leaked")
	}
	return result
}
func TestPersonFollowHTTPMappingAndReadOnly(t *testing.T) {
	h, r, a, body := personFollowHTTPFixture(t)
	path := "/api/v1/person-follows/" + r.config.ID.String()
	response := personFollowHTTPRequest(t, h, a, http.MethodPut, path, body, http.StatusOK)
	var receipt PersonFollowReceipt
	if json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.CommandKey != "owned-follow" || receipt.Config.OwnerId.String() != a.ID || receipt.Config.Version != 1 || r.last.Settings.MinReleaseDate != "2026-10-01" || r.last.Settings.Role != "director" {
		t.Fatal("configuration contract changed")
	}
	personFollowHTTPRequest(t, h, a, http.MethodGet, path, "", http.StatusOK)
	personFollowHTTPRequest(t, h, a, http.MethodGet, "/api/v1/person-follows", "", http.StatusOK)
	personFollowHTTPRequest(t, h, a, http.MethodGet, "/api/v1/person-follows/receipts/owned-follow", "", http.StatusOK)
	if r.calls != 4 {
		t.Fatal("unexpected repository calls", r.calls)
	}
	r.empty = true
	response = personFollowHTTPRequest(t, h, a, http.MethodGet, "/api/v1/person-follows", "", http.StatusOK)
	if strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatal("empty list was not an empty array")
	}
}
func TestPersonFollowHTTPAuthorityBeforeBinding(t *testing.T) {
	h, r, a, _ := personFollowHTTPFixture(t)
	for _, actor := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleMember, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: "not-an-id", Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: "guest"}} {
		status := http.StatusForbidden
		if actor == nil {
			status = http.StatusUnauthorized
		}
		personFollowHTTPRequest(t, h, actor, http.MethodPut, "/api/v1/person-follows/not-a-uuid?unexpected=true", "{broken", status)
	}
	if r.calls != 0 {
		t.Fatal("unauthorized request reached storage")
	}
}
func TestPersonFollowHTTPStrictJSONAndQuery(t *testing.T) {
	h, r, a, body := personFollowHTTPFixture(t)
	path := "/api/v1/person-follows/" + r.config.ID.String()
	cases := map[string]string{
		"trailing": body + body, "root_duplicate": strings.Replace(body, `"expected_version":`, `"expected_version":1,"expected_version":`, 1), "nested_duplicate": strings.Replace(body, `"person_id":`, `"person_id":1,"person_id":`, 1), "root_alias": strings.Replace(body, `"settings":`, `"Settings":`, 1), "nested_alias": strings.Replace(body, `"role":`, `"Role":`, 1), "unknown": strings.Replace(body, `"role":`, `"extra":true,"role":`, 1), "null": strings.Replace(body, `"person_id":525`, `"person_id":null`, 1), "invalid_date": strings.Replace(body, "2026-10-01", "2026-02-30", 1), "bad_interval": strings.Replace(body, `"interval_minutes":1440`, `"interval_minutes":0`, 1), "body_limit": strings.Repeat(" ", 16385), "missing": "{}",
		"binding_alias": strings.Replace(body, `"settings":{`, `"settings":{"bindings":{"Indexer_ids":[]},`, 1), "binding_duplicate": strings.Replace(body, `"settings":{`, `"settings":{"bindings":{"indexer_ids":[],"indexer_ids":[]},`, 1), "binding_null": strings.Replace(body, `"settings":{`, `"settings":{"bindings":null,`, 1), "binding_unknown": strings.Replace(body, `"settings":{`, `"settings":{"bindings":{"indexer_ids":[],"extra":true},`, 1),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) { personFollowHTTPRequest(t, h, a, http.MethodPut, path, bad, http.StatusBadRequest) })
	}
	for _, target := range []string{"/api/v1/person-follows?unknown=1", "/api/v1/person-follows?", "/api/v1/person-follows/receipts/key?x=1"} {
		personFollowHTTPRequest(t, h, a, http.MethodGet, target, "", http.StatusBadRequest)
	}
	personFollowHTTPRequest(t, h, a, http.MethodPut, path, body, http.StatusBadRequest, func(req *http.Request) { req.Header.Add("Idempotency-Key", "duplicate") })
	personFollowHTTPRequest(t, h, a, http.MethodPut, path, body, http.StatusBadRequest, func(req *http.Request) { req.Header.Del("Idempotency-Key") })
	personFollowHTTPRequest(t, h, a, http.MethodPut, path, body, http.StatusBadRequest, func(req *http.Request) { req.Header.Set("Content-Type", "text/plain") })
	if r.calls != 0 {
		t.Fatal("invalid input reached repository", r.calls)
	}
}
func TestPersonFollowHTTPBindingPresenceAndIntegerIdentity(t *testing.T) {
	for _, bindings := range []string{"", `"bindings":{"indexer_ids":[]},`} {
		h, r, a, body := personFollowHTTPFixture(t)
		body = strings.Replace(body, `"settings":{`, `"settings":{`+bindings, 1)
		body = strings.Replace(body, `"person_id":525`, `"person_id":9007199254740993`, 1)
		response := personFollowHTTPRequest(t, h, a, http.MethodPut, "/api/v1/person-follows/"+r.config.ID.String(), body, http.StatusOK)
		if r.last.Settings.PersonID != 9007199254740993 || !strings.Contains(response.Body.String(), `"person_id":9007199254740993`) {
			t.Fatal("identity rounded through floating point")
		}
		if bindings == "" && r.last.Settings.Bindings != nil || bindings != "" && (r.last.Settings.Bindings == nil || r.last.Settings.Bindings.IndexerIDs == nil || len(r.last.Settings.Bindings.IndexerIDs) != 0) {
			t.Fatal("binding omission and empty list collapsed")
		}
	}
}
func TestPersonFollowHTTPErrorClassification(t *testing.T) {
	h, r, a, body := personFollowHTTPFixture(t)
	path := "/api/v1/person-follows/" + r.config.ID.String()
	for _, tc := range []struct {
		err    error
		status int
	}{{ports.ErrForbidden, http.StatusForbidden}, {ports.ErrRejected, http.StatusBadRequest}, {ports.ErrPersonFollowConflict, http.StatusConflict}, {ports.ErrNotFound, http.StatusNotFound}, {ports.ErrUnavailable, http.StatusServiceUnavailable}, {errors.New("owned-private SQLSTATE fixture"), http.StatusServiceUnavailable}} {
		r.fail = tc.err
		personFollowHTTPRequest(t, h, a, http.MethodPut, path, body, tc.status)
	}
	r.fail = ports.ErrNotFound
	personFollowHTTPRequest(t, h, a, http.MethodGet, "/api/v1/person-follows/receipts/missing", "", http.StatusNotFound)
	r.fail = ports.ErrUnavailable
	personFollowHTTPRequest(t, h, a, http.MethodGet, "/api/v1/person-follows", "", http.StatusServiceUnavailable)
	disabled := personFollowNoStore(ProblemResponseMiddleware()(strictPersonFollowJSON(Handler(NewStrictHandler(&ServerHandler{}, nil)))))
	personFollowHTTPRequest(t, disabled, a, http.MethodGet, "/api/v1/person-follows", "", http.StatusServiceUnavailable)
}
