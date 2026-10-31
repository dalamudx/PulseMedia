package postgres_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"testing"
	"time"
)

func TestScheduleCooldownProductionQueriesPG(t *testing.T) {
	t.Run("person_version_change", func(t *testing.T) {
		f := newPersonScheduleFixture(t)
		ids := seedPersonScheduleIsolation(t, f)
		c := clock.NewMock(time.Now())
		f.scheduler = postgres.NewPersonFollowSyncStore(f.p, idgen.New(), postgres.NewScheduleLimits(c))
		f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key='person.follow.config:isolation-0'`)
		if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
			t.Fatal(err)
		}
		if err := f.scheduler.EnqueueDue(t.Context()); err != nil {
			t.Fatal("failed version was not cooled", err)
		}
		if f.scheduleCounts(t)[0] != 2 {
			t.Fatal("healthy configuration blocked")
		}
		in := f.input()
		in.ExpectedVersion = 1
		in.Settings.IntervalMinutes = 30
		f.save(t, ids[0], in, "cooldown-repair")
		if err := f.scheduler.EnqueueDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.scheduleCounts(t)[0] != 3 {
			t.Fatal("new version inherited cooldown")
		}
	})
	t.Run("douban_retry", func(t *testing.T) {
		f, owners := seedWishScheduleOwners(t, 3)
		c := clock.NewMock(time.Now())
		f.store = postgres.NewDoubanWishSyncStore(f.p, idgen.New(), postgres.NewScheduleLimits(c))
		f.exec(t, `CREATE FUNCTION cooldown_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM douban_wish_sync_run WHERE id=NEW.run_id AND owner_id='`+owners[0].String()+`') THEN RAISE EXCEPTION 'owned failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER cooldown_fault BEFORE INSERT ON douban_wish_sync_page FOR EACH ROW EXECUTE FUNCTION cooldown_fault()`)
		if err := f.store.EnqueueDue(t.Context()); err == nil {
			t.Fatal("fault not observed")
		}
		f.exec(t, `DROP TRIGGER cooldown_fault ON douban_wish_sync_page`)
		wishNoError(t, f.store.EnqueueDue(t.Context()))
		wishCount(t, f, "SELECT count(*) FROM douban_wish_sync_run", 2)
		c.Set(c.Now().Add(5 * time.Second))
		wishNoError(t, f.store.EnqueueDue(t.Context()))
		wishCount(t, f, "SELECT count(*) FROM douban_wish_sync_run", 3)
	})
	t.Run("cookiecloud_retry_and_rebuild", func(t *testing.T) {
		f, _ := cloudIsolationFixture(t, 3, func(i int) bool { return i == 1 })
		c := clock.NewMock(time.Now())
		f.runtime = postgres.NewCookieCloudSyncStore(f.p, idgen.New(), f.secret, f.secret, postgres.NewScheduleLimits(c))
		if err := f.runtime.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(err)
		}
		if err := f.runtime.EnqueueDue(t.Context()); err != nil {
			t.Fatal("failed owner not cooled", err)
		}
		cloudQueueGraph(t, f, 2)
		c.Set(c.Now().Add(5 * time.Second))
		if err := f.runtime.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("due owner not retried", err)
		}
		fresh := postgres.NewCookieCloudSyncStore(f.p, idgen.New(), f.secret, f.secret, postgres.NewScheduleLimits(c))
		if err := fresh.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("restart did not recheck database", err)
		}
		cloudQueueGraph(t, f, 2)
	})
}
