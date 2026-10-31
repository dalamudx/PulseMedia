package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func seedWishScheduleOwners(t *testing.T, count int) (*wishFixture, []uuid.UUID) {
	t.Helper()
	f := newDoubanSyncFixture(t)
	owners := make([]uuid.UUID, count)
	for i := range count {
		owners[i] = uuid.MustParse(fmt.Sprintf("10000000-0000-0000-0000-%012d", i+1))
		_, err := postgres.Queries(f.p).CreateUser(t.Context(), db.CreateUserParams{ID: owners[i], Username: fmt.Sprintf("schedule-%d", i), PasswordHash: "fixture", Role: "admin", IsActive: true})
		wishNoError(t, err)
		in := f.input()
		in.TVRootID = nil
		_, err = f.s.SaveDoubanSyncConfig(t.Context(), &ports.Actor{Type: ports.ActorTypeUser, ID: owners[i].String(), Role: "admin"}, in, "schedule-initial")
		wishNoError(t, err)
	}
	return &wishFixture{f, postgres.NewDoubanWishSyncStore(f.p, idgen.New())}, owners
}

func TestDoubanWishScheduleFailureIsolationPG(t *testing.T) {
	f, owners := seedWishScheduleOwners(t, 3)
	f.exec(t, fmt.Sprintf(`CREATE FUNCTION wish_schedule_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM douban_wish_sync_run WHERE id=NEW.run_id AND owner_id='%s') THEN RAISE EXCEPTION 'fixture-private-tail'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER wish_schedule_fault BEFORE INSERT ON douban_wish_sync_page FOR EACH ROW EXECUTE FUNCTION wish_schedule_fault()`, owners[0]))
	for range 2 {
		err := f.store.EnqueueDue(t.Context())
		if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("failure lost or exposed", err)
		}
		for _, table := range []string{"douban_wish_sync_run", "douban_wish_sync_page", "job"} {
			wishCount(t, f, "SELECT count(*) FROM "+table, 2)
		}
		wishCount(t, f, fmt.Sprintf("SELECT count(*) FROM douban_wish_sync_run WHERE owner_id='%s'", owners[0]), 0)
	}
	f.exec(t, "DROP TRIGGER wish_schedule_fault ON douban_wish_sync_page")
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	for _, table := range []string{"douban_wish_sync_run", "douban_wish_sync_page", "job"} {
		wishCount(t, f, "SELECT count(*) FROM "+table, 3)
	}
}

func TestDoubanWishScheduleOfflinePrefixPG(t *testing.T) {
	f, owners := seedWishScheduleOwners(t, 66)
	healthyRoot := uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type) VALUES($1,'/wish-schedule-online','online','movie')`, healthyRoot)
	for _, owner := range owners[64:] {
		in := f.input()
		in.TVRootID = nil
		in.MovieRootID = &healthyRoot
		in.ExpectedVersion = 1
		_, err := f.s.SaveDoubanSyncConfig(t.Context(), &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: "admin"}, in, "online-target")
		wishNoError(t, err)
	}
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.movie)
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	wishCount(t, f, `SELECT count(*) FROM job`, 0)
	f.store = postgres.NewDoubanWishSyncStore(f.p, idgen.New())
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	wishCount(t, f, `SELECT count(*) FROM job`, 0)
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	wishCount(t, f, `SELECT count(*) FROM job`, 2)
	f.exec(t, `UPDATE library_root SET health='online' WHERE id=$1`, f.movie)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.store.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		wishNoError(t, err)
	}
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	for _, table := range []string{"douban_wish_sync_run", "douban_wish_sync_page", "job"} {
		wishCount(t, f, "SELECT count(*) FROM "+table, 66)
	}
}

func TestDoubanWishScheduleBusyAndCancellationPG(t *testing.T) {
	f, owners := seedWishScheduleOwners(t, 3)
	blocker, err := f.p.Begin(t.Context())
	wishNoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = blocker.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, owners[1])
	wishNoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() { defer close(done); result <- f.store.EnqueueDue(ctx) }()
	defer func() { cancel(); _ = blocker.Rollback(context.Background()); <-done }()
	waitPreparationBlock(t, f.p, blocker.Conn().PgConn().PID())
	busy, stopBusy := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer stopBusy()
	wishNoError(t, f.store.EnqueueDue(busy))
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if err = f.store.EnqueueDue(stopped); !errors.Is(err, context.Canceled) {
		t.Fatal("busy caller cancellation lost", err)
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("scan cancellation lost", err)
	}
	wishCount(t, f, `SELECT count(*) FROM job`, 1)
	wishNoError(t, blocker.Rollback(context.Background()))
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	wishCount(t, f, `SELECT count(*) FROM job`, 3)
}

func TestDoubanWishScheduleLockBudgetPG(t *testing.T) {
	f, owners := seedWishScheduleOwners(t, 3)
	blocker, err := f.p.Begin(t.Context())
	wishNoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = blocker.Exec(t.Context(), `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, owners[0])
	wishNoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err = f.store.EnqueueDue(ctx)
	if !errors.Is(err, ports.ErrUnavailable) || ctx.Err() != nil {
		t.Fatal("one lock exhausted the batch context", err, ctx.Err())
	}
	wishCount(t, f, `SELECT count(*) FROM job`, 2)
	wishNoError(t, blocker.Rollback(context.Background()))
	wishNoError(t, f.store.EnqueueDue(t.Context()))
	wishCount(t, f, `SELECT count(*) FROM job`, 3)
}
