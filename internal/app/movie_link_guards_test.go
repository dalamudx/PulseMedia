package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func movieLinkState(t *testing.T, f movieLinkFixture) string {
	t.Helper()
	var state string
	err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'media',(SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY id),'[]') FROM media m),
 'identity',(SELECT COALESCE(jsonb_agg(to_jsonb(i) ORDER BY id),'[]') FROM media_identity i),
 'units',(SELECT COALESCE(jsonb_agg(to_jsonb(u) ORDER BY id),'[]') FROM media_unit u),
 'mappings',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY file_id,unit_id),'[]') FROM library_file_unit x),
 'facts',(SELECT COALESCE(jsonb_agg(to_jsonb(z) ORDER BY id),'[]') FROM unit_fact z),
 'roots',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id),'[]') FROM library_root r),
 'files',(SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY id),'[]') FROM library_file f))::text`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func TestMovieLinkEightWayReplayAndCrossRootIdentity(t *testing.T) {
	for _, crossRoot := range []bool{false, true} {
		t.Run(fmt.Sprint(crossRoot), func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			roots := []uuid.UUID{f.root}
			files := []uuid.UUID{f.file}
			for i := 1; i < 8; i++ {
				if crossRoot {
					r, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: f.gen.NewUUID(), Path: t.TempDir(), MediaType: "movie", Name: "Concurrent"})
					if e != nil {
						t.Fatal(e)
					}
					file, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: f.gen.NewUUID(), RootID: r.ID, RelPath: f.path, SizeBytes: 100, MtimeNs: time.Now().UnixNano()})
					if e != nil {
						t.Fatal(e)
					}
					roots = append(roots, r.ID)
					files = append(files, file.ID)
				} else {
					roots = append(roots, f.root)
				}
			}
			ready := make(chan struct{}, 8)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{before: func() {
				ready <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			}})
			type outcome struct {
				r *usecaseMedia.MatchResult
				e error
			}
			done := make(chan outcome, 8)
			for _, root := range roots {
				go func() { r, e := m.MatchAndLinkFile(ctx, root, f.path); done <- outcome{r, e} }()
			}
			for i := 0; i < 8; i++ {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("metadata barrier timed out")
				}
			}
			close(release)
			var work, unit uuid.UUID
			for i := 0; i < 8; i++ {
				out := <-done
				if out.e != nil {
					t.Fatal(out.e)
				}
				if i == 0 {
					work = out.r.Media.ID
					unit = out.r.Unit.ID
				}
				if out.r.Media.ID != work || out.r.Unit.ID != unit {
					t.Fatal("concurrency forked canonical identity")
				}
			}
			var nMedia, nIdentity, nUnits, nFacts int
			if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_identity),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM unit_fact)").Scan(&nMedia, &nIdentity, &nUnits, &nFacts); e != nil {
				t.Fatal(e)
			}
			expected := 1
			if crossRoot {
				expected = 8
			}
			if nMedia != 1 || nIdentity != 2 || nUnits != 1 || nFacts != expected {
				t.Fatalf("unexpected counts %d/%d/%d/%d", nMedia, nIdentity, nUnits, nFacts)
			}
			for _, file := range files {
				row, e := f.q.GetFileByID(ctx, file)
				if e != nil {
					t.Fatal(e)
				}
				root, e := f.q.GetLibraryRootByID(ctx, row.RootID)
				if e != nil {
					t.Fatal(e)
				}
				if row.LastMutationSeq != 1 || root.MutationSeq != 1 {
					t.Fatal("replay advanced watermark")
				}
			}
		})
	}
}
func TestMovieLinkWriteFaultRollsBackAndRecovers(t *testing.T) {
	for _, table := range []string{"media_identity", "media_unit", "unit_fact", "library_root", "library_file"} {
		t.Run(table, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx := context.Background()
			before := movieLinkState(t, f)
			if _, e := f.pool.Exec(ctx, `CREATE FUNCTION movie_link_fault() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'movie link injected failure';END$$`); e != nil {
				t.Fatal(e)
			}
			operation := "INSERT"
			condition := ""
			if table == "library_root" || table == "library_file" {
				operation = "UPDATE"
			}
			if table == "library_file" {
				condition = " WHEN (NEW.last_mutation_seq IS DISTINCT FROM OLD.last_mutation_seq)"
			}
			if _, e := f.pool.Exec(ctx, "CREATE TRIGGER movie_link_fault AFTER "+operation+" ON "+table+" FOR EACH ROW"+condition+" EXECUTE FUNCTION movie_link_fault()"); e != nil {
				t.Fatal(e)
			}
			m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
			if _, e := m.MatchAndLinkFile(ctx, f.root, f.path); e == nil {
				t.Fatal("fault committed")
			}
			if movieLinkState(t, f) != before {
				t.Fatal("fault left partial identity, facts or watermark")
			}
			if _, e := f.pool.Exec(ctx, "DROP TRIGGER movie_link_fault ON "+table); e != nil {
				t.Fatal(e)
			}
			if _, e := m.MatchAndLinkFile(ctx, f.root, f.path); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestMovieLinkLegacyQualityAndRetiredTargetRemainUnchanged(t *testing.T) {
	for _, branch := range []string{"legacy_quality", "retired_unit", "other_mapping", "imdb_conflict"} {
		t.Run(branch, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx := context.Background()
			external := "550"
			if branch == "other_mapping" || branch == "imdb_conflict" {
				external = "123"
			}
			work, e := f.q.CreateMedia(ctx, db.CreateMediaParams{ID: f.gen.NewUUID(), Title: "Historical", MediaType: "movie"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: f.gen.NewUUID(), MediaID: work.ID, Namespace: "tmdb.movie", ExternalID: external}); e != nil {
				t.Fatal(e)
			}
			unit, e := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.gen.NewUUID(), MediaID: work.ID, UnitKey: "movie"})
			if e != nil {
				t.Fatal(e)
			}
			switch branch {
			case "legacy_quality", "other_mapping":
				if e = f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: unit.ID}); e != nil {
					t.Fatal(e)
				}
				if _, e = f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: f.gen.NewUUID(), UnitID: unit.ID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)}); e != nil {
					t.Fatal(e)
				}
			case "retired_unit":
				if _, e = f.pool.Exec(ctx, "UPDATE media_unit SET retired=true WHERE id=$1", unit.ID); e != nil {
					t.Fatal(e)
				}
			case "imdb_conflict":
				if _, e = f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: f.gen.NewUUID(), MediaID: work.ID, Namespace: "imdb", ExternalID: "tt0137523"}); e != nil {
					t.Fatal(e)
				}
			}
			before := movieLinkState(t, f)
			m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
			if r, e := m.MatchAndLinkFile(ctx, f.root, f.path); !errors.Is(e, ports.ErrMovieLinkConflict) || !r.RequiresReview {
				t.Fatalf("historical conflict approved: %v", e)
			}
			if movieLinkState(t, f) != before {
				t.Fatal("historical record was rewritten or re-certified")
			}
		})
	}
}
func movieLinkWaitPool(t *testing.T, f movieLinkFixture) *pgxpool.Pool {
	t.Helper()
	cfg := f.pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.ConnConfig.RuntimeParams["application_name"] = "movie-link-waiter"
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	return pool
}
func waitMovieLinkLock(t *testing.T, f movieLinkFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='movie-link-waiter' AND wait_event_type='Lock')").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("consumer did not reach row lock")
		}
	}
}
func TestMovieLinkRechecksRootAfterLockWait(t *testing.T) {
	for _, mutation := range []string{"health='offline'", "generation=generation+1", "health_revision=health_revision+1", "path=path||'/replacement'", "media_type='tv'"} {
		t.Run(mutation, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); e != nil {
				t.Fatal(e)
			}
			m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(movieLinkWaitPool(t, f), f.gen), movieLinkProvider{})
			done := make(chan error, 1)
			go func() { _, err := m.MatchAndLinkFile(ctx, f.root, f.path); done <- err }()
			waitMovieLinkLock(t, f)
			if _, e = tx.Exec(ctx, "UPDATE library_root SET "+mutation+" WHERE id=$1", f.root); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			before := movieLinkState(t, f)
			if e = <-done; !errors.Is(e, ports.ErrMovieLinkStale) {
				t.Fatalf("stale root accepted: %v", e)
			}
			if movieLinkState(t, f) != before {
				t.Fatal("waiter modified replacement root")
			}
		})
	}
}
func TestMovieLinkRechecksRetiredUnitAfterLockWait(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	work, e := f.q.CreateMedia(ctx, db.CreateMediaParams{ID: f.gen.NewUUID(), Title: "Fight Club", MediaType: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: f.gen.NewUUID(), MediaID: work.ID, Namespace: "tmdb.movie", ExternalID: "550"}); e != nil {
		t.Fatal(e)
	}
	unit, e := f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.gen.NewUUID(), MediaID: work.ID, UnitKey: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", unit.ID); e != nil {
		t.Fatal(e)
	}
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(movieLinkWaitPool(t, f), f.gen), movieLinkProvider{})
	done := make(chan error, 1)
	go func() { _, err := m.MatchAndLinkFile(ctx, f.root, f.path); done <- err }()
	waitMovieLinkLock(t, f)
	if _, e = tx.Exec(ctx, "UPDATE media_unit SET retired=true WHERE id=$1", unit.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	before := movieLinkState(t, f)
	if e = <-done; !errors.Is(e, ports.ErrMovieLinkConflict) {
		t.Fatalf("retired unit accepted: %v", e)
	}
	if movieLinkState(t, f) != before {
		t.Fatal("retired target modified")
	}
}
