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
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const statsConfigSource = "11111111-1111-4111-8111-111111111111"
const statsConfigPath = "/api/v1/indexers/" + statsConfigSource + "/account-stats/config"
const statsConfigBody = `{"expected_account_id":"18446744073709551615","profile":"nexusphp_classic_info_block_v1","enabled":false,"interval_seconds":900,"expected_version":0,"expected_source_version":7}`

type statsConfigRepo struct {
	config       *ports.SiteAccountStatsConfig
	receipt      *ports.SiteAccountStatsConfigReceipt
	err          error
	calls, saves int
	source       uuid.UUID
	actor        ports.Actor
	input        ports.SiteAccountStatsConfigInput
	key          string
}

func (r *statsConfigRepo) track(a *ports.Actor, id uuid.UUID) { r.calls++; r.actor = *a; r.source = id }
func (r *statsConfigRepo) ReadSiteAccountStatsConfig(_ context.Context, a *ports.Actor, id uuid.UUID) (*ports.SiteAccountStatsConfig, error) {
	r.track(a, id)
	return r.config, r.err
}
func (r *statsConfigRepo) SaveSiteAccountStatsConfig(_ context.Context, a *ports.Actor, id uuid.UUID, in ports.SiteAccountStatsConfigInput, key string) (*ports.SiteAccountStatsConfigReceipt, error) {
	r.track(a, id)
	r.saves++
	r.input = in
	r.key = key
	return r.receipt, r.err
}
func (r *statsConfigRepo) ReadSiteAccountStatsConfigReceipt(_ context.Context, a *ports.Actor, id uuid.UUID, key string) (*ports.SiteAccountStatsConfigReceipt, error) {
	r.track(a, id)
	r.key = key
	return r.receipt, r.err
}
func statsConfigAdmin() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "22222222-2222-4222-8222-222222222222", Role: ports.RoleAdmin}
}
func statsConfigRouter(s *ServerHandler, a *ports.Actor) http.Handler {
	h := HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{Middlewares: []MiddlewareFunc{strictSiteAccountStatsConfigJSON}})
	return siteAccountStatsConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		h.ServeHTTP(w, r)
	}))
}
func statsConfigCall(h http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func statsConfigFixture() (*ServerHandler, *statsConfigRepo) {
	r := &statsConfigRepo{}
	s := &ServerHandler{}
	s.SetSiteAccountStatsConfigService(site.NewAccountStatsConfigService(r))
	return s, r
}

func TestSiteAccountStatsConfigRoundTrip(t *testing.T) {
	s, repo := statsConfigFixture()
	h := statsConfigRouter(s, statsConfigAdmin())
	w := statsConfigCall(h, "GET", statsConfigPath, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal(w.Code, w.Body.String())
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := ports.SiteAccountStatsConfig{IndexerID: uuid.MustParse(statsConfigSource), ExpectedAccountID: "18446744073709551615", Profile: "nexusphp_classic_info_block_v1", Enabled: false, IntervalSeconds: 900, Version: 1, SourceConfigVersion: 7, CreatedAt: now, UpdatedAt: now}
	repo.config = &c
	repo.receipt = &ports.SiteAccountStatsConfigReceipt{CommandKey: "key:1_.-", Config: c}
	w = statsConfigCall(h, "PUT", statsConfigPath, statsConfigBody, "key:1_.-")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	original := w.Body.String()
	var got ports.SiteAccountStatsConfigReceipt
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, *repo.receipt) || repo.input.ExpectedAccountID != c.ExpectedAccountID || repo.input.ExpectedSourceVersion != 7 || repo.source != c.IndexerID || !reflect.DeepEqual(repo.actor, *statsConfigAdmin()) {
		t.Fatal(original, repo)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &fields)
	if len(fields) != 2 {
		t.Fatal(original)
	}
	var configFields map[string]json.RawMessage
	_ = json.Unmarshal(fields["config"], &configFields)
	if len(configFields) != 9 {
		t.Fatal(original)
	}
	writes := repo.saves
	current := c
	current.Version = 2
	repo.config = &current
	w = statsConfigCall(h, "GET", statsConfigPath+"/commands/key:1_.-", "")
	if w.Code != 200 || w.Body.String() != original || repo.saves != writes {
		t.Fatal(w.Code, w.Body.String())
	}
	w = statsConfigCall(h, "GET", statsConfigPath, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSiteAccountStatsConfigSubjectsAndMissingDependency(t *testing.T) {
	for _, tt := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401}, {"admin", statsConfigAdmin(), 503},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: statsConfigAdmin().ID, Role: ports.RoleMember}, 403},
		{"token", &ports.Actor{Type: "api_token", ID: statsConfigAdmin().ID, Role: ports.RoleAdmin}, 403},
		{"scoped", &ports.Actor{Type: ports.ActorTypeUser, ID: statsConfigAdmin().ID, Role: ports.RoleAdmin, Scopes: []string{}}, 403},
		{"bad-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := statsConfigRouter(&ServerHandler{}, tt.actor)
			for _, req := range []struct {
				method, path, body string
				keys               []string
			}{{"GET", statsConfigPath, "", nil}, {"PUT", statsConfigPath, statsConfigBody, []string{"key"}}, {"GET", statsConfigPath + "/commands/key", "", nil}} {
				w := statsConfigCall(h, req.method, req.path, req.body, req.keys...)
				if w.Code != tt.status || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
	h := statsConfigRouter(&ServerHandler{}, statsConfigAdmin())
	w := statsConfigCall(h, "PUT", statsConfigPath, "{}", "key")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestSiteAccountStatsConfigStrictZeroIO(t *testing.T) {
	s, repo := statsConfigFixture()
	h := statsConfigRouter(s, statsConfigAdmin())
	bodies := []string{"", "null", "[]", "{}", statsConfigBody + "{}", statsConfigBody + "null", statsConfigBody + "junk", statsConfigBody + strings.Repeat(" ", 4096), strings.TrimSuffix(statsConfigBody, "}") + `,"cookie":"secret"}`, strings.TrimSuffix(statsConfigBody, "}") + `,"url":"https://private"}`}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(statsConfigBody), &fields)
	for name, value := range fields {
		for _, bad := range []string{"null", "[]", "{}"} {
			bodies = append(bodies, strings.Replace(statsConfigBody, string(value), bad, 1))
		}
		missing := make(map[string]json.RawMessage, len(fields))
		for key, item := range fields {
			if key != name {
				missing[key] = item
			}
		}
		withoutField, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(withoutField))
		bodies = append(bodies, strings.TrimSuffix(statsConfigBody, "}")+fmt.Sprintf(`,"%s":%s}`, name, value))
		bodies = append(bodies, strings.Replace(statsConfigBody, name, strings.ToUpper(name), 1))
	}
	for _, id := range []string{"0", "01", "+1", "-1", "18446744073709551616", "1e2", "用户", ""} {
		bodies = append(bodies, strings.Replace(statsConfigBody, "18446744073709551615", id, 1))
	}
	for _, pair := range [][2]string{{`:900`, `:899`}, {`:900`, `:86401`}, {`:900`, `:900.0`}, {`:900`, `:"900"`}, {`:0`, `:-1`}, {`:7`, `:0`}, {`false`, `"false"`}, {`"nexusphp_classic_info_block_v1"`, `"unknown"`}} {
		bodies = append(bodies, strings.Replace(statsConfigBody, pair[0], pair[1], 1))
	}
	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			w := statsConfigCall(h, "PUT", statsConfigPath, body, "key")
			if w.Code != 400 || repo.calls != 0 {
				t.Fatal(w.Code, w.Body.String(), repo.calls)
			}
		})
	}
	for _, keys := range [][]string{nil, {""}, {"a,b"}, {"a b"}, {"é"}, {strings.Repeat("a", 161)}, {"one", "two"}} {
		w := statsConfigCall(h, "PUT", statsConfigPath, statsConfigBody, keys...)
		if w.Code != 400 {
			t.Fatal(keys, w.Code)
		}
	}
	for _, req := range []struct {
		method, path, body string
		keys               []string
	}{
		{"GET", statsConfigPath, "x", nil}, {"GET", statsConfigPath + "?x=1", "", nil}, {"GET", statsConfigPath + "?", "", nil}, {"GET", statsConfigPath, "", []string{"extra"}},
		{"GET", statsConfigPath + "/commands/a%20b", "", nil}, {"GET", statsConfigPath + "/commands/key", "", []string{"extra"}},
		{"GET", "/api/v1/indexers/00000000-0000-0000-0000-000000000000/account-stats/config", "", nil},
		{"GET", "/api/v1/indexers/bad/account-stats/config", "", nil},
	} {
		w := statsConfigCall(h, req.method, req.path, req.body, req.keys...)
		if w.Code != 400 {
			t.Fatal(req, w.Code, w.Body.String())
		}
	}
	if repo.calls != 0 {
		t.Fatal("repository called", repo.calls)
	}
}
func TestSiteAccountStatsConfigSafeErrors(t *testing.T) {
	s, repo := statsConfigFixture()
	h := statsConfigRouter(s, statsConfigAdmin())
	for _, tt := range []struct {
		err    error
		status int
	}{{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrRejected, 400}, {ports.ErrNotFound, 404}, {ports.ErrEndpointVersionConflict, 409}, {ports.ErrUnavailable, 503}, {errors.New("https://private Cookie=secret"), 503}, {&pgconn.PgError{Message: "postgres://private secret"}, 503}} {
		repo.err = tt.err
		for _, req := range []struct {
			method, path, body string
			keys               []string
		}{{"GET", statsConfigPath, "", nil}, {"PUT", statsConfigPath, statsConfigBody, []string{"key"}}, {"GET", statsConfigPath + "/commands/key", "", nil}} {
			w := statsConfigCall(h, req.method, req.path, req.body, req.keys...)
			if w.Code != tt.status || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "postgres") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}
