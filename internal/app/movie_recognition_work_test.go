package app_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func recognitionFixture(t *testing.T) (movieLinkFixture, *postgres.MovieRecognitionStore) {
	t.Helper()
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, "UPDATE library_file SET file_fingerprint='native-fixture' WHERE id=$1", f.file); e != nil {
		t.Fatal(e)
	}
	return f, postgres.NewMovieRecognitionStore(f.pool, f.gen)
}
func recognitionWork(t *testing.T, f movieLinkFixture, s *postgres.MovieRecognitionStore) ports.MovieRecognitionWork {
	t.Helper()
	ctx := context.Background()
	if n, e := s.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	j, e := jobs.NewQueue(f.pool, f.gen, clock.New()).Claim(ctx, []string{"movie_recognition"}, "recognition-owner", 5*time.Minute)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	w, e := s.Work(ctx, ports.JobLease{ID: j.ID, Owner: "recognition-owner", Token: j.LeaseToken})
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func recognitionState(t *testing.T, f movieLinkFixture) string {
	t.Helper()
	var state string
	e := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM job j),
 (SELECT jsonb_agg(to_jsonb(w) ORDER BY job_id) FROM movie_recognition_work w),
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM media m),
 (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM media_identity i),
 (SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM media_unit u),
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY file_id,unit_id) FROM library_file_unit m),
 (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f),
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM library_root r),
 (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM library_file f),
 (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_log a),
 (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM outbox_event e))::text`).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	return state
}
func TestMovieRecognitionQueueAdmissionAndEightWay(t *testing.T) {
	f, s := recognitionFixture(t)
	ctx := context.Background()
	for _, statement := range []string{"UPDATE library_root SET baseline_completed=false", "UPDATE library_root SET baseline_completed=true,health='offline'", "UPDATE library_root SET health='online',media_type='tv'; UPDATE library_root SET baseline_completed=true", "UPDATE library_root SET media_type='movie'; UPDATE library_root SET baseline_completed=true; UPDATE library_file SET file_fingerprint=NULL"} {
		if _, e := f.pool.Exec(ctx, statement); e != nil {
			t.Fatal(e)
		}
		if n, e := s.QueueDue(ctx, 20); e != nil || n != 0 {
			t.Fatal(statement, n, e)
		}
	}
	if _, e := f.pool.Exec(ctx, "UPDATE library_file SET file_fingerprint='native-fixture'"); e != nil {
		t.Fatal(e)
	}
	var g sync.WaitGroup
	es := make(chan error, 8)
	ns := make(chan int, 8)
	for i := 0; i < 8; i++ {
		g.Add(1)
		go func() { defer g.Done(); n, e := s.QueueDue(ctx, 20); es <- e; ns <- n }()
	}
	g.Wait()
	close(es)
	close(ns)
	for e := range es {
		if e != nil {
			t.Fatal(e)
		}
	}
	n := 0
	for x := range ns {
		n += x
	}
	if n != 1 {
		t.Fatal("duplicate work", n)
	}
}
func TestMovieRecognitionOutcomeAuditEventAndJobAtomic(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(fmt.Sprint(review), func(t *testing.T) {
			f, s := recognitionFixture(t)
			w := recognitionWork(t, f, s)
			ctx := context.Background()
			repo := postgres.NewLeasedMovieLinkStore(f.pool, f.gen, w)
			if review {
				if e := repo.Review(ctx, "low_confidence"); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e := mediaUsecase.NewMatcher(repo, movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); e != nil {
					t.Fatal(e)
				}
			}
			var state, outcome string
			var facts, audits, events int
			e := f.pool.QueryRow(ctx, `SELECT j.state,w.outcome,(SELECT count(*) FROM unit_fact),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event) FROM job j JOIN movie_recognition_work w ON w.job_id=j.id WHERE j.id=$1`, w.Lease.ID).Scan(&state, &outcome, &facts, &audits, &events)
			want := "linked"
			wantFacts := 1
			if review {
				want = "review"
				wantFacts = 0
			}
			if e != nil || state != "succeeded" || outcome != want || facts != wantFacts || audits != 1 || events != 1 {
				t.Fatal(state, outcome, facts, audits, events, e)
			}
			before := recognitionState(t, f)
			if n, err := s.QueueDue(ctx, 20); err != nil || n != 0 {
				t.Fatal("terminal source repeated", n, err)
			}
			if _, e = s.Work(ctx, w.Lease); !errors.Is(e, ports.ErrMovieRecognitionLease) {
				t.Fatal("finished owner", e)
			}
			if e = repo.Review(ctx, "low_confidence"); !errors.Is(e, ports.ErrMovieRecognitionLease) {
				t.Fatal("finished result rewritten", e)
			}
			if recognitionState(t, f) != before {
				t.Fatal("receipt mutated")
			}
		})
	}
}
func TestMovieRecognitionTerminalFaultRollsBackAndRecovers(t *testing.T) {
	for _, table := range []string{"movie_recognition_work", "audit_log", "outbox_event", "job"} {
		t.Run(table, func(t *testing.T) {
			f, s := recognitionFixture(t)
			w := recognitionWork(t, f, s)
			ctx := context.Background()
			repo := postgres.NewLeasedMovieLinkStore(f.pool, f.gen, w)
			before := recognitionState(t, f)
			event := "INSERT"
			if table == "job" || table == "movie_recognition_work" {
				event = "UPDATE"
			}
			ddl := fmt.Sprintf(`CREATE FUNCTION reject_recognition_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic recognition final fault'; END $$; CREATE TRIGGER reject_recognition_finish BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION reject_recognition_finish();`, event, table)
			if _, e := f.pool.Exec(ctx, ddl); e != nil {
				t.Fatal(e)
			}
			if _, e := mediaUsecase.NewMatcher(repo, movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); e == nil {
				t.Fatal("fault committed")
			}
			if recognitionState(t, f) != before {
				t.Fatal("partial recognition outcome")
			}
			if _, e := f.pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER reject_recognition_finish ON %s; DROP FUNCTION reject_recognition_finish()", table)); e != nil {
				t.Fatal(e)
			}
			if _, e := mediaUsecase.NewMatcher(repo, movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestMovieRecognitionScopeAndExpiredOwnerRejectWrites(t *testing.T) {
	for _, branch := range []string{"owner", "token", "state", "expiry", "root", "file", "generation", "kind"} {
		t.Run(branch, func(t *testing.T) {
			f, s := recognitionFixture(t)
			w := recognitionWork(t, f, s)
			ctx := context.Background()
			switch branch {
			case "owner":
				w.Lease.Owner = "old-owner"
			case "token":
				w.Lease.Token++
			case "root":
				w.RootID = uuid.New()
			case "file":
				w.FileID = uuid.New()
			case "generation":
				w.Generation++
			case "state":
				if _, e := f.pool.Exec(ctx, "UPDATE job SET state='queued' WHERE id=$1", w.Lease.ID); e != nil {
					t.Fatal(e)
				}
			case "expiry":
				if _, e := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp() WHERE id=$1", w.Lease.ID); e != nil {
					t.Fatal(e)
				}
			case "kind":
				if _, e := f.pool.Exec(ctx, "UPDATE job SET kind='other' WHERE id=$1", w.Lease.ID); e != nil {
					t.Fatal(e)
				}
			}
			before := recognitionState(t, f)
			repo := postgres.NewLeasedMovieLinkStore(f.pool, f.gen, w)
			if _, e := mediaUsecase.NewMatcher(repo, movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); !errors.Is(e, ports.ErrMovieRecognitionLease) {
				t.Fatal(e)
			}
			if e := repo.Review(ctx, "low_confidence"); !errors.Is(e, ports.ErrMovieRecognitionLease) {
				t.Fatal(e)
			}
			if recognitionState(t, f) != before {
				t.Fatal("invalid owner wrote")
			}
		})
	}
}
func TestMovieRecognitionScopeForeignKeyAndHistoryDowngrade(t *testing.T) {
	f, s := recognitionFixture(t)
	ctx := context.Background()
	w := recognitionWork(t, f, s)
	other, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "other"})
	if e != nil {
		t.Fatal(e)
	}
	before := recognitionState(t, f)
	if e = f.q.InsertMovieRecognitionJob(ctx, db.InsertMovieRecognitionJobParams{JobID: uuid.New(), RootID: other.ID, FileID: f.file, Generation: w.Generation + 1}); e == nil {
		t.Fatal("cross-root scope accepted")
	}
	if recognitionState(t, f) != before {
		t.Fatal("orphan job")
	}
	// Newer empty schemas may roll back; Schema 52 owns recognition history.
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 52; expected-- {
		if v, err := postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir); err != nil || v != expected {
			t.Fatal(v, err)
		}
		if recognitionState(t, f) != before {
			t.Fatal("newer downgrade changed recognition history")
		}
	}
	if _, e = postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir); e == nil {
		t.Fatal("durable work discarded")
	}
	if recognitionState(t, f) != before {
		t.Fatal("rollback changed history")
	}
}
