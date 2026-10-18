package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestAppRunResumesPersistentEndpointPages(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	user, movie, unit, idx := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "paged-runtime-owner", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateMedia(ctx, db.CreateMediaParams{ID: movie, Title: "Movie", Year: pgtype.Int4{Int32: 2020, Valid: true}, MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: movie, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "paged-runtime-secret" || r.URL.Query().Get("q") != "Movie 2020" {
			w.WriteHeader(403)
			return
		}
		offset := 0
		var e error
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, e = strconv.Atoi(raw)
		}
		if e != nil || offset != 0 && offset != 50 {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		if offset == 0 {
			if _, e = f.pool.Exec(r.Context(), "UPDATE indexer SET next_query_at=clock_timestamp()+interval '15 seconds' WHERE id=$1", idx); e != nil {
				w.WriteHeader(503)
				return
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		if _, e = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="%d" total="51"/>`, offset); e != nil {
			return
		}
		count := 50
		if offset > 0 {
			count = 1
		}
		for n := 0; n < count; n++ {
			if _, e = fmt.Fprintf(w, `<item><title>Unrelated.Film.2021.1080p.BluRay.x264-GROUP%d</title><guid>persistent-runtime-%d</guid><enclosure url="https://fixture.invalid/%d?secret=private" length="1024"/></item>`, offset+n, offset+n, offset+n); e != nil {
				return
			}
		}
		_, _ = io.WriteString(w, `</channel></rss>`)
	}))
	defer server.Close()
	encrypted, e := crypto.Encrypt(outboxTestKey, []byte("paged-runtime-secret"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "Persistent pages", Url: server.URL, ApiKeyEncrypted: encrypted, Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	eStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	_, _, e = subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}, subscription.CreateSubscriptionCommand{MediaID: movie, RootID: f.root, ScopeType: "all", IndexerIDs: []uuid.UUID{idx}, DownloaderID: &f.client})
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	start := func() (*App, context.CancelFunc, chan error) {
		pool, e := postgres.NewPool(ctx, cfg.Database.URL, postgres.DefaultPoolConfig())
		if e != nil {
			t.Fatal(e)
		}
		a, e := New(cfg, WithDBPool(pool), WithLogger(logging.NewLogger(io.Discard, "error")))
		if e != nil {
			pool.Close()
			t.Fatal(e)
		}
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		done := make(chan error, 1)
		go func() { done <- a.Run(runCtx) }()
		return a, cancel, done
	}
	stop := func(a *App, cancel context.CancelFunc, done chan error) {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("paged App did not drain")
		}
		if e := a.Close(); e != nil {
			t.Fatal(e)
		}
	}
	a, cancel, done := start()
	running := true
	defer func() {
		if running {
			stop(a, cancel, done)
		}
	}()
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Second)
	defer waitCancel()
	waitOutboxCondition(t, waitCtx, func() bool {
		var n int
		return f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint_page WHERE indexer_id=$1", idx).Scan(&n) == nil && n == 1
	})
	var jobID, roundID uuid.UUID
	var token int64
	var before string
	if e = f.pool.QueryRow(ctx, "SELECT j.id,j.lease_token,e.round_id,(SELECT row_to_json(p)::text FROM search_round_endpoint_page p WHERE p.round_id=e.round_id AND p.indexer_id=e.indexer_id AND p.page=0) FROM job j JOIN search_round_endpoint e ON j.payload->>'round_id'=e.round_id::text AND j.payload->>'indexer_id'=e.indexer_id::text WHERE e.indexer_id=$1 AND j.kind='search_endpoint' AND j.state='queued'", idx).Scan(&jobID, &token, &roundID, &before); e != nil {
		var snapshot string
		_ = f.pool.QueryRow(ctx, `SELECT jsonb_build_object('job',(SELECT jsonb_agg(jsonb_build_object('state',state,'kind',kind,'attempts',attempts)) FROM job WHERE kind='search_endpoint'),'round',(SELECT jsonb_agg(jsonb_build_object('state',state,'deadline',deadline)) FROM search_round),'endpoint',(SELECT jsonb_agg(to_jsonb(e)) FROM search_round_endpoint e),'budget',(SELECT next_query_at FROM indexer WHERE id=$1),'pages',(SELECT jsonb_agg(to_jsonb(p)) FROM indexer_query_page p))::text`, idx).Scan(&snapshot)
		t.Fatal(e, snapshot)
	}
	stop(a, cancel, done)
	running = false
	mu.Lock()
	if len(offsets) != 1 || offsets[0] != 0 {
		t.Fatal("first app replayed/advanced unexpectedly", offsets)
	}
	mu.Unlock()
	// Make the explicitly delayed fixture budget due; normal production intervals stay unchanged.
	if _, e = f.pool.Exec(ctx, "UPDATE indexer SET next_query_at=clock_timestamp() WHERE id=$1", idx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE job SET not_before=clock_timestamp() WHERE id=$1", jobID); e != nil {
		t.Fatal(e)
	}
	a, cancel, done = start()
	running = true
	waitOutboxCondition(t, waitCtx, func() bool {
		var n int
		return f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint_page WHERE round_id=$1 AND indexer_id=$2", roundID, idx).Scan(&n) == nil && n == 2
	})
	stop(a, cancel, done)
	running = false
	var after string
	var saved, links, downloads int
	var newToken int64
	var state string
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT row_to_json(p)::text FROM search_round_endpoint_page p WHERE p.round_id=$1 AND p.indexer_id=$2 AND p.page=0),(SELECT count(*) FROM search_round_endpoint_page WHERE round_id=$1 AND indexer_id=$2),(SELECT count(*) FROM indexer_query_candidate qc JOIN search_round_endpoint_page p ON p.query_id=qc.query_id WHERE p.round_id=$1),(SELECT count(*) FROM job WHERE kind='download_submission'),j.lease_token,j.state FROM job j WHERE j.id=$3", roundID, idx, jobID).Scan(&after, &saved, &links, &downloads, &newToken, &state); e != nil || after != before || saved != 2 || links != 51 || downloads != 0 || newToken <= token || state != "succeeded" {
		t.Fatal("restart page state", saved, links, downloads, newToken, state, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 50 {
		t.Fatal("restart replayed first page", offsets)
	}
}
