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

const checkinConfigSource = "11111111-1111-4111-8111-111111111111"
const checkinConfigPath = "/api/v1/indexers/" + checkinConfigSource + "/checkin/config"
const checkinConfigBody = `{"expected_account_id":"18446744073709551615","profile":"nexusphp_1103_attendance_v1","enabled":false,"timezone":"Asia/Shanghai","daily_at":"09:05","expected_version":0,"expected_source_version":7}`

type checkinConfigRepo struct {
	config       *ports.SiteCheckinConfig
	receipt      *ports.SiteCheckinConfigReceipt
	err          error
	calls, saves int
	source       uuid.UUID
	actor        ports.Actor
	input        ports.SiteCheckinConfigInput
	key          string
}

func (r *checkinConfigRepo) track(a *ports.Actor, id uuid.UUID) {
	r.calls++
	r.actor = *a
	r.source = id
}
func (r *checkinConfigRepo) ReadSiteCheckinConfig(_ context.Context, a *ports.Actor, id uuid.UUID) (*ports.SiteCheckinConfig, error) {
	r.track(a, id)
	return r.config, r.err
}
func (r *checkinConfigRepo) SaveSiteCheckinConfig(_ context.Context, a *ports.Actor, id uuid.UUID, in ports.SiteCheckinConfigInput, key string) (*ports.SiteCheckinConfigReceipt, error) {
	r.track(a, id)
	r.saves++
	r.input = in
	r.key = key
	return r.receipt, r.err
}
func (r *checkinConfigRepo) ReadSiteCheckinConfigReceipt(_ context.Context, a *ports.Actor, id uuid.UUID, key string) (*ports.SiteCheckinConfigReceipt, error) {
	r.track(a, id)
	r.key = key
	return r.receipt, r.err
}
func checkinConfigAdmin() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "22222222-2222-4222-8222-222222222222", Role: ports.RoleAdmin}
}
func checkinConfigRouter(s *ServerHandler, a *ports.Actor) http.Handler {
	h := HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{Middlewares: []MiddlewareFunc{strictSiteCheckinConfigJSON}})
	return siteAccountStatsConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		h.ServeHTTP(w, r)
	}))
}
func checkinConfigCall(h http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func checkinConfigFixture() (*ServerHandler, *checkinConfigRepo) {
	r := &checkinConfigRepo{}
	s := &ServerHandler{}
	s.SetSiteCheckinConfigService(site.NewCheckinConfigService(r))
	return s, r
}

func TestSiteCheckinConfigRoundTrip(t *testing.T) {
	s, repo := checkinConfigFixture()
	h := checkinConfigRouter(s, checkinConfigAdmin())
	w := checkinConfigCall(h, "GET", checkinConfigPath, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || strings.TrimSpace(w.Body.String()) != `{"config":null}` {
		t.Fatal(w.Code, w.Body.String())
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := ports.SiteCheckinConfig{IndexerID: uuid.MustParse(checkinConfigSource), ExpectedAccountID: "18446744073709551615", Profile: "nexusphp_1103_attendance_v1", Enabled: false, Timezone: "Asia/Shanghai", DailyAt: "09:05", Version: 1, SourceConfigVersion: 7, CreatedAt: now, UpdatedAt: now}
	repo.config = &c
	repo.receipt = &ports.SiteCheckinConfigReceipt{CommandKey: "key:1_.-", Config: c}
	w = checkinConfigCall(h, "PUT", checkinConfigPath, checkinConfigBody, "key:1_.-")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	original := w.Body.String()
	var got ports.SiteCheckinConfigReceipt
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, *repo.receipt) || repo.input.ExpectedAccountID != c.ExpectedAccountID || repo.input.ExpectedSourceVersion != 7 || repo.source != c.IndexerID || !reflect.DeepEqual(repo.actor, *checkinConfigAdmin()) {
		t.Fatal(original, repo)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &fields)
	if len(fields) != 2 {
		t.Fatal(original)
	}
	var configFields map[string]json.RawMessage
	_ = json.Unmarshal(fields["config"], &configFields)
	if len(configFields) != 10 {
		t.Fatal(original)
	}
	writes := repo.saves
	current := c
	current.Version = 2
	repo.config = &current
	w = checkinConfigCall(h, "GET", checkinConfigPath+"/commands/key:1_.-", "")
	if w.Code != 200 || w.Body.String() != original || repo.saves != writes {
		t.Fatal(w.Code, w.Body.String())
	}
	w = checkinConfigCall(h, "GET", checkinConfigPath, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSiteCheckinConfigSubjectsAndMissingDependency(t *testing.T) {
	for _, tt := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401}, {"admin", checkinConfigAdmin(), 503},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: checkinConfigAdmin().ID, Role: ports.RoleMember}, 403},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: checkinConfigAdmin().ID, Role: ports.RoleAdmin}, 403},
		{"token", &ports.Actor{Type: "api_token", ID: checkinConfigAdmin().ID, Role: ports.RoleAdmin}, 403},
		{"scoped", &ports.Actor{Type: ports.ActorTypeUser, ID: checkinConfigAdmin().ID, Role: ports.RoleAdmin, Scopes: []string{}}, 403},
		{"scopes-cannot-grant-admin", &ports.Actor{Type: ports.ActorTypeUser, ID: checkinConfigAdmin().ID, Role: ports.RoleMember, Scopes: []string{"read", "write", "admin"}}, 403},
		{"bad-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleAdmin}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, repo := checkinConfigFixture()
			if tt.name == "admin" {
				server = &ServerHandler{}
			}
			h := checkinConfigRouter(server, tt.actor)
			for _, req := range []struct {
				method, path, body string
				keys               []string
			}{{"GET", checkinConfigPath, "", nil}, {"PUT", checkinConfigPath, checkinConfigBody, []string{"key"}}, {"GET", checkinConfigPath + "/commands/key", "", nil}} {
				w := checkinConfigCall(h, req.method, req.path, req.body, req.keys...)
				if w.Code != tt.status || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			if repo.calls != 0 {
				t.Fatal("unauthorized repository I/O", repo.calls)
			}
		})
	}
	h := checkinConfigRouter(&ServerHandler{}, checkinConfigAdmin())
	w := checkinConfigCall(h, "PUT", checkinConfigPath, "{}", "key")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestSiteCheckinConfigStrictZeroIO(t *testing.T) {
	s, repo := checkinConfigFixture()
	h := checkinConfigRouter(s, checkinConfigAdmin())
	bodies := map[string]string{"empty": "", "null": "null", "array": "[]", "object": "{}", "second-object": checkinConfigBody + "{}", "second-null": checkinConfigBody + "null", "trailing-junk": checkinConfigBody + "junk", "oversized": checkinConfigBody + strings.Repeat(" ", 4096), "cookie": strings.TrimSuffix(checkinConfigBody, "}") + `,"cookie":"secret"}`, "url": strings.TrimSuffix(checkinConfigBody, "}") + `,"url":"https://private"}`}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(checkinConfigBody), &fields)
	for name, value := range fields {
		for _, bad := range []string{"null", "[]", "{}"} {
			invalid := make(map[string]json.RawMessage, len(fields))
			for key, item := range fields {
				invalid[key] = item
			}
			invalid[name] = json.RawMessage(bad)
			raw, err := json.Marshal(invalid)
			if err != nil {
				t.Fatal(err)
			}
			bodies[name+"-"+bad] = string(raw)
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
		bodies["missing-"+name] = string(withoutField)
		bodies["duplicate-"+name] = strings.TrimSuffix(checkinConfigBody, "}") + fmt.Sprintf(`,"%s":%s}`, name, value)
		bodies["uppercase-"+name] = strings.Replace(checkinConfigBody, name, strings.ToUpper(name), 1)
	}
	for _, id := range []string{"0", "01", "+1", "-1", "18446744073709551616", "1e2", "用户", ""} {
		bodies["uid-"+id] = strings.Replace(checkinConfigBody, "18446744073709551615", id, 1)
	}
	for _, pair := range [][2]string{{`:0`, `:-1`}, {`:0`, `:9223372036854775807`}, {`:0`, `:0.0`}, {`:7`, `:0`}, {`:7`, `:9223372036854775808`}, {`:7`, `:7e0`}, {`false`, `"false"`}, {`"nexusphp_1103_attendance_v1"`, `"unknown"`}, {`"09:05"`, `"24:00"`}, {`"09:05"`, `"9:05"`}, {`"09:05"`, `"23:60"`}, {`"Asia/Shanghai"`, `"../UTC"`}, {`"Asia/Shanghai"`, `"Local"`}} {
		bodies[pair[0]+"-"+pair[1]] = strings.Replace(checkinConfigBody, pair[0], pair[1], 1)
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			w := checkinConfigCall(h, "PUT", checkinConfigPath, body, "key")
			if w.Code != 400 || w.Header().Get("Cache-Control") != "private, no-store" || repo.calls != 0 {
				t.Fatal(w.Code, w.Body.String(), repo.calls)
			}
		})
	}
	for _, keys := range [][]string{nil, {""}, {"a,b"}, {"a b"}, {"é"}, {strings.Repeat("a", 161)}, {"one", "two"}} {
		w := checkinConfigCall(h, "PUT", checkinConfigPath, checkinConfigBody, keys...)
		if w.Code != 400 {
			t.Fatal(keys, w.Code)
		}
	}
	for _, req := range []struct {
		method, path, body string
		keys               []string
	}{
		{"GET", checkinConfigPath, "x", nil}, {"GET", checkinConfigPath + "?x=1", "", nil}, {"GET", checkinConfigPath + "?", "", nil}, {"GET", checkinConfigPath, "", []string{"extra"}},
		{"GET", checkinConfigPath + "/commands/key", "{}", nil}, {"GET", checkinConfigPath + "/commands/key?extra=1", "", nil},
		{"GET", checkinConfigPath + "/commands/a%20b", "", nil}, {"GET", checkinConfigPath + "/commands/key", "", []string{"extra"}},
		{"GET", "/api/v1/indexers/00000000-0000-0000-0000-000000000000/checkin/config", "", nil},
		{"GET", "/api/v1/indexers/bad/checkin/config", "", nil},
		{"GET", "/api/v1/indexers/11111111111141118111111111111111/checkin/config", "", nil},
		{"GET", "/api/v1/indexers/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA/checkin/config", "", nil},
	} {
		w := checkinConfigCall(h, req.method, req.path, req.body, req.keys...)
		if w.Code != 400 {
			t.Fatal(req, w.Code, w.Body.String())
		}
	}
	if repo.calls != 0 {
		t.Fatal("repository called", repo.calls)
	}
}
func TestSiteCheckinConfigSafeErrors(t *testing.T) {
	s, repo := checkinConfigFixture()
	h := checkinConfigRouter(s, checkinConfigAdmin())
	for _, tt := range []struct {
		err    error
		status int
	}{{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrRejected, 400}, {ports.ErrNotFound, 404}, {ports.ErrEndpointVersionConflict, 409}, {ports.ErrUnavailable, 503}, {errors.New("https://private Cookie=secret"), 503}, {&pgconn.PgError{Message: "postgres://private secret"}, 503}} {
		repo.err = tt.err
		for _, req := range []struct {
			method, path, body string
			keys               []string
		}{{"GET", checkinConfigPath, "", nil}, {"PUT", checkinConfigPath, checkinConfigBody, []string{"key"}}, {"GET", checkinConfigPath + "/commands/key", "", nil}} {
			w := checkinConfigCall(h, req.method, req.path, req.body, req.keys...)
			if w.Code != tt.status || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "postgres") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestSiteCheckinConfigUnknownZoneRepositoryRejection(t *testing.T) {
	s, repo := checkinConfigFixture()
	repo.err = ports.ErrRejected
	w := checkinConfigCall(checkinConfigRouter(s, checkinConfigAdmin()), "PUT", checkinConfigPath, strings.Replace(checkinConfigBody, "Asia/Shanghai", "Madeup/IANA", 1), "key")
	if w.Code != 400 || repo.calls != 1 || repo.input.Timezone != "Madeup/IANA" {
		t.Fatal(w.Code, w.Body.String(), repo.calls)
	}
}

func TestSiteCheckinConfigAcceptedIntentBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, zone, daily, version, source string
		enabled                            bool
	}{
		{"create-midnight", "UTC", "00:00", "0", "1", false},
		{"max-versions-late-day", "Etc/GMT+8", "23:59", "9223372036854775806", "9223372036854775807", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, repo := checkinConfigFixture()
			repo.err = ports.ErrUnavailable
			body := fmt.Sprintf(`{"expected_account_id":"18446744073709551615","profile":"nexusphp_1103_attendance_v1","timezone":%q,"daily_at":%q,"enabled":%t,"expected_version":%s,"expected_source_version":%s}`, tt.zone, tt.daily, tt.enabled, tt.version, tt.source)
			key := strings.Repeat("a", 160)
			w := checkinConfigCall(checkinConfigRouter(s, checkinConfigAdmin()), "PUT", checkinConfigPath, body, key)
			if w.Code != 503 || repo.calls != 1 || repo.saves != 1 || repo.key != key || repo.input.Enabled != tt.enabled || repo.input.Timezone != tt.zone || repo.input.DailyAt != tt.daily {
				t.Fatal(w.Code, w.Body.String(), repo)
			}
		})
	}
}
