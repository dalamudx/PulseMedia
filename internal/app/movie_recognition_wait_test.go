package app_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"testing"
	"time"
)

func TestMovieRecognitionQueueFinalScopeFault(t *testing.T) {
	f, s := recognitionFixture(t)
	ctx := context.Background()
	before := recognitionState(t, f)
	if _, e := f.pool.Exec(ctx, `CREATE FUNCTION reject_recognition_scope() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic recognition scope fault'; END $$; CREATE TRIGGER reject_recognition_scope BEFORE INSERT ON movie_recognition_work FOR EACH ROW EXECUTE FUNCTION reject_recognition_scope();`); e != nil {
		t.Fatal(e)
	}
	if n, e := s.QueueDue(ctx, 20); e == nil || n != 0 {
		t.Fatal("scope fault admitted job", n, e)
	}
	if recognitionState(t, f) != before {
		t.Fatal("job without scope")
	}
	if _, e := f.pool.Exec(ctx, "DROP TRIGGER reject_recognition_scope ON movie_recognition_work; DROP FUNCTION reject_recognition_scope()"); e != nil {
		t.Fatal(e)
	}
	if n, e := s.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestMovieRecognitionLeaseExpiryAfterRootWait(t *testing.T) {
	f, s := recognitionFixture(t)
	w := recognitionWork(t, f, s)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, e := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()+interval '750 milliseconds' WHERE id=$1", w.Lease.ID); e != nil {
		t.Fatal(e)
	}
	before := recognitionState(t, f)
	blocker, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, e = blocker.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); e != nil {
		t.Fatal(e)
	}
	repo := postgres.NewLeasedMovieLinkStore(movieLinkWaitPool(t, f), f.gen, w)
	done := make(chan error, 1)
	go func() {
		_, e := mediaUsecase.NewMatcher(repo, movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path)
		done <- e
	}()
	waitMovieLinkLock(t, f)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var expired bool
		if e = f.pool.QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM job WHERE id=$1", w.Lease.ID).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if expired {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrMovieRecognitionLease) {
		t.Fatal("expired wait committed", e)
	}
	if recognitionState(t, f) != before {
		t.Fatal("expired task/facts/audit/event mutated")
	}
	if _, e = f.q.RecoverExpiredMovieRecognitionJobs(ctx); e != nil {
		t.Fatal(e)
	}
	j, e := jobs.NewQueue(f.pool, f.gen, clock.New()).Claim(ctx, []string{"movie_recognition"}, "recognition-owner", 5*time.Minute)
	if e != nil || j == nil || j.LeaseToken <= w.Lease.Token {
		t.Fatal(j, e)
	}
	next, e := s.Work(ctx, ports.JobLease{ID: j.ID, Owner: "recognition-owner", Token: j.LeaseToken})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.LoadSource(ctx, f.root, f.path); !errors.Is(e, ports.ErrMovieRecognitionLease) {
		t.Fatal("old token recovered", e)
	}
	if _, e = mediaUsecase.NewMatcher(postgres.NewLeasedMovieLinkStore(f.pool, f.gen, next), movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); e != nil {
		t.Fatal(e)
	}
}
