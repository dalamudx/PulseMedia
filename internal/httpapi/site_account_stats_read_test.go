package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

const statsReadPath = "/api/v1/indexers/" + statsConfigSource + "/account-stats"

type statsReadRepo struct {
	view   *ports.SiteAccountStatsView
	err    error
	calls  int
	actor  ports.Actor
	source uuid.UUID
}

func (r *statsReadRepo) ReadSiteAccountStats(_ context.Context, a *ports.Actor, id uuid.UUID) (*ports.SiteAccountStatsView, error) {
	r.calls++
	r.actor = *a
	r.source = id
	return r.view, r.err
}
func (*statsReadRepo) EnqueueDue(context.Context) error     { panic("GET enqueued collection") }
func (*statsReadRepo) RecoverExpired(context.Context) error { panic("GET recovered collection") }
func (*statsReadRepo) Acquire(context.Context, ports.JobLease) (ports.SiteAccountStatsPermit, error) {
	panic("GET acquired source permit")
}

func statsReadRouter(s *ServerHandler, a *ports.Actor) http.Handler {
	bad := func(w http.ResponseWriter, r *http.Request, _ error) {
		WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
	}
	h := HandlerWithOptions(NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{RequestErrorHandlerFunc: bad, ResponseErrorHandlerFunc: bad}), StdHTTPServerOptions{ErrorHandlerFunc: bad, Middlewares: []MiddlewareFunc{strictSiteAccountStatsReadJSON}})
	return siteAccountStatsReadNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		h.ServeHTTP(w, r)
	}))
}
func statsReadFixture() (*ServerHandler, *statsReadRepo) {
	repo := &statsReadRepo{}
	s := &ServerHandler{}
	s.SetSiteAccountStatsService(site.NewAccountStatsReader(repo))
	return s, repo
}
func statsReadView() *ports.SiteAccountStatsView {
	at := time.Date(2022, 3, 4, 5, 6, 7, 123000000, time.FixedZone("historical", 8*3600))
	return &ports.SiteAccountStatsView{
		Config:          ports.SiteAccountStatsConfig{IndexerID: uuid.MustParse(statsConfigSource), ExpectedAccountID: "18446744073709551615", Profile: ports.NexusPHPClassicAccountStatsProfile, Enabled: true, IntervalSeconds: 900, Version: 3, SourceConfigVersion: 9, CreatedAt: at, UpdatedAt: at.Add(time.Hour)},
		CollectionState: "never_collected", Stale: true,
	}
}
func TestSiteAccountStatsReadPersisted(t *testing.T) {
	s, repo := statsReadFixture()
	h := statsReadRouter(s, statsConfigAdmin())
	w := statsConfigCall(h, "GET", statsReadPath, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"observation":null}` {
		t.Fatal(w.Code, w.Body.String())
	}
	repo.view = statsReadView()
	w = statsConfigCall(h, "GET", statsReadPath, "")
	for _, field := range []string{`"stats":null`, `"observed_at":null`, `"last_attempt_at":null`, `"sample_config_version":0`, `"sample_source_version":0`, `"stale":true`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatal(field, w.Code, w.Body.String())
		}
	}
	bonus := "123456789012345678901234567890.0"
	at := repo.view.Config.CreatedAt.Add(-time.Hour)
	attempt := at.Add(30 * time.Minute)
	repo.view.Stats = &ports.SiteAccountStats{AccountID: repo.view.Config.ExpectedAccountID, Uploaded: &indexer.SiteAccountAmount{Value: "123456789012345678901234567890.120", Unit: "TB", Approximate: true}, Downloaded: &indexer.SiteAccountAmount{Value: "12.00", Unit: "GB", Approximate: true}, Ratio: indexer.SiteAccountRatio{State: "finite", Value: "10.000"}, Bonus: &bonus}
	repo.view.ObservedAt = &at
	repo.view.LastAttemptAt = &attempt
	repo.view.SampleConfigVersion = 2
	repo.view.SampleSourceVersion = 7
	repo.view.CollectionState = "failed"
	repo.view.FailureCode = "blocked"
	w = statsConfigCall(h, "GET", statsReadPath, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var decoded struct {
		Observation ports.SiteAccountStatsView `json:"observation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Observation
	if !reflect.DeepEqual(got.Stats, repo.view.Stats) || !got.ObservedAt.Equal(at) || !got.LastAttemptAt.Equal(attempt) || !got.Config.CreatedAt.Equal(repo.view.Config.CreatedAt) || !got.Config.UpdatedAt.Equal(repo.view.Config.UpdatedAt) || !got.Stale || got.SampleConfigVersion != 2 || got.SampleSourceVersion != 7 || got.FailureCode != "blocked" {
		t.Fatal(w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "private, no-store" || repo.calls != 3 || repo.source != repo.view.Config.IndexerID || !reflect.DeepEqual(repo.actor, *statsConfigAdmin()) {
		t.Fatal(w.Header(), repo)
	}
	for _, state := range []string{"unknown", "undefined", "infinite"} {
		repo.view.Stats.Uploaded = nil
		repo.view.Stats.Downloaded = nil
		repo.view.Stats.Bonus = nil
		repo.view.Stats.Ratio = indexer.SiteAccountRatio{State: state}
		w = statsConfigCall(h, "GET", statsReadPath, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"uploaded":null`) || !strings.Contains(w.Body.String(), `"downloaded":null`) || !strings.Contains(w.Body.String(), `"bonus":null`) || strings.Contains(w.Body.String(), `"value"`) {
			t.Fatal(state, w.Code, w.Body.String())
		}
	}
}
func TestSiteAccountStatsReadValidation(t *testing.T) {
	for _, tt := range []struct {
		name, path, body string
		keys             []string
	}{
		{name: "query", path: statsReadPath + "?unknown=1"}, {name: "empty-query", path: statsReadPath + "?"},
		{name: "body", path: statsReadPath, body: `{}`}, {name: "whitespace-body", path: statsReadPath, body: " "},
		{name: "key", path: statsReadPath, keys: []string{"one"}}, {name: "duplicate-key", path: statsReadPath, keys: []string{"one", "two"}},
		{name: "empty-key", path: statsReadPath, keys: []string{""}},
		{name: "invalid-id", path: "/api/v1/indexers/bad/account-stats"},
		{name: "zero-id", path: "/api/v1/indexers/00000000-0000-0000-0000-000000000000/account-stats"},
		{name: "noncanonical-id", path: "/api/v1/indexers/11111111111141118111111111111111/account-stats"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, repo := statsReadFixture()
			w := statsConfigCall(statsReadRouter(s, statsConfigAdmin()), "GET", tt.path, tt.body, tt.keys...)
			if w.Code != 400 || repo.calls != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String(), repo.calls)
			}
			w = statsConfigCall(statsReadRouter(&ServerHandler{}, statsConfigAdmin()), "GET", tt.path, tt.body, tt.keys...)
			if w.Code != 400 {
				t.Fatal("missing service masked validation", w.Code)
			}
		})
	}
	for _, path := range []string{statsConfigPath, statsReadPath + "/other", "/api/v1/indexers/" + statsConfigSource + "/other/account-stats"} {
		if siteAccountStatsReadRoute(path) {
			t.Fatal("hijacked route", path)
		}
	}
}
func TestSiteAccountStatsReadSafeFailures(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{
		{ports.ErrNotFound, 404}, {ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrRejected, 400}, {ports.ErrUnavailable, 503}, {errors.New("https://secret.example/?Cookie=secret"), 503},
	} {
		s, repo := statsReadFixture()
		repo.err = tt.err
		w := statsConfigCall(statsReadRouter(s, statsConfigAdmin()), "GET", statsReadPath, "")
		if w.Code != tt.status || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, corrupt := range []func(*ports.SiteAccountStatsView){
		func(v *ports.SiteAccountStatsView) { v.Config.IndexerID = uuid.New() },
		func(v *ports.SiteAccountStatsView) { v.CollectionState = "https://secret.example/" },
		func(v *ports.SiteAccountStatsView) { v.FailureCode = "Cookie=secret" },
		func(v *ports.SiteAccountStatsView) {
			at := v.Config.CreatedAt
			v.ObservedAt = &at
			v.SampleConfigVersion = 1
			v.SampleSourceVersion = 1
			v.Stats = &ports.SiteAccountStats{AccountID: "12", Ratio: indexer.SiteAccountRatio{State: "unknown"}}
		},
	} {
		s, repo := statsReadFixture()
		repo.view = statsReadView()
		corrupt(repo.view)
		w := statsConfigCall(statsReadRouter(s, statsConfigAdmin()), "GET", statsReadPath, "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := statsConfigCall(statsReadRouter(&ServerHandler{}, statsConfigAdmin()), "GET", statsReadPath, "")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestSiteAccountStatsReadAuthentication(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, role               string
		active, token, anonymous bool
		status                   int
	}{
		{name: "admin", role: ports.RoleAdmin, active: true, status: 200},
		{name: "member", role: ports.RoleMember, active: true, status: 403},
		{name: "admin-token", role: ports.RoleAdmin, active: true, token: true, status: 403},
		{name: "inactive", role: ports.RoleAdmin, status: 401},
		{name: "anonymous", anonymous: true, status: 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nonce := uuid.NewString()
			secrets := crypto.AuthenticationSecrets{}
			ar := &statsConfigAuthRepo{hash: secrets.HashToken(nonce), user: ports.User{ID: uuid.New(), Role: tt.role, IsActive: tt.active}, expires: now.Add(time.Hour), token: tt.token}
			service := auth.NewService(ar, secrets, statsConfigClock{now}, nil)
			s, repo := statsReadFixture()
			h := siteAccountStatsReadNoStore(AuthMiddleware(service)(statsReadRouter(s, nil)))
			r := httptest.NewRequest("GET", statsReadPath, nil)
			if !tt.anonymous {
				r.Header.Set("Authorization", "Bearer "+nonce)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			calls := 0
			if tt.status == 200 {
				calls = 1
			}
			if w.Code != tt.status || repo.calls != calls || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String(), repo.calls)
			}
		})
	}
}
