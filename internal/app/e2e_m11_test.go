package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

type m11FanartTransport func(*http.Request) (*http.Response, error)

func (f m11FanartTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFullM11EcosystemIntegration(t *testing.T) {
	ctx := context.Background()

	// Fanart uses an owned TLS service; any request or decoding failure fails this check.
	fanartServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/movies/550" || r.URL.Query().Get("api_key") != "test_fanart_key" {
			t.Error("unexpected native Fanart request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"tmdb_id":"550",
			"hdmovielogo": [{"id":"1","url":"https://assets.fanart.tv/fanart/movies/550/hdmovielogo/logo.png","lang":"zh"}],
			"moviebanner": [{"id":"2","url":"https://assets.fanart.tv/fanart/movies/550/moviebanner/banner.jpg","lang":"en"}],
			"moviedisc": [{"id":"3","url":"https://assets.fanart.tv/fanart/movies/550/moviedisc/disc.png","lang":"en"}]}`)
	}))
	defer fanartServer.Close()
	fanartHTTP := fanartServer.Client()
	fanartTransport := fanartHTTP.Transport
	fanartHTTP.Transport = m11FanartTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "webservice.fanart.tv" {
			t.Error("unexpected Fanart origin")
		}
		request := r.Clone(r.Context())
		request.URL.Host = fanartServer.Listener.Addr().String()
		return fanartTransport.RoundTrip(request)
	})
	fanartClient := fanart.NewClient("test_fanart_key", fanartHTTP)
	art, err := fanartClient.GetMovieArtworks(ctx, 550)
	if err != nil || art == nil || art.ClearLogo != "https://assets.fanart.tv/fanart/movies/550/hdmovielogo/logo.png" || art.Banner == "" || art.DiscArt == "" {
		t.Fatalf("Fanart native compatibility failed: %+v %v", art, err)
	}

	// TVDB uses a login token, and retains TVDB identity instead of relabelling it TMDB.
	var tvdbLogins, tvdbReads atomic.Int32
	tvdbServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v4/login":
			var credentials map[string]string
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&credentials) != nil || len(credentials) != 1 || credentials["apikey"] != "test_tvdb_key" {
				t.Error("incorrect native TVDB login")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tvdbLogins.Add(1)
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"token":"owned-tvdb-token"}}`)
		case "/v4/series/81189/extended":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer owned-tvdb-token" {
				t.Error("incorrect native TVDB read")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tvdbReads.Add(1)
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"id":81189,"name":"Breaking Bad","year":"2008","seasons":[{"id":1,"number":1,"name":"Season 1","type":{"id":1,"type":"official"}}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tvdbServer.Close()
	tvdbHTTP := tvdbServer.Client()
	tvdbTransport := tvdbHTTP.Transport
	tvdbHTTP.Transport = m11FanartTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api4.thetvdb.com" {
			t.Error("unexpected TVDB origin")
		}
		request := r.Clone(r.Context())
		request.URL.Host = tvdbServer.Listener.Addr().String()
		return tvdbTransport.RoundTrip(request)
	})
	tvdbClient := tvdb.NewClient("test_tvdb_key", tvdbHTTP)
	tv, err := tvdbClient.GetSeriesDetails(ctx, 81189)
	if err != nil || tv == nil || tv.TVDBID != 81189 || tv.Title != "Breaking Bad" || len(tv.Seasons) != 1 || tv.Seasons[0].EpisodeCount != nil || tvdbLogins.Load() != 1 || tvdbReads.Load() != 1 {
		t.Fatalf("TVDB native compatibility failed: %+v %v", tv, err)
	}

	// 3. System Audit Log Persistence Verification (PG 18.6 isolated cluster)
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	gen := idgen.New()
	mockClock := clock.NewMock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	auditSvc := audit.NewService(postgres.NewLegacyAuditWriter(pool), gen, mockClock)

	actor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   "admin-uuid-001",
		Role: ports.RoleAdmin,
	}
	entry, err := auditSvc.Record(ctx, actor, "subscription.create", "media", "media-001", map[string]any{
		"title": "Breaking Bad",
	})
	if err != nil || entry == nil || entry.Action != "subscription.create" {
		t.Fatalf("record audit failed: %v", err)
	}

	// 4. rTorrent XML-RPC Downloader Verification
	rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>0.9.8</string></value></param></params></methodResponse>`)
	}))
	defer rtServer.Close()

	rtClient := rtorrent.NewClient(rtServer.URL, rtServer.Client())
	if err := rtClient.Test(ctx); err != nil {
		t.Fatalf("test rtorrent failed: %v", err)
	}

	// 5. Encrypted native Web Push compatibility; no delivery-ledger claim.
	m11WebPushCompatibility(t, ctx)

	// 6. Person Subscription Auto-Tracking Verification
	personMgr := subscription.NewPersonSubscriptionManager(nil, nil)
	ok, err := personMgr.AutoSubscribeNewRelease(ctx, actor, subscription.PersonSubscription{
		ID:         uuid.New(),
		PersonID:   525,
		PersonName: "Christopher Nolan",
		Role:       "director",
	}, &ports.MovieMetadata{
		TMDBID: 27205,
		Title:  "Inception",
		Year:   2010,
	}, uuid.New())

	if ok || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("legacy person prototype reported an uncommitted subscription: %v", err)
	}
}
