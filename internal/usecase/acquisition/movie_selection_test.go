package acquisition_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type selectedMetadataFixture struct {
	ports.MetadataProvider
	calls atomic.Int32
}

func (p *selectedMetadataFixture) GetMovieDetails(context.Context, int64, string) (*ports.MovieMetadata, error) {
	p.calls.Add(1)
	return &ports.MovieMetadata{TMDBID: 550, MediaType: "movie", Title: "Fight Club", OriginalTitle: "Fight Club", Year: 1999}, nil
}
func TestExplicitMovieSelectionAtomicTargetAndStableReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	ids := idgen.New()
	root, indexer, candidate, client := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), Name: "Chosen movie root", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexer, Name: "Chosen source", Url: "https://example.test", ApiKeyEncrypted: []byte("fixture-cipher"), Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexer, Title: "Fight.Club.1999.1080p.Bluray.x264", Guid: "chosen-movie", SourceConfigVersion: 1, SizeBytes: 1024, LinkEncrypted: []byte("fixture-link-cipher"), QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2","codec":"H264","codec_parser_version":"codec.tokens.v2"}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "Chosen client", ClientType: "qbittorrent", Url: "http://127.0.0.1:8080", Username: "fixture", PasswordEncrypted: []byte("fixture-cipher"), RemotePathMap: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	queryID := uuid.New()
	if _, e = pool.Exec(ctx, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", queryID, indexer); e != nil {
		t.Fatal(e)
	}
	if e = q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: queryID, CandidateID: candidate}); e != nil {
		t.Fatal(e)
	}
	provider := &selectedMetadataFixture{}
	svc := acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(pool, ids, clock.New(), acquisition.SelectMovieRound), provider, clock.New())
	actor := pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	selection := ports.MovieSelection{TMDBID: 550, RootID: root, CandidateID: candidate, DownloaderID: client, PolicyID: "default"}
	p, e := svc.Preview(ctx, actor, selection)
	if e != nil || p.Decision != "accept" {
		t.Fatal("preview failed", p, e)
	}
	cmd := ports.MovieSelectionCommand{Selection: selection, ExpectedInputStamp: p.InputStamp, Confirmed: true}
	for _, change := range []struct {
		name, modify, restore string
		args                  []any
	}{
		{"root health", "UPDATE library_root SET health='offline' WHERE id=$1", "UPDATE library_root SET health='online' WHERE id=$1", []any{root}},
		{"root baseline", "UPDATE library_root SET baseline_completed=false WHERE id=$1", "UPDATE library_root SET baseline_completed=true WHERE id=$1", []any{root}},
		{"client configuration", "UPDATE download_client SET username='changed' WHERE id=$1", "UPDATE download_client SET username='fixture' WHERE id=$1", []any{client}},
		{"source receipt failed", "UPDATE indexer_query SET state='failed' WHERE id=$1", "UPDATE indexer_query SET state='succeeded' WHERE id=$1", []any{queryID}},
		{"source receipt expired", "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE id=$1", "UPDATE indexer_query SET finished_at=clock_timestamp() WHERE id=$1", []any{queryID}},
		{"source offline", "UPDATE indexer SET health='offline' WHERE id=$1", "UPDATE indexer SET health='online' WHERE id=$1", []any{indexer}},
		{"candidate byte evidence", "UPDATE release_candidate SET size_bytes=2048 WHERE id=$1", "UPDATE release_candidate SET size_bytes=1024 WHERE id=$1", []any{candidate}},
	} {
		if _, e = pool.Exec(ctx, change.modify, change.args...); e != nil {
			t.Fatal(change.name, e)
		}
		if _, e = svc.Commit(ctx, actor, cmd, "changed-"+change.name); !errors.Is(e, ports.ErrMovieSelectionConflict) {
			t.Fatal("old preview survived", change.name, e)
		}
		var entities, work int
		if e = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media)+(SELECT count(*) FROM media_unit),(SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&entities, &work); e != nil || entities != 0 || work != 0 {
			t.Fatal("partial changed-input write", change.name, entities, work, e)
		}
		if _, e = pool.Exec(ctx, change.restore, change.args...); e != nil {
			t.Fatal(e)
		}
	}

	p, e = svc.Preview(ctx, actor, selection)
	if e != nil || p.Decision != "accept" {
		t.Fatal("post-restore preview", p, e)
	}
	cmd.ExpectedInputStamp = p.InputStamp
	if _, e = pool.Exec(ctx, `CREATE FUNCTION reject_selected_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='selected-once' THEN RAISE EXCEPTION 'receipt fault';END IF;RETURN NEW;END$$;CREATE TRIGGER reject_selected_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_selected_receipt()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Commit(ctx, actor, cmd, "selected-once"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("fault accepted", e)
	}
	var mediaCount, unitCount, reservations, jobs int
	if e = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&mediaCount, &unitCount, &reservations, &jobs); e != nil || mediaCount != 0 || unitCount != 0 || reservations != 0 || jobs != 0 {
		t.Fatal("partial selected target/work", mediaCount, unitCount, reservations, jobs, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_selected_receipt ON idempotency_record;DROP FUNCTION reject_selected_receipt()"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Commit(ctx, actor, cmd, "selected-once")
			if e != nil {
				errs <- e
				return
			}
			raw, _ := json.Marshal(r)
			results <- string(raw)
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for e := range errs {
		t.Fatal(e)
	}
	first := ""
	for r := range results {
		if first == "" {
			first = r
		} else if r != first {
			t.Fatal("unstable receipt")
		}
	}
	if e = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&mediaCount, &unitCount, &reservations, &jobs); e != nil || mediaCount != 1 || unitCount != 1 || reservations != 1 || jobs != 1 {
		t.Fatal("duplicate target/work", mediaCount, unitCount, reservations, jobs, e)
	}
	calls := provider.calls.Load()
	if _, e = svc.Commit(ctx, actor, cmd, "selected-once"); e != nil || provider.calls.Load() != calls {
		t.Fatal("receipt re-fetched or failed", e)
	}
	scoped := *actor
	scoped.Scopes = []string{"*"}
	if _, e = svc.Commit(ctx, &scoped, cmd, "selected-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("scoped token gained human selection authority", e)
	}
	other := *actor
	other.Role = "guest"
	if _, e = svc.Commit(ctx, &other, cmd, "selected-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cached role elevation", e)
	}
}
