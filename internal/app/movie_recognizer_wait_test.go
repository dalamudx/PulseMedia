package app_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func TestMovieRecognizerPhysicalProofExpiresAfterRootWait(t *testing.T) {
	f, s, _ := physicalRecognitionFixture(t)
	work := recognitionWork(t, f, s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "movie-link-waiter"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "15s"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "20s"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := postgres.NewCheckedMovieRecognitionStore(pool, f.gen, work)
	source, err := repo.LoadSource(ctx, f.root, f.path)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := repo.BeginFileCheck(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	before := recognitionState(t, f)
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); err != nil {
		t.Fatal(err)
	}
	meta, _ := movieLinkProvider{}.GetMovieDetails(ctx, 550, "")
	done := make(chan error, 1)
	go func() {
		_, e := repo.Link(ctx, ports.MovieLinkInput{Source: source, Metadata: *meta, QualityJSON: []byte(`{}`), PhysicalCheck: &proof})
		done <- e
	}()
	waitMovieLinkLock(t, f)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var expired bool
		if err = f.pool.QueryRow(ctx, "SELECT clock_timestamp()>=$1::timestamptz+interval '5 seconds'", proof.StartedAt).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(err, ports.ErrMoviePhysicalCheck) {
		t.Fatal("expired physical evidence committed", err)
	}
	if recognitionState(t, f) != before {
		t.Fatal("expiry rewrote source/task/audit/events")
	}
	proof, err = repo.BeginFileCheck(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Link(ctx, ports.MovieLinkInput{Source: source, Metadata: *meta, QualityJSON: []byte(`{}`), PhysicalCheck: &proof}); err != nil {
		t.Fatal(err)
	}
}
