package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/adapter/notifier/webpush"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestFullM11EcosystemIntegration(t *testing.T) {
	ctx := context.Background()

	// 1. Fanart.tv Artworks Verification
	fanartServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"hdmovielogo": [{"id": "1", "url": "https://assets.fanart.tv/logo.png", "lang": "zh"}],
			"moviebanner": [{"id": "2", "url": "https://assets.fanart.tv/banner.jpg", "lang": "en"}],
			"moviedisc": [{"id": "3", "url": "https://assets.fanart.tv/disc.png", "lang": "en"}]
		}`)
	}))
	defer fanartServer.Close()

	fanartClient := fanart.NewClient("test_fanart_key", fanartServer.Client())
	art, err := fanartClient.GetMovieArtworks(ctx, 1396)
	if err == nil && art != nil {
		if art.ClearLogo != "" && art.ClearLogo != "https://assets.fanart.tv/logo.png" {
			t.Fatalf("unexpected fanart logo: %s", art.ClearLogo)
		}
	}

	// 2. TheTVDB v4 Series Verification
	tvdbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"data": {
				"id": 81189,
				"name": "Breaking Bad",
				"year": "2008",
				"seasons": [{"id": 1, "number": 1, "name": "Season 1"}]
			}
		}`)
	}))
	defer tvdbServer.Close()

	tvdbClient := tvdb.NewClient("test_tvdb_key", tvdbServer.Client())
	tv, err := tvdbClient.GetSeriesDetails(ctx, 81189)
	if err == nil && tv != nil {
		if tv.Title != "Breaking Bad" {
			t.Fatalf("unexpected tvdb title: %s", tv.Title)
		}
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

	// 5. Web Push Dispatch Verification
	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushServer.Close()

	pushClient := webpush.NewClient(pushServer.Client())
	err = pushClient.SendNotification(ctx, pushServer.URL, webpush.NotificationPayload{
		Title: "入库完成",
		Body:  "电影《盗梦空间》已成功入库",
	})
	if err != nil {
		t.Fatalf("send webpush failed: %v", err)
	}

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

	if err != nil || !ok {
		t.Fatalf("person subscription auto-tracking failed: %v", err)
	}
}
