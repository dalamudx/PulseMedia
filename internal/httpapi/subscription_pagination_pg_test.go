package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionPagesAndCalendarAtScalePG(t *testing.T) {
	a, adminToken, memberToken, _, _, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	member, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(memberToken))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(adminToken))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := a.DBPool().Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	root := uuid.New()
	exec("INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,'tv','Page TV',true)", root, t.TempDir())
	exec("INSERT INTO media(id,title,media_type) SELECT gen_random_uuid(),'Page TV '||n,'tv' FROM generate_series(1,1000)n")
	exec("INSERT INTO media_identity(id,media_id,namespace,external_id) SELECT gen_random_uuid(),id,'tmdb.tv',split_part(title,' ',3) FROM media WHERE title LIKE 'Page TV %'")
	exec("INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision) SELECT gen_random_uuid(),$1,id,$2,'default',1 FROM media WHERE title LIKE 'Page TV %'", member.UserID, root)
	exec("INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,air_date) SELECT gen_random_uuid(),m.id,'S01E'||lpad(n::text,3,'0'),1,n,CASE WHEN n%2=0 THEN NULL ELSE date '2026-01-01'+(n-1) END FROM media m CROSS JOIN generate_series(1,100)n WHERE m.title LIKE 'Page TV %'")
	exec("INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version) SELECT gen_random_uuid(),s.id,u.id,s.intent_version FROM subscription s JOIN media_unit u ON u.media_id=s.media_id WHERE s.root_id=$1", root)
	exec("ANALYZE")
	read := func(token, path string, code int) httpapi.SubscriptionListResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		start := time.Now()
		router.ServeHTTP(rec, req)
		if rec.Code != code || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s status=%d cache=%q body=%s", path, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		var body httpapi.SubscriptionListResponse
		if code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Body.Len() > 160000 {
				t.Fatalf("unbounded response: %d", rec.Body.Len())
			}
			t.Logf("%s rows=%d total=%d bytes=%d time=%s", path, len(body.Items), body.Total, rec.Body.Len(), time.Since(start))
		}
		return body
	}
	first := read(memberToken, "/api/v1/subscriptions", 200)
	if len(first.Items) != 50 || first.Total != 1000 || first.TvCount != 1000 || first.MovieCount != 0 {
		t.Fatal("summary page/count", first.Total, len(first.Items))
	}
	for _, check := range []struct {
		token, path string
		status      int
	}{{memberToken, "/api/v1/subscriptions/" + first.Items[0].Id.String(), 200}, {adminToken, "/api/v1/subscriptions/" + first.Items[0].Id.String(), 200}, {memberToken, "/api/v1/subscriptions/" + uuid.NewString(), 404}, {"", "/api/v1/subscriptions/" + first.Items[0].Id.String(), 401}} {
		req := httptest.NewRequest(http.MethodGet, check.path, nil)
		if check.token != "" {
			req.Header.Set("Authorization", "Bearer "+check.token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != check.status || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("detail cache contract", rec.Code, rec.Header())
		}
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range first.Items {
		seen[item.Id] = true
		if item.WantedItems != nil || item.TotalEpisodes == nil || *item.TotalEpisodes != 100 || item.RootPath != nil {
			t.Fatal("summary privacy/progress", item)
		}
	}
	second := read(memberToken, "/api/v1/subscriptions?offset=50", 200)
	for _, item := range second.Items {
		if seen[item.Id] {
			t.Fatal("overlapping stable pages")
		}
	}
	tail := read(memberToken, "/api/v1/subscriptions?offset=999", 200)
	if len(tail.Items) != 1 || tail.Total != 1000 {
		t.Fatal("tail", tail)
	}
	empty := read(memberToken, "/api/v1/subscriptions?offset=1000", 200)
	if len(empty.Items) != 0 || empty.Total != 1000 {
		t.Fatal("empty page lost total")
	}
	match := read(memberToken, "/api/v1/subscriptions?tmdb_id=900&media_type=tv&own_only=true", 200)
	if len(match.Items) != 1 || match.Total != 1 || *match.Items[0].Title != "Page TV 900" {
		t.Fatal("identity beyond first page", match)
	}
	read(memberToken, "/api/v1/subscriptions?search=Page%20TV%20900", 200)
	own := read(adminToken, "/api/v1/subscriptions?own_only=true", 200)
	if own.Total != 0 {
		t.Fatal("admin own-only includes other users")
	}
	movies := read(memberToken, "/api/v1/subscriptions?media_type=movie", 200)
	if movies.Total != 0 || movies.TvCount != 1000 {
		t.Fatal("filter counts")
	}
	// Exercise prepared/generic-plan reuse, including deep and empty pages.
	var samples []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		read(memberToken, fmt.Sprintf("/api/v1/subscriptions?offset=%d", []int{0, 50, 999, 1000}[i%4]), 200)
		samples = append(samples, time.Since(start))
	}
	slices.Sort(samples)
	t.Logf("SUBSCRIPTION_SCALE_P95 samples=20 subscriptions=1000 wanted=100000 p95=%s", samples[18])
	if samples[18] > 500*time.Millisecond {
		t.Fatalf("deep-page performance regression p95=%s", samples[18])
	}
	for _, query := range []string{"limit=0", "limit=101", "offset=-1", "offset=1000001", "limit=1&limit=2", "unexpected=1", "view=wrong", "media_type=bad", "tmdb_id=0", "view=calendar", "view=calendar&start=2026-01-01&end=2026-03-01", "start=2026-01-01", "unknown_dates=true"} {
		read(memberToken, "/api/v1/subscriptions?"+query, 400)
	}
	read("", "/api/v1/subscriptions", 401)
	calendar := read(memberToken, "/api/v1/subscriptions?view=calendar&start=2026-01-01&end=2026-01-07&limit=100", 200)
	count := 0
	wanted := map[uuid.UUID]bool{}
	for _, item := range calendar.Items {
		if item.WantedItems == nil {
			t.Fatal("missing calendar units")
		}
		for _, w := range *item.WantedItems {
			count++
			wanted[w.Id] = true
			if w.AirDate == nil || w.AirDate.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || w.AirDate.After(time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)) {
				t.Fatal("invented/out-of-range date")
			}
		}
	}
	if count != 100 || calendar.Total != 4000 {
		t.Fatal("calendar bounds", count, calendar.Total)
	}
	next := read(memberToken, "/api/v1/subscriptions?view=calendar&start=2026-01-01&end=2026-01-07&limit=100&offset=100", 200)
	for _, item := range next.Items {
		for _, w := range *item.WantedItems {
			if wanted[w.Id] {
				t.Fatal("calendar overlap")
			}
		}
	}
	unknown := read(memberToken, "/api/v1/subscriptions?view=calendar&start=2026-01-01&end=2026-01-07&unknown_dates=true", 200)
	count = 0
	for _, item := range unknown.Items {
		for _, w := range *item.WantedItems {
			count++
			if w.AirDate != nil {
				t.Fatal("known date mixed with unknown")
			}
		}
	}
	if count != 50 || unknown.Total != 50000 {
		t.Fatal("unknown page", count, unknown.Total)
	}
	// Current account controls both counts and rows, even with a previously administrative actor.
	reader := postgres.NewSubscriptionListStore(a.DBPool())
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.UserID.String(), Role: ports.RoleAdmin}
	exec("UPDATE app_user SET role='member' WHERE id=$1", admin.UserID)
	if page, err := reader.ReadSubscriptionList(ctx, actor, ports.SubscriptionListQuery{Limit: 50}); page != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("stale admin returned data", err)
	}
}

type failingSubscriptionReader struct{ err error }

func (f failingSubscriptionReader) ReadSubscriptionList(context.Context, *ports.Actor, ports.SubscriptionListQuery) (*ports.SubscriptionListPage, error) {
	return nil, f.err
}
func TestSubscriptionListHTTPErrorAndCacheContractPG(t *testing.T) {
	a, token, _, _, _, _, _, _ := setupOperationHTTP(t)
	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	for _, tc := range []struct {
		err  error
		code int
	}{{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrUnavailable, 503}, {fmt.Errorf("private-database-detail"), 503}} {
		wrapped := &subscriptionListApp{App: a, reader: failingSubscriptionReader{tc.err}}
		handler := httpapi.NewServerHandler(wrapped, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		router := httpapi.NewRouter(wrapped, handler, authSvc, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.code || rec.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(rec.Body.String(), "private-database-detail") {
			t.Fatal("error contract", rec.Code, rec.Header(), rec.Body.String())
		}
	}
}
