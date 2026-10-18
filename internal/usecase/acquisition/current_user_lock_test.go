package acquisition_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"testing"
	"time"
)

func waitAcquisitionLock(t *testing.T, f commandFixture, ctx context.Context, pattern string) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)", pattern).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("lock wait not observed")
		}
	}
}
func TestManualAcquisitionCurrentUserAfterLock(t *testing.T) {
	for _, mode := range []string{"unkeyed", "preview", "receipt", "commit"} {
		t.Run(mode, func(t *testing.T) {
			f, svc, _, c := authorizationSelectionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			f.cmd.IdempotencyKey = ""
			var stamp string
			if mode == "commit" || mode == "receipt" {
				p, e := svc.Preview(ctx, f.actor, c)
				if e != nil {
					t.Fatal(e)
				}
				stamp = p.InputStamp
				if mode == "receipt" {
					if _, e = svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: stamp, Confirmed: true}, "original-selection"); e != nil {
						t.Fatal(e)
					}
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
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				var err error
				switch mode {
				case "unkeyed":
					_, err = f.svc.AcquireMovie(ctx, f.actor, f.cmd)
				case "preview":
					_, err = svc.Preview(ctx, f.actor, c)
				case "receipt":
					_, err = svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: stamp, Confirmed: true}, "original-selection")
				case "commit":
					_, err = svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: stamp, Confirmed: true}, "new-selection")
				}
				done <- err
			}()
			defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-finished }()
			waitAcquisitionLock(t, f, ctx, "%LockAcquisitionCommandUser%")
			if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if !errors.Is(e, ports.ErrForbidden) && (mode != "preview" || !errors.Is(e, ports.ErrUnavailable)) {
				t.Fatal("expired user authorized", e)
			}
			if before != commandCounts(t, f) {
				t.Fatal("denial changed business state")
			}
		})
	}
}
func TestSelectedMovieCurrentUserAtCommitAndTailRollback(t *testing.T) {
	for _, tail := range []string{"disabled", "audit", "receipt"} {
		t.Run(tail, func(t *testing.T) {
			f, svc, _, c := authorizationSelectionFixture(t)
			ctx := context.Background()
			p, e := svc.Preview(ctx, f.actor, c)
			if e != nil {
				t.Fatal(e)
			}
			before := commandCounts(t, f)
			var mediaBefore, unitBefore int
			if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_unit)").Scan(&mediaBefore, &unitBefore); e != nil {
				t.Fatal(e)
			}
			if tail == "disabled" {
				if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID); e != nil {
					t.Fatal(e)
				}
			} else {
				table, predicate := "audit_log", "NEW.action='movie.selection.committed'"
				if tail == "receipt" {
					table, predicate = "idempotency_record", "true"
				}
				if _, e = f.pool.Exec(ctx, fmt.Sprintf("CREATE FUNCTION reject_selection_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'synthetic-selection-tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_selection_tail BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION reject_selection_tail()", predicate, table)); e != nil {
					t.Fatal(e)
				}
			}
			_, e = svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: p.InputStamp, Confirmed: true}, "selection-tail")
			want := ports.ErrUnavailable
			if tail == "disabled" {
				want = ports.ErrForbidden
			}
			if !errors.Is(e, want) {
				t.Fatal(e)
			}
			var mediaAfter, unitAfter int
			if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM media),(SELECT count(*) FROM media_unit)").Scan(&mediaAfter, &unitAfter); e != nil {
				t.Fatal(e)
			}
			if before != commandCounts(t, f) || mediaBefore != mediaAfter || unitBefore != unitAfter {
				t.Fatal("failed commit left partial entities")
			}
			if tail != "disabled" {
				if _, e = f.pool.Exec(ctx, "DROP TRIGGER reject_selection_tail ON "+func() string {
					if tail == "audit" {
						return "audit_log"
					}
					return "idempotency_record"
				}()); e != nil {
					t.Fatal(e)
				}
				if _, e = svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: p.InputStamp, Confirmed: true}, "selection-tail"); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

type userMutationMetadata struct {
	*selectedMetadataFixture
	started chan struct{}
	release chan struct{}
}

func (m *userMutationMetadata) GetMovieDetails(ctx context.Context, id int64, language string) (*ports.MovieMetadata, error) {
	close(m.started)
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.selectedMetadataFixture.GetMovieDetails(ctx, id, language)
}
func TestSelectedMovieUserChangedDuringMetadata(t *testing.T) {
	f, _, base, c := authorizationSelectionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	provider := &userMutationMetadata{base, make(chan struct{}), make(chan struct{})}
	svc := acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(f.pool, idgen.New(), clock.New(), acquisition.SelectMovieRound), provider, clock.New())
	before := commandCounts(t, f)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); _, e := svc.Preview(ctx, f.actor, c); done <- e }()
	defer func() { cancel(); <-finished }()
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("metadata not started")
	}
	if _, e := f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID); e != nil {
		t.Fatal(e)
	}
	close(provider.release)
	if e := <-done; !errors.Is(e, ports.ErrForbidden) {
		t.Fatal(e)
	}
	if before != commandCounts(t, f) {
		t.Fatal("preview wrote state")
	}
}
func TestManualAcquisitionCannotBorrowSubscriptionDemand(t *testing.T) {
	for _, key := range []string{"", "new-key"} {
		f := newCommandFixture(t)
		before := commandCounts(t, f)
		f.cmd.IdempotencyKey = key
		s := uuid.New()
		f.cmd.SubscriptionID = &s
		if _, e := f.svc.AcquireMovie(context.Background(), f.actor, f.cmd); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
		if before != commandCounts(t, f) {
			t.Fatal("foreign scope wrote records")
		}
	}
}
func TestSelectedMovieActiveMemberSharedRoot(t *testing.T) {
	f, svc, _, c := authorizationSelectionFixture(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID); e != nil {
		t.Fatal(e)
	}
	f.actor.Role = ports.RoleMember
	p, e := svc.Preview(ctx, f.actor, c)
	if e != nil {
		t.Fatal(e)
	}
	r, e := svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: p.InputStamp, Confirmed: true}, "member-selection")
	if e != nil {
		t.Fatal(e)
	}
	again, e := svc.Commit(ctx, f.actor, ports.MovieSelectionCommand{Selection: c, ExpectedInputStamp: p.InputStamp, Confirmed: true}, "member-selection")
	if e != nil || again.OperationID != r.OperationID {
		t.Fatal(e)
	}
}
