package app

import (
	"context"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppRunOwnsPersistentMovieSearch(t *testing.T)      { testMovieSearchRuntime(t, false) }
func TestAppRunOwnsPersistentRSSMovieEvidence(t *testing.T) { testMovieSearchRuntime(t, true) }
func TestAppRunRejectsAmbiguousYearWithMatchingIdentity(t *testing.T) {
	testMovieSearchRuntime(t, false, true)
}
func testMovieSearchRuntime(t *testing.T, rss bool, ambiguousYear ...bool) {
	f := organizationSetup(t)
	ctx := context.Background()
	user := uuid.New()
	if _, err := f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "automatic-owner", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	movie, unit := uuid.New(), uuid.New()
	if _, err := f.q.CreateMedia(ctx, db.CreateMediaParams{ID: movie, Title: "Movie", Year: pgtype.Int4{Int32: 2020, Valid: true}, MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: movie, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	badYear := len(ambiguousYear) > 0 && ambiguousYear[0]
	if badYear {
		if _, e := f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: movie, Namespace: "tmdb.movie", ExternalID: "550"}); e != nil {
			t.Fatal(e)
		}
	}
	var requests, feeds atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "runtime-secret" {
			w.WriteHeader(400)
			return
		}
		keyword := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/xml")
		if rss && keyword == "" {
			feeds.Add(1)
		} else {
			if keyword != "Movie 2020" {
				w.WriteHeader(400)
				return
			}
			requests.Add(1)
			if rss {
				_, _ = io.WriteString(w, `<rss><channel></channel></rss>`)
				return
			}
		}
		title := "Movie.2020.1080p.BluRay"
		if badYear {
			title = "Movie.1984.2020.{tmdb-550}.1080p.BluRay"
		}
		_, _ = io.WriteString(w, `<rss><channel><item><title>`+title+`</title><guid>runtime-result</guid><enclosure url="https://fixture.invalid/torrent?passkey=private" length="1024"/></item></channel></rss>`)
	}))
	defer server.Close()
	encrypted, _ := crypto.Encrypt(outboxTestKey, []byte("runtime-secret"))
	idx := uuid.New()
	if _, err := f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "Runtime", Url: server.URL, ApiKeyEncrypted: encrypted, Categories: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	errStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	sub, wanted, err := subscription.NewService(errStore, errStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), errStore).CreateSubscription(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}, subscription.CreateSubscriptionCommand{MediaID: movie, RootID: f.root, ScopeType: "all", IndexerIDs: []uuid.UUID{idx}, DownloaderID: &f.client})
	if err != nil || len(wanted) != 1 {
		t.Fatal(err, wanted)
	}
	if rss {
		feed := usecaseSearch.NewFeedService(postgres.NewRSSStore(f.pool, f.gen), crypto.NewSecretProtector(outboxTestKey), nil)
		if _, e := feed.Configure(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}, idx, usecaseSearch.FeedCommand{Enabled: true, Kind: "torznab", IntervalSeconds: 30, ExpectedVersion: 1}, "runtime-enable-rss"); e != nil {
			t.Fatal(e)
		}
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, cancel := context.WithTimeout(ctx, 30*time.Second)
	waitWindow := 14 * time.Second
	if rss { // A successful empty directed query may close the first round before RSS ACK.
		// The persisted late receipt wakes a second round; allow its normal budget/
		// consumer ticks, not a permanently running first-window assumption.
		waitWindow = 25 * time.Second
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, waitWindow)
	defer waitCancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(runtime) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not join search workers")
		}
	}()
	defer func() {
		if t.Failed() {
			var snapshot []byte
			err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(jsonb_build_object('kind',kind,'state',state,'attempts',attempts,'error',error,'not_before',not_before)) FROM job WHERE kind IN ('rss_poll','search_endpoint','search_round_finalize')),'rounds',(SELECT jsonb_agg(jsonb_build_object('state',state,'reason',abort_reason,'deadline',deadline,'wanted',wanted_item_id)) FROM search_round),'endpoints',(SELECT jsonb_agg(jsonb_build_object('state',state,'query',query_id)) FROM search_round_endpoint),'indexers',(SELECT jsonb_agg(jsonb_build_object('last_query',rss_last_query_id,'last_success',rss_last_success_at,'budget',next_query_at)) FROM indexer),'wanted',(SELECT jsonb_agg(jsonb_build_object('next',next_evaluate_at,'seen',rss_seen_at,'satisfied',is_satisfied)) FROM wanted_item))`).Scan(&snapshot)
			t.Logf("runtime diagnostic: search=%d feed=%d snapshot=%s err=%v", requests.Load(), feeds.Load(), snapshot, err)
		}
	}()
	if badYear {
		waitOutboxCondition(t, waitCtx, func() bool {
			var n int
			e := f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round WHERE subscription_id=$1 AND state='expired' AND abort_reason='no_qualified_candidate' AND operation_id IS NULL", sub.ID).Scan(&n)
			return e == nil && n == 1
		})
		if requests.Load() != 1 {
			t.Fatal("ambiguous title not obtained via actual HTTP", requests.Load())
		}
		var candidates, downloads, succeeded int
		if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM job WHERE kind='search_round_finalize' AND state='succeeded')").Scan(&candidates, &downloads, &succeeded); e != nil || candidates < 1 || downloads != 0 || succeeded != 1 {
			t.Fatal("ambiguous external ID acquired or did not finalize", candidates, downloads, succeeded, e)
		}
		var reservations int
		if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched')", unit).Scan(&reservations); e != nil || reservations != 0 {
			t.Fatal("ambiguous year granted unit protection", reservations, e)
		}
		firstWanted, e := f.q.GetWantedItemByID(ctx, wanted[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		window := time.NewTimer(8 * time.Second)
		defer window.Stop()
		select {
		case <-window.C:
		case <-runtime.Done():
			t.Fatal("runtime stopped before backoff observation")
		}
		if requests.Load() != 1 {
			t.Fatal("projection invalidation bypassed no-candidate backoff", requests.Load())
		}
		later, e := f.q.GetWantedItemByID(ctx, wanted[0].ID)
		if e != nil || !later.NextEvaluateAt.Time.Equal(firstWanted.NextEvaluateAt.Time) {
			t.Fatal("projection silently slid retry deadline", e)
		}
		// Model the persisted retry becoming due without sleeping five minutes.
		if _, e = f.pool.Exec(ctx, "UPDATE wanted_item SET next_evaluate_at=clock_timestamp()-interval '1 second' WHERE id=$1", wanted[0].ID); e != nil {
			t.Fatal(e)
		}
		retryCtx, retryCancel := context.WithTimeout(ctx, 10*time.Second)
		defer retryCancel()
		waitOutboxCondition(t, retryCtx, func() bool {
			var n int
			e := f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round WHERE subscription_id=$1 AND state='expired' AND abort_reason='no_qualified_candidate' AND operation_id IS NULL", sub.ID).Scan(&n)
			return e == nil && n == 2
		})
		if requests.Load() != 2 {
			t.Fatal("due retry suppressed forever or duplicated", requests.Load())
		}
		var jobs int
		if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 0 {
			t.Fatal("due ambiguous retry acquired", jobs, e)
		}
		return
	}
	waitOutboxCondition(t, waitCtx, func() bool {
		var count int
		err := f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round WHERE subscription_id=$1 AND state='completed' AND operation_id IS NOT NULL", sub.ID).Scan(&count)
		return err == nil && count == 1
	})
	if (!rss && requests.Load() != 1) || (rss && (requests.Load() < 1 || feeds.Load() != 1)) {
		t.Fatal("wrong real network observations", requests.Load(), feeds.Load())
	}
	if rss {
		var n int
		if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='rss_poll' AND state='succeeded'").Scan(&n); e != nil || n != 1 {
			t.Fatal("runtime did not own RSS job", n, e)
		}
	}
	var operation pgtype.UUID
	if err = f.pool.QueryRow(ctx, "SELECT operation_id FROM search_round WHERE subscription_id=$1 AND operation_id IS NOT NULL", sub.ID).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	requirements, err := f.q.ListRequirementsForOperation(ctx, operation.Bytes)
	if err != nil || len(requirements) != 1 || requirements[0].ActorID != user.String() || requirements[0].ActorType != "user" {
		t.Fatal("automatic demand must belong to actual subscription owner", requirements, err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	done <- nil
}
