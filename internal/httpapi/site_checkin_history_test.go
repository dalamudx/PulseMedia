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

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const checkinHistoryPath = "/api/v1/indexers/" + checkinConfigSource + "/checkin/history"

type checkinHistoryRepo struct {
	page   *ports.SiteCheckinHistoryPage
	err    error
	calls  int
	limit  int32
	cursor *ports.SiteCheckinHistoryCursor
	actor  ports.Actor
	source uuid.UUID
}

func (r *checkinHistoryRepo) ReadSiteCheckinHistory(_ context.Context, a *ports.Actor, id uuid.UUID, limit int32, cursor *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
	r.calls++
	r.limit = limit
	r.cursor = cursor
	r.actor = *a
	r.source = id
	return r.page, r.err
}
func checkinHistoryFixture() (*ServerHandler, *checkinHistoryRepo) {
	r := &checkinHistoryRepo{page: &ports.SiteCheckinHistoryPage{}}
	s := &ServerHandler{}
	s.SetSiteCheckinHistoryService(site.NewCheckinHistoryReader(r))
	return s, r
}
func checkinHistoryRouter(s *ServerHandler, a *ports.Actor) http.Handler {
	bad := func(w http.ResponseWriter, r *http.Request, _ error) {
		WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
	}
	h := HandlerWithOptions(NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{RequestErrorHandlerFunc: bad, ResponseErrorHandlerFunc: bad}), StdHTTPServerOptions{ErrorHandlerFunc: bad, Middlewares: []MiddlewareFunc{strictSiteCheckinHistoryJSON}})
	return siteCheckinHistoryNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, a))
		}
		h.ServeHTTP(w, r)
	}))
}
func checkinHistoryDay() ports.SiteCheckinDayView {
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return ports.SiteCheckinDayView{ID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), IndexerID: uuid.MustParse(checkinConfigSource), ExpectedAccountID: "18446744073709551615", Profile: ports.NexusPHP1103CheckinProfile, Timezone: "UTC", CalendarDay: "2026-01-02", DayStart: start, DayEnd: start.Add(24 * time.Hour), State: "prepared", Prepared: true, ConfigVersion: 1, SourceVersion: 2, RestoreAcknowledged: true}
}
func TestSiteCheckinHistoryPersisted(t *testing.T) {
	s, r := checkinHistoryFixture()
	h := checkinHistoryRouter(s, checkinConfigAdmin())
	w := statsConfigCall(h, "GET", checkinHistoryPath, "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"config":null,"next_cursor":null,"rows":[]}` {
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || len(got) != 3 || got["config"] != nil || got["next_cursor"] != nil || !reflect.DeepEqual(got["rows"], []any{}) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if r.limit != 20 || r.calls != 1 {
		t.Fatal(r)
	}
	row := checkinHistoryDay()
	cfg := &ports.SiteCheckinConfig{IndexerID: row.IndexerID, ExpectedAccountID: "42", Profile: row.Profile, Timezone: "UTC", DailyAt: "09:05", Enabled: false, Version: 5, SourceConfigVersion: 8, CreatedAt: row.DayStart, UpdatedAt: row.DayEnd}
	r.page = &ports.SiteCheckinHistoryPage{Config: cfg, Rows: []ports.SiteCheckinDayView{row}, Next: &ports.SiteCheckinHistoryCursor{CalendarDay: row.CalendarDay, DayID: row.ID}}
	w = statsConfigCall(h, "GET", checkinHistoryPath+"?limit=1", "")
	var out struct {
		Config     *ports.SiteCheckinConfig   `json:"config"`
		Rows       []ports.SiteCheckinDayView `json:"rows"`
		NextCursor *string                    `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !reflect.DeepEqual(out.Config, cfg) || len(out.Rows) != 1 || !reflect.DeepEqual(out.Rows[0], row) || out.NextCursor == nil || r.calls != 2 || r.source != row.IndexerID || !reflect.DeepEqual(r.actor, *checkinConfigAdmin()) {
		t.Fatal(w.Code, w.Body.String(), r)
	}
	token := *out.NextCursor
	if strings.Contains(token, "https") || strings.Contains(token, ".") {
		t.Fatal(token)
	}
	r.page = &ports.SiteCheckinHistoryPage{Config: cfg}
	w = statsConfigCall(h, "GET", checkinHistoryPath+"?cursor="+token, "")
	if w.Code != 200 || r.cursor == nil || r.cursor.DayID != row.ID || r.calls != 3 {
		t.Fatal(w.Code, w.Body.String(), r)
	}
	before := r.calls
	other := strings.Replace(checkinHistoryPath, checkinConfigSource, "44444444-4444-4444-8444-444444444444", 1)
	w = statsConfigCall(h, "GET", other+"?cursor="+token, "")
	if w.Code != 400 || r.calls != before {
		t.Fatal(w.Code, r.calls)
	}
	observed := row.DayStart.Add(time.Hour)
	row.ObservedAt = &observed
	row.State = "attended_observed"
	r.page = &ports.SiteCheckinHistoryPage{Config: cfg, Rows: []ports.SiteCheckinDayView{row}}
	w = statsConfigCall(h, "GET", checkinHistoryPath+"?limit=100", "")
	if w.Code != 200 || r.limit != 100 || !strings.Contains(w.Body.String(), `"observed_at":"2026-01-02T01:00:00Z"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSiteCheckinHistoryValidation(t *testing.T) {
	for _, q := range []string{"?", "?limit=0", "?limit=-1", "?limit=101", "?limit=2147483647", "?limit=2147483648", "?limit=01", "?limit=1.0", "?limit=1&limit=2", "?limit=1&%6cimit=2", "?%6cimit=1", "?Limit=1", "?unknown=1", "?cursor=", "?cursor=x&cursor=y", "?cursor=%zz", "?cursor=" + strings.Repeat("x", 513), "?cursor=https%3A%2F%2Fsecret.example", "?cursor=1.2", "?limit=1;cursor=x", "?limit=1&"} {
		t.Run(q, func(t *testing.T) {
			s, r := checkinHistoryFixture()
			w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", checkinHistoryPath+q, "")
			if w.Code != 400 || r.calls != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String(), r.calls)
			}
		})
	}
	for _, body := range []string{"{}", "null", " "} {
		s, r := checkinHistoryFixture()
		w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", checkinHistoryPath, body)
		if w.Code != 400 || r.calls != 0 {
			t.Fatal(w.Code, r.calls)
		}
	}
	for _, keys := range [][]string{{""}, {"key"}, {"one", "two"}} {
		s, r := checkinHistoryFixture()
		w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", checkinHistoryPath, "", keys...)
		if w.Code != 400 || r.calls != 0 {
			t.Fatal(w.Code, r.calls)
		}
	}
	for _, id := range []string{"bad", "00000000-0000-0000-0000-000000000000", "11111111111141118111111111111111", "%31" + checkinConfigSource[1:]} {
		s, r := checkinHistoryFixture()
		w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", strings.Replace(checkinHistoryPath, checkinConfigSource, id, 1), "")
		if w.Code != 400 || r.calls != 0 {
			t.Fatal(w.Code, r.calls)
		}
	}
}
func TestSiteCheckinHistoryFailures(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{{ports.ErrNotFound, 404}, {ports.ErrUnavailable, 503}, {ports.ErrForbidden, 403}, {ports.ErrUnauthorized, 401}, {errors.New("https://secret.example Cookie=secret"), 503}, {&pgconn.PgError{Message: "SELECT secret Cookie=secret", Detail: "https://secret.example"}, 503}} {
		s, r := checkinHistoryFixture()
		r.err = tt.err
		w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", checkinHistoryPath, "")
		if w.Code != tt.status || r.calls != 1 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w.Code, w.Body.String(), r.calls)
		}
	}
	for _, mutate := range []func(*ports.SiteCheckinDayView){func(v *ports.SiteCheckinDayView) { v.ExpectedAccountID = "1.0" }, func(v *ports.SiteCheckinDayView) { v.State = "prepared"; v.Prepared = false }, func(v *ports.SiteCheckinDayView) { v.SafeFailureCode = "https://secret.example" }, func(v *ports.SiteCheckinDayView) { v.CalendarDay = "2026-02-30" }, func(v *ports.SiteCheckinDayView) { v.State = "attended_observed"; at := v.DayEnd; v.ObservedAt = &at }, func(v *ports.SiteCheckinDayView) { v.ConfigVersion = 0 }} {
		s, r := checkinHistoryFixture()
		row := checkinHistoryDay()
		mutate(&row)
		r.page.Rows = []ports.SiteCheckinDayView{row}
		w := statsConfigCall(checkinHistoryRouter(s, checkinConfigAdmin()), "GET", checkinHistoryPath, "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := statsConfigCall(checkinHistoryRouter(&ServerHandler{}, checkinConfigAdmin()), "GET", checkinHistoryPath, "")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestSiteCheckinHistoryAuth(t *testing.T) {
	admin := checkinConfigAdmin()
	member := *admin
	member.Role = ports.RoleMember
	token := *admin
	token.Scopes = []string{"read", "write"}
	system := *admin
	system.Type = ports.ActorTypeSystem
	for _, actor := range []*ports.Actor{nil, &member, &token, &system} {
		s, r := checkinHistoryFixture()
		w := statsConfigCall(checkinHistoryRouter(s, actor), "GET", checkinHistoryPath, "")
		want := 403
		if actor == nil {
			want = 401
		}
		if w.Code != want || r.calls != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(w.Code, r.calls)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, role                      string
		active, token, expired, revoked bool
		status                          int
	}{{"admin", ports.RoleAdmin, true, false, false, false, 200}, {"member", ports.RoleMember, true, false, false, false, 403}, {"token", ports.RoleAdmin, true, true, false, false, 403}, {"inactive", ports.RoleAdmin, false, false, false, false, 401}, {"expired", ports.RoleAdmin, true, false, true, false, 401}, {"revoked", ports.RoleAdmin, true, false, false, true, 401}} {
		t.Run(tt.name, func(t *testing.T) {
			nonce := uuid.NewString()
			secrets := crypto.AuthenticationSecrets{}
			repo := &checkinConfigAuthRepo{hash: secrets.HashToken(nonce), user: ports.User{ID: uuid.New(), Role: tt.role, IsActive: tt.active}, expires: now.Add(time.Hour), token: tt.token}
			if tt.expired {
				repo.expires = now
			}
			if tt.revoked {
				repo.err = ports.ErrAuthenticationNotFound
			}
			svc := auth.NewService(repo, secrets, checkinConfigClock{now}, nil)
			s, r := checkinHistoryFixture()
			h := siteCheckinHistoryNoStore(AuthMiddleware(svc)(checkinHistoryRouter(s, nil)))
			request := httptest.NewRequest("GET", checkinHistoryPath, nil)
			request.Header.Set("Authorization", "Bearer "+nonce)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request)
			wantCalls := 0
			if tt.status == 200 {
				wantCalls = 1
			}
			if w.Code != tt.status || r.calls != wantCalls || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal(w.Code, w.Body.String(), r.calls)
			}
		})
	}
}
