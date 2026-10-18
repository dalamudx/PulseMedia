package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEpisodeSourceHTTPIdentityUnknownRefreshThenCurrentJoin(t *testing.T) {
	ctx := context.Background()
	var mode, calls atomic.Int32
	var late func()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("api_key") != "fixture-source-secret" {
			w.WriteHeader(403)
			return
		}
		if mode.Load() == 3 {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, "fixture-source-secret")
			return
		}
		switch r.URL.Path {
		case "/tv/100/season/1":
			_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":null}]}`)
		case "/tv/100/season/2":
			_, _ = io.WriteString(w, `{"season_number":2,"episodes":[{"id":1002,"season_number":2,"episode_number":1,"air_date":null}]}`)
		case "/tv/100/episode_groups":
			if mode.Load() == 2 {
				_, _ = io.WriteString(w, `{"results":[]}`)
			} else {
				_, _ = io.WriteString(w, `{"results":[{"id":"group1","name":"Fixture","type":2,"episode_count":2,"group_count":1}]}`)
			}
		case "/tv/episode_group/group1":
			if mode.Load() == 4 {
				late()
			}
			season := 2
			if mode.Load() == 1 {
				season = 3
			}
			_, _ = fmt.Fprintf(w, `{"id":"group1","type":2,"episode_count":2,"group_count":1,"groups":[{"id":"part1","name":"One sequence","order":0,"episodes":[{"id":1002,"season_number":%d,"episode_number":1,"order":1},{"id":1001,"season_number":1,"episode_number":1,"order":0}]}]}`, season)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	a, admin, member, _, _, _, router, _ := setupOperationHTTP(t, func(h *httpapi.ServerHandler, a *app.App) {
		refresh := postgres.NewTVRefreshStore(a.DBPool(), idgen.New())
		p := tmdb.NewClient("fixture-source-secret", tmdb.WithBaseURL(provider.URL))
		h.SetTVRefreshService(subscription.NewTVRefreshService(refresh, p))
		h.SetEpisodeSourceService(subscription.NewEpisodeSourceService(postgres.NewEpisodeSourceStore(refresh), p))
	})
	q := a.Queries()
	root, m, unit := uuid.New(), uuid.New(), uuid.New()
	if _, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "tv", Name: "Source"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Source Show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	if _, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: m, UnitKey: "S01E01"}); e != nil {
		t.Fatal(e)
	}
	call := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	created := call(admin, "POST", "/api/v1/subscriptions", "", httpapi.CreateSubscriptionRequest{MediaId: &m, RootId: &root})
	var sub httpapi.SubscriptionDetails
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &sub) != nil {
		t.Fatal(created.Code)
	}
	base := "/api/v1/subscriptions/" + sub.Id.String() + "/episode-groups"
	preview := base + "/preview?group_id=group1"
	if w := call(member, "GET", preview, "", nil); w.Code != 403 || calls.Load() != 0 {
		t.Fatal("member reached source", w.Code, calls.Load())
	}
	read := func() *ports.EpisodeGroupPreview {
		t.Helper()
		w := call(admin, "GET", preview, "", nil)
		var p ports.EpisodeGroupPreview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || w.Header().Get("Cache-Control") != "private, no-store" || bytes.Contains(w.Body.Bytes(), []byte("fixture-source-secret")) {
			t.Fatal(w.Code, w.Body.String())
		}
		return &p
	}
	p := read()
	if p.CanMap || p.CanUseAbsolute || p.HoldReason != "canonical_identity_unknown" || len(p.Rows) != 0 {
		t.Fatal("legacy unit number treated as provider ID", p)
	}
	r := call(admin, "POST", "/api/v1/subscriptions/"+sub.Id.String()+"/metadata/refresh", "source-identity-refresh", ports.TVRefreshCommand{ExpectedVersion: int64(sub.IntentVersion), Seasons: []int{1, 2}})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	p = read()
	if !p.CanMap || !p.CanUseAbsolute || len(p.Rows) != 2 || p.Rows[0].SourceNumber != 1 || p.Rows[0].UnitID != unit || p.Rows[0].UnitKey != "S01E01" || p.Rows[1].UnitKey != "S02E01" {
		t.Fatal("group array rewrote canonical units", p)
	}
	savedHash := p.SourceHash
	again := read()
	if again.SourceHash != savedHash || again.InputStamp != p.InputStamp {
		t.Fatal("unstable observation fingerprints")
	}
	mode.Store(1)
	p = read()
	if p.CanMap || p.HoldReason != "canonical_numbering_conflict" || len(p.Rows) != 0 {
		t.Fatal("same ID conflicting canonical coordinate accepted", p)
	}
	mode.Store(2)
	if w := call(admin, "GET", preview, "", nil); w.Code != 404 {
		t.Fatal("foreign group not distinguished", w.Code)
	}
	mode.Store(3)
	if w := call(admin, "GET", base, "", nil); w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("fixture-source-secret")) {
		t.Fatal("failure mistaken for empty or leaked", w.Code)
	}
	late = func() {
		if _, e := a.DBPool().Exec(ctx, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", m); e != nil {
			t.Error(e)
		}
	}
	mode.Store(4)
	if w := call(admin, "GET", preview, "", nil); w.Code != 409 {
		t.Fatal("late current unit source response accepted", w.Code, w.Body.String())
	}
	var downloads int
	if e := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM download_task").Scan(&downloads); e != nil || downloads != 0 {
		t.Fatal("preview created acquisition", downloads, e)
	}
}
