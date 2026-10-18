package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A single-connection reader makes leaked transactions during poster I/O observable.
type subscriptionListApp struct {
	*app.App
	reader      ports.SubscriptionListReader
	readerCalls int
}

func (a *subscriptionListApp) SubscriptionListReader() ports.SubscriptionListReader {
	a.readerCalls++
	return a.reader
}

type subscriptionListTrace struct {
	mu           sync.Mutex
	queries      []string
	afterEntries func()
}

func (t *subscriptionListTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries = append(t.queries, data.SQL)
	return context.WithValue(ctx, subscriptionListTraceKey{}, data.SQL)
}

type subscriptionListTraceKey struct{}

func (t *subscriptionListTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(subscriptionListTraceKey{}).(string)
	t.mu.Lock()
	hook := t.afterEntries
	if strings.HasPrefix(sql, "-- name: ReadSubscriptionListEntries") {
		t.afterEntries = nil
	} else {
		hook = nil
	}
	t.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (t *subscriptionListTrace) reset() { t.mu.Lock(); defer t.mu.Unlock(); t.queries = nil }
func (t *subscriptionListTrace) recorded() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.queries...)
}

func subscriptionListPool(t *testing.T, a *app.App, trace *subscriptionListTrace) *pgxpool.Pool {
	t.Helper()
	cfg := a.DBPool().Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	trace.reset()
	return pool
}

type subscriptionListMetadata struct {
	mockMetaProvider
	t     *testing.T
	pool  *pgxpool.Pool
	calls map[string]int
	fail  bool
}

func (m *subscriptionListMetadata) poster(ctx context.Context, kind string, id int64, language string) string {
	m.t.Helper()
	if language != "zh-CN" {
		m.t.Fatal("poster language changed", language)
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var ok int
	if err := m.pool.QueryRow(probe, "SELECT 1").Scan(&ok); err != nil || ok != 1 {
		m.t.Fatal("poster called with database snapshot still open", err)
	}
	key := fmt.Sprintf("%s:%d", kind, id)
	m.calls[key]++
	return "/fixtures/" + key + ".jpg"
}
func (m *subscriptionListMetadata) GetMovieDetails(ctx context.Context, id int64, language string) (*ports.MovieMetadata, error) {
	poster := m.poster(ctx, "movie", id, language)
	if m.fail {
		return nil, ports.ErrUnavailable
	}
	return &ports.MovieMetadata{TMDBID: id, PosterURL: poster}, nil
}
func (m *subscriptionListMetadata) GetTVDetails(ctx context.Context, id int64, language string) (*ports.TVDetails, error) {
	poster := m.poster(ctx, "tv", id, language)
	if m.fail {
		return nil, ports.ErrUnavailable
	}
	return &ports.TVDetails{TMDBID: id, PosterURL: poster}, nil
}

func TestSubscriptionListBulkHTTPAndLegacyEquivalence(t *testing.T) {
	a, adminSession, memberSession, rootID, movieUnit, _, _, _ := setupOperationHTTP(t)
	ctx := context.Background()
	q, pool := a.Queries(), a.DBPool()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	member, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(memberSession))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	var movieID uuid.UUID
	if err = pool.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", movieUnit).Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE media SET year=1999 WHERE id=$1", movieID)
	for _, identity := range []db.CreateMediaIdentityParams{{ID: uuid.New(), MediaID: movieID, Namespace: "imdb", ExternalID: "tt-not-a-poster-id"}, {ID: uuid.New(), MediaID: movieID, Namespace: "tmdb.movie", ExternalID: "550"}} {
		if _, err = q.CreateMediaIdentity(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}
	tvID, tvRoot, client, indexer1, indexer2 := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'List Series','tv')", tvID)
	exec("INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','1399')", uuid.New(), tvID)
	exec("INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,'tv','List TV',true)", tvRoot, t.TempDir())
	sealed, err := crypto.Encrypt(make([]byte, 32), []byte("owned-loopback-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'List downloader','qbittorrent','http://127.0.0.1:1','fixture',$2,'[]')", client, sealed)
	for _, id := range []uuid.UUID{indexer1, indexer2} {
		if _, err := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: id, Name: "List indexer", Url: "http://127.0.0.1:1", ApiKeyEncrypted: sealed, Categories: []byte("[]")}); err != nil {
			t.Fatal(err)
		}
	}
	bindings, _ := json.Marshal([]uuid.UUID{indexer2, indexer1})
	create := func(user, media, root uuid.UUID, scope string) uuid.UUID {
		t.Helper()
		sub, err := q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: user, MediaID: media, RootID: root, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: bindings, DownloaderID: pgtype.UUID{Bytes: client, Valid: true}, ScopeType: scope})
		if err != nil {
			t.Fatal(err)
		}
		return sub.ID
	}
	movieSub := create(member.UserID, movieID, rootID, "all")
	tvSub := create(member.UserID, tvID, tvRoot, "selected")
	adminSub := create(admin.UserID, movieID, rootID, "all")
	emptyMedia := uuid.New()
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'Empty intent','movie')", emptyMedia)
	emptySub := create(member.UserID, emptyMedia, rootID, "all")
	wanted := func(sub, unit uuid.UUID, skipped bool) uuid.UUID {
		t.Helper()
		w, err := q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: sub, UnitID: unit, IntentVersion: 1, IsSkipped: skipped, NextEvaluateAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	movieWanted := wanted(movieSub, movieUnit, false)
	allSkipped := wanted(adminSub, movieUnit, true)
	exec("UPDATE wanted_item SET user_skipped=true WHERE id=$1", allSkipped)
	keys := []string{"S00E01", "S01E01", "S01E02", "S02E01", "S02E02"}
	var tvUnits, tvWanted []uuid.UUID
	for i, key := range keys {
		unit := uuid.New()
		exec("INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,$5)", unit, tvID, key, []int{0, 1, 1, 2, 2}[i], []int{1, 1, 2, 1, 2}[i])
		tvUnits = append(tvUnits, unit)
		tvWanted = append(tvWanted, wanted(tvSub, unit, i == 0 || i == 2))
	}
	exec("UPDATE wanted_item SET scope_excluded=true WHERE id=$1", tvWanted[0])
	exec("UPDATE wanted_item SET user_skipped=true WHERE id=$1", tvWanted[2])
	// Real imported facts and the real evaluator establish positive projection evidence.
	for _, unit := range []uuid.UUID{movieUnit, tvUnits[1], tvUnits[3], tvUnits[4]} {
		root := tvRoot
		if unit == movieUnit {
			root = rootID
		}
		file := uuid.New()
		exec("INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,32,1)", file, root, unit.String()+".mkv")
		if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) VALUES($1,$2,'imported',$3,$4,$5)", uuid.New(), unit, file, root, []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`))
	}
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	for _, id := range append([]uuid.UUID{movieWanted, allSkipped}, tvWanted...) {
		if _, err = evaluator.EvaluateWantedItem(ctx, id, quality.DefaultPolicy()); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE wanted_item SET next_evaluate_at=now()-interval '1 second' WHERE id=$1", tvWanted[3])
	exec("UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", tvUnits[4])
	// Distinct creation times make the legacy descending order explicit.
	for i, id := range []uuid.UUID{movieSub, tvSub, adminSub, emptySub} {
		exec("UPDATE subscription SET created_at=now()+$2*interval '1 second' WHERE id=$1", id, i)
	}
	trace := &subscriptionListTrace{}
	readPool := subscriptionListPool(t, a, trace)
	metadata := &subscriptionListMetadata{t: t, pool: readPool, calls: map[string]int{}}
	wrapped := &subscriptionListApp{App: a, reader: postgres.NewSubscriptionListStore(readPool)}
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	handler := httpapi.NewServerHandler(wrapped, authSvc, nil, nil, nil, metadata, nil, nil, nil, nil, nil, nil, nil)
	if wrapped.readerCalls != 1 {
		t.Fatal("list reader must be assembled once by the handler constructor", wrapped.readerCalls)
	}
	router := httpapi.NewRouter(wrapped, handler, authSvc, nil)
	token := func(user uuid.UUID, scopes string) string {
		raw := uuid.NewString()
		exec("INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'list read',$3,$4::jsonb)", uuid.New(), user, crypto.HashToken(raw), scopes)
		return raw
	}
	memberToken := token(member.UserID, `["read","admin","system"]`)
	adminToken := token(admin.UserID, `["read"]`)
	read := func(raw string) httpapi.SubscriptionListResponse {
		t.Helper()
		metadata.calls = map[string]int{}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("list %d %s", rec.Code, rec.Body.String())
		}
		var body httpapi.SubscriptionListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	for _, tc := range []struct {
		raw   string
		admin bool
	}{{adminSession, true}, {adminToken, true}, {memberSession, false}, {memberToken, false}} {
		body := read(tc.raw)
		ids := []uuid.UUID{emptySub, adminSub, tvSub, movieSub}
		if !tc.admin {
			ids = []uuid.UUID{emptySub, tvSub, movieSub}
		}
		if len(body.Items) != len(ids) {
			t.Fatal("list scope", body.Items)
		}
		for i, item := range body.Items {
			if item.Id != ids[i] || item.WantedItems == nil || item.TotalEpisodes == nil || item.SatisfiedEpisodes == nil {
				t.Fatal("order/fields", item)
			}
			legacy, err := q.GetSubscriptionByID(ctx, item.Id)
			if err != nil {
				t.Fatal(err)
			}
			media, err := q.GetMediaByID(ctx, legacy.MediaID)
			if err != nil {
				t.Fatal(err)
			}
			root, err := q.GetLibraryRootByID(ctx, legacy.RootID)
			if err != nil {
				t.Fatal(err)
			}
			if item.UserId != legacy.UserID || item.MediaId != legacy.MediaID || item.RootId != legacy.RootID || item.State != legacy.State || item.IntentVersion != int(legacy.IntentVersion) || item.ScopeType != legacy.ScopeType || item.QualityPolicyId != legacy.QualityPolicyID || item.QualityPolicyRevision != int(legacy.QualityPolicyRevision) || item.Title == nil || *item.Title != media.Title || item.MediaType == nil || *item.MediaType != media.MediaType || item.RootName == nil || *item.RootName != root.Name {
				t.Fatal("base projection changed", item)
			}
			if (item.Year != nil) != media.Year.Valid || (item.Year != nil && *item.Year != int(media.Year.Int32)) {
				t.Fatal("year", item.Year)
			}
			if (item.RootPath != nil) != tc.admin || (item.RootPath != nil && *item.RootPath != root.Path) {
				t.Fatal("root path authority", item.RootPath)
			}
			if item.IndexerIds == nil || !reflect.DeepEqual(*item.IndexerIds, []uuid.UUID{indexer2, indexer1}) || item.DownloaderId == nil || *item.DownloaderId != client {
				t.Fatal("bindings changed", item)
			}
			oldWanted, err := q.ListWantedItemsWithUnitDetails(ctx, item.Id)
			if err != nil {
				t.Fatal(err)
			}
			if len(*item.WantedItems) != len(oldWanted) {
				t.Fatal("wanted list truncated")
			}
			for j, w := range *item.WantedItems {
				old := oldWanted[j]
				if w.Id != old.ID || w.UnitId != old.UnitID || w.UnitKey == nil || *w.UnitKey != old.UnitKey || w.IsSkipped != old.IsSkipped || w.IsSatisfied != old.IsSatisfied || (w.CurrentFactStage != nil) != (old.CurrentFactStage != "") || (w.CurrentFactStage != nil && *w.CurrentFactStage != old.CurrentFactStage) {
					t.Fatal("wanted equivalence", w, old)
				}
			}
			total, satisfied, poster := 0, 0, ""
			switch item.Id {
			case movieSub:
				total, satisfied, poster = 1, 1, "/fixtures/movie:550.jpg"
			case tvSub:
				total, satisfied, poster = 3, 1, "/fixtures/tv:1399.jpg"
			case adminSub:
				total, poster = 1, "/fixtures/movie:550.jpg"
			}
			if *item.TotalEpisodes != total || *item.SatisfiedEpisodes != satisfied || (item.PosterUrl != nil) != (poster != "") || (item.PosterUrl != nil && *item.PosterUrl != poster) {
				t.Fatal("progress/poster", item)
			}
		}
		if !reflect.DeepEqual(metadata.calls, map[string]int{"movie:550": 1, "tv:1399": 1}) {
			t.Fatal("metadata identity/memoization", metadata.calls)
		}
		if wrapped.readerCalls != 1 {
			t.Fatal("list requests rebuilt the reader", wrapped.readerCalls)
		}
	}
	// A paused intent still belongs in the full list and uses current projection validity.
	exec("UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", movieSub)
	for _, item := range read(memberSession).Items {
		if item.Id == movieSub && (item.State != "paused" || *item.TotalEpisodes != 1 || *item.SatisfiedEpisodes != 0) {
			t.Fatal("paused intent semantics", item)
		}
	}
	metadata.fail = true
	for _, item := range read(memberSession).Items {
		if item.PosterUrl != nil {
			t.Fatal("metadata failure changed optional poster behavior")
		}
	}
}

func TestSubscriptionListCurrentAccountLock(t *testing.T) {
	a, adminSession, _, _, _, _, _, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: session.UserID.String(), Role: ports.RoleAdmin}
	reader := postgres.NewSubscriptionListStore(a.DBPool())
	for _, invalid := range []*ports.Actor{nil, {Type: ports.ActorTypeSystem, ID: actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleAdmin, Scopes: []string{"admin"}}} {
		if rows, err := reader.ReadSubscriptionList(ctx, invalid); rows != nil || (!errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized)) {
			t.Fatal("invalid current account accepted", rows, err)
		}
	}
	for _, change := range []string{"role='member'", "is_active=false"} {
		if _, err = a.DBPool().Exec(ctx, "UPDATE app_user SET role='admin',is_active=true WHERE id=$1", session.UserID); err != nil {
			t.Fatal(err)
		}
		tx, err := a.DBPool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "UPDATE app_user SET "+change+" WHERE id=$1", session.UserID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		type result struct {
			rows []ports.SubscriptionListEntry
			err  error
		}
		done := make(chan result, 1)
		go func() { rows, err := reader.ReadSubscriptionList(ctx, actor); done <- result{rows, err} }()
		// Always unblock and join the owned reader, even when a lock assertion fails.
		func() {
			defer func() {
				_ = tx.Rollback(ctx)
				select {
				case r := <-done:
					if r.rows != nil || r.err == nil {
						t.Error("concurrent account change returned data", r)
					}
				case <-ctx.Done():
					t.Error("reader did not finish")
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				if err = a.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '-- name: LockSubscriptionListAccount%' AND pid<>pg_backend_pid())`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("account lock was not reached")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}()
		if rows, err := reader.ReadSubscriptionList(ctx, actor); rows != nil || !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("stale authority survived", rows, err)
		}
	}
}

func TestSubscriptionListBoundedSnapshotAndFailure(t *testing.T) {
	a, adminSession, memberSession, root, unit, _, _, _ := setupOperationHTTP(t)
	ctx := context.Background()
	member, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(memberSession))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	var media uuid.UUID
	if err = a.DBPool().QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", unit).Scan(&media); err != nil {
		t.Fatal(err)
	}
	create := func(owner uuid.UUID, media, unit uuid.UUID) uuid.UUID {
		t.Helper()
		sub, err := a.Queries().CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: owner, MediaID: media, RootID: root, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: []byte("[]"), ScopeType: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Queries().UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: sub.ID, UnitID: unit, IntentVersion: 1}); err != nil {
			t.Fatal(err)
		}
		return sub.ID
	}
	sub := create(member.UserID, media, unit)
	trace := &subscriptionListTrace{}
	readPool := subscriptionListPool(t, a, trace)
	reader := postgres.NewSubscriptionListStore(readPool)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.UserID.String(), Role: ports.RoleAdmin}
	trace.afterEntries = func() {
		if _, err := a.DBPool().Exec(ctx, "UPDATE wanted_item SET is_skipped=true,user_skipped=true WHERE subscription_id=$1", sub); err != nil {
			t.Error(err)
		}
	}
	rows, err := reader.ReadSubscriptionList(ctx, actor)
	if err != nil || len(rows) != 1 || len(rows[0].WantedItems) != 1 || rows[0].WantedItems[0].IsSkipped {
		t.Fatal("mixed database snapshots", rows, err)
	}
	first := trace.recorded()
	for i := 0; i < 15; i++ {
		m, u := uuid.New(), uuid.New()
		if _, err = a.DBPool().Exec(ctx, "INSERT INTO media(id,title,media_type) VALUES($1,'Bulk scope','movie')", m); err != nil {
			t.Fatal(err)
		}
		if _, err = a.DBPool().Exec(ctx, "INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')", u, m); err != nil {
			t.Fatal(err)
		}
		create(member.UserID, m, u)
	}
	trace.reset()
	rows, err = reader.ReadSubscriptionList(ctx, actor)
	if err != nil || len(rows) != 16 {
		t.Fatal("full list", len(rows), err)
	}
	second := trace.recorded()
	if len(first) != 6 || len(second) != 6 {
		t.Fatal("query count grew with list", len(first), len(second), second)
	}
	for _, name := range []string{"LockSubscriptionListAccount", "SetSubscriptionListReadOnly", "ReadSubscriptionListEntries", "ReadSubscriptionListWanted"} {
		count := 0
		for _, sql := range second {
			if strings.HasPrefix(sql, "-- name: "+name) {
				count++
			}
		}
		if count != 1 {
			t.Fatal("bulk query count", name, count)
		}
	}
	for _, row := range rows {
		if row.ID == sub && !row.WantedItems[0].IsSkipped {
			t.Fatal("next request reused stale snapshot")
		}
	}
	// A failure after base rows have loaded must expose no partial successful list.
	if _, err = a.DBPool().Exec(ctx, "ALTER VIEW wanted_projection_validity RENAME TO owned_list_unavailable"); err != nil {
		t.Fatal(err)
	}
	if rows, err = reader.ReadSubscriptionList(ctx, actor); rows != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("database failure returned partial list", rows, err)
	}
	if _, err = a.DBPool().Exec(ctx, "ALTER VIEW owned_list_unavailable RENAME TO wanted_projection_validity"); err != nil {
		t.Fatal(err)
	}
	readPool.Close()
	if rows, err = reader.ReadSubscriptionList(ctx, actor); rows != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("closed database succeeded", rows, err)
	}
}
