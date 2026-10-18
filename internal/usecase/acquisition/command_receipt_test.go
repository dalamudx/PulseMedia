package acquisition_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"testing"
	"time"
)

func TestAcquireMovieCommandTailFaultRollsBack(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newCommandFixture(t)
			ctx := context.Background()
			before := commandCounts(t, f)
			if _, e := f.pool.Exec(ctx, `CREATE FUNCTION acquisition_command_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$`); e != nil {
				t.Fatal(e)
			}
			if _, e := f.pool.Exec(ctx, "CREATE TRIGGER acquisition_command_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION acquisition_command_fault()"); e != nil {
				t.Fatal(e)
			}
			if _, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatalf("unsafe failure: %v", e)
			}
			if commandCounts(t, f) != before {
				t.Fatal("tail failure left partial effects")
			}
			if _, e := f.pool.Exec(ctx, "DROP TRIGGER acquisition_command_fault ON "+table); e != nil {
				t.Fatal(e)
			}
			if _, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd); e != nil {
				t.Fatal(e)
			}
			if commandCounts(t, f) != "[1, 1, 1, 1, 1, 1, 1]" {
				t.Fatal(commandCounts(t, f))
			}
		})
	}
}
func TestAcquireMovieCommandRechecksUserAfterLock(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, mutation := range []string{"role='member'", "is_active=false"} {
			t.Run(fmt.Sprintf("replay=%v/%s", replay, mutation), func(t *testing.T) {
				f := newCommandFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				if replay {
					if _, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd); e != nil {
						t.Fatal(e)
					}
				}
				before := commandCounts(t, f)
				tx, e := f.pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
				if _, e = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", f.actor.ID); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { _, err := f.svc.AcquireMovie(ctx, f.actor, f.cmd); done <- err }()
				defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-done }()
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for {
					var waiting bool
					if e = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionCommandUser%')").Scan(&waiting); e != nil {
						t.Fatal(e)
					}
					if waiting {
						break
					}
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Fatal("no user-lock wait")
					}
				}
				if _, e = tx.Exec(ctx, "UPDATE app_user SET "+mutation+" WHERE id=$1", f.actor.ID); e != nil {
					t.Fatal(e)
				}
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				err := <-done
				done <- err
				if !errors.Is(err, ports.ErrForbidden) || commandCounts(t, f) != before {
					t.Fatalf("stale actor authorized: %v", err)
				}
			})
		}
	}
}
func TestAcquireMovieCommandFullIntentBinding(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	if _, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd); e != nil {
		t.Fatal(e)
	}
	before := commandCounts(t, f)
	for _, change := range []func(*ports.AcquireMovieCommand){func(c *ports.AcquireMovieCommand) { c.RootID = uuid.New() }, func(c *ports.AcquireMovieCommand) { c.UnitIDs = []uuid.UUID{uuid.New()} }, func(c *ports.AcquireMovieCommand) { c.CandidateID = uuid.New() }, func(c *ports.AcquireMovieCommand) { id := uuid.New(); c.DownloaderID = &id }, func(c *ports.AcquireMovieCommand) { c.PolicyRevision++ }, func(c *ports.AcquireMovieCommand) { c.IntentVersion++ }} {
		c := f.cmd
		change(&c)
		if _, e := f.svc.AcquireMovie(ctx, f.actor, c); !errors.Is(e, ports.ErrAcquisitionCommandConflict) {
			t.Fatal(e)
		}
	}
	if before != commandCounts(t, f) {
		t.Fatal("conflicting key changed original command")
	}
}

type commandFixture struct {
	pool  *pgxpool.Pool
	svc   *acquisition.Service
	actor *ports.Actor
	cmd   ports.AcquireMovieCommand
}

func newCommandFixture(t *testing.T) commandFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal(e)
	}
	p, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	q := db.New(p)
	root, unit, media, idx, candidate, user := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "command-user", PasswordHash: "fixture", Role: "admin", IsActive: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), Name: "Movies", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Fight Club", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "Source", Url: "https://example.test", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: idx, Title: "Fight.Club.1999.1080p.mkv", Guid: "command-candidate", SourceConfigVersion: 1, LinkEncrypted: []byte("cipher"), QualityProfile: []byte("{}")}); e != nil {
		t.Fatal(e)
	}
	return commandFixture{p, acquisition.NewService(postgres.NewAcquisitionStore(p, idgen.New(), clock.New(), acquisition.SelectMovieRound)), &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}, ports.AcquireMovieCommand{UnitIDs: []uuid.UUID{unit}, RootID: root, CandidateID: candidate, IdempotencyKey: "command-once"}}
}
func commandCounts(t *testing.T, f commandFixture) string {
	t.Helper()
	var raw string
	if e := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM idempotency_record))::text`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestAcquireMovieCommandReceiptSurvivesMutableRoot(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	first, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd)
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_record").Scan(&count); e != nil || count != 1 {
		t.Fatalf("receipt count=%d error=%v", count, e)
	}
	before := commandCounts(t, f)
	if _, e = f.pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", f.cmd.RootID); e != nil {
		t.Fatal(e)
	}
	again, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(again)
	if string(a) != string(b) || before != commandCounts(t, f) {
		t.Fatal("recovery mutated original command result")
	}
}
func TestAcquireMovieCommandEightWaySingleReceipt(t *testing.T) {
	f := newCommandFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan *ports.AcquireResult, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd)
			if e != nil {
				errs <- e
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	var id uuid.UUID
	for r := range results {
		if id == uuid.Nil {
			id = r.OperationID
		}
		if r.OperationID != id || r.Status != "dispatched" {
			t.Fatal("different concurrent result")
		}
	}
	if got := commandCounts(t, f); got != "[1, 1, 1, 1, 1, 1, 1]" {
		t.Fatal(got)
	}
}
func TestAcquireMovieCommandCurrentUserBeforeReceipt(t *testing.T) {
	for _, mode := range []string{"demoted", "disabled", "deleted", "token", "empty-token", "wrong-command"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommandFixture(t)
			ctx := context.Background()
			if _, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd); e != nil {
				t.Fatal(e)
			}
			before := commandCounts(t, f)
			a := *f.actor
			c := f.cmd
			switch mode {
			case "demoted":
				if _, e := f.pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", a.ID); e != nil {
					t.Fatal(e)
				}
			case "disabled":
				if _, e := f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", a.ID); e != nil {
					t.Fatal(e)
				}
			case "deleted":
				if _, e := f.pool.Exec(ctx, "DELETE FROM app_user WHERE id=$1", a.ID); e != nil {
					t.Fatal(e)
				}
			case "token":
				a.Scopes = []string{"read"}
			case "empty-token":
				a.Scopes = []string{}
			case "wrong-command":
				c.CandidateID = uuid.New()
			}
			_, e := f.svc.AcquireMovie(ctx, &a, c)
			if e == nil || (!errors.Is(e, ports.ErrForbidden) && mode != "wrong-command") {
				t.Fatalf("replay accepted: %v", e)
			}
			if before != commandCounts(t, f) {
				t.Fatal("denied replay mutated command")
			}
		})
	}
}
