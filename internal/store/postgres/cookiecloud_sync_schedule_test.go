package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type cloudScheduleFixture struct {
	cookieConfigFixture
	runtime           *postgres.CookieCloudSyncStore
	runtimeMigrations string
}

func newCloudScheduleFixture(t *testing.T) cloudScheduleFixture {
	t.Helper()
	base := newCookieConfigFixture(t)
	history := repositoryMigrationsThrough(t, 109)
	if version, err := postgres.Migrate(t.Context(), base.dsn, history); err != nil || version != 109 {
		t.Fatal("runtime migration", version, err)
	}
	return cloudScheduleFixture{base, postgres.NewCookieCloudSyncStore(base.p, idgen.New(), base.secret, base.secret), history}
}
func (f cloudScheduleFixture) seed(t *testing.T, a *ports.Actor) {
	t.Helper()
	raw, err := json.Marshal(cookieConfigInput().Connection)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	cipher, err := f.secret.EncryptSecret(raw)
	if err != nil {
		t.Fatal(err)
	}
	// A legitimate old configuration establishes due-time without changing any
	// immutable row or disabling its protection triggers.
	f.exec(t, `INSERT INTO cookiecloud_configuration(owner_id,version,enabled,interval_seconds,server_origin,connection_encrypted,created_at,updated_at) VALUES($1,1,true,60,'https://cloud.example',$2,clock_timestamp()-interval '1 day',clock_timestamp()-interval '1 day')`, a.ID, cipher)
}
func (f cloudScheduleFixture) enqueue(t *testing.T) {
	t.Helper()
	if err := f.runtime.EnqueueDue(t.Context()); err != nil {
		t.Fatal("durable due scheduler", err)
	}
}

func TestCookieCloudSyncConcurrentQueuePG(t *testing.T) {
	f := newCloudScheduleFixture(t)
	f.seed(t, f.owner)
	before := f.cipher(t)
	baseline := f.snapshot(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.runtime.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent enqueue", err)
		}
	}
	for _, table := range []string{"job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule", "audit_log", "outbox_event"} {
		if got := f.count(t, table); got != 1 {
			t.Fatal("queue graph not singular", table, got)
		}
	}
	var bound bool
	if err := f.p.QueryRow(t.Context(), `SELECT j.kind='cookiecloud_sync' AND j.max_attempts=1 AND j.attempts=0 AND j.state='queued' AND j.payload=jsonb_build_object('owner_id',a.owner_id,'config_version',a.config_version) AND s.active_job_id=j.id AND s.state='queued' FROM job j JOIN cookiecloud_sync_attempt a ON a.job_id=j.id JOIN cookiecloud_sync_schedule s ON s.owner_id=a.owner_id`).Scan(&bound); err != nil || !bound {
		t.Fatal("initial authorization graph", err)
	}
	if !bytes.Equal(before, f.cipher(t)) {
		t.Fatal("scheduling replaced encrypted connection")
	}
	for _, table := range []string{"subscription", "wanted_item", "download_task", "library_file", "library_file_unit", "idempotency_record"} {
		if f.count(t, table) != 0 {
			t.Fatal("scheduling crossed business boundary", table)
		}
	}
	after := f.snapshot(t)
	for _, allowed := range []string{"job", "cookiecloud_sync_attempt", "cookiecloud_sync_schedule", "audit_log", "outbox_event"} {
		delete(baseline, allowed)
		delete(after, allowed)
	}
	if !reflect.DeepEqual(baseline, after) {
		t.Fatal("scheduling changed protected non-authentication business rows")
	}
}

func TestCookieCloudSyncDueAnd65OwnerPaginationPG(t *testing.T) {
	t.Run("new-save-is-not-due", func(t *testing.T) {
		f := newCloudScheduleFixture(t)
		f.save(t, cookieConfigInput(), "only-save")
		before := f.snapshot(t)
		f.enqueue(t)
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("saving configuration immediately enqueued work")
		}
	})
	t.Run("64-plus-1", func(t *testing.T) {
		f := newCloudScheduleFixture(t)
		for i := range 65 {
			a := f.owner
			if i > 0 {
				a = f.user(t, fmt.Sprintf("cloud-owner-%02d", i))
			}
			f.seed(t, a)
		}
		f.enqueue(t)
		if f.count(t, "job") != 64 {
			t.Fatal("first batch not bounded at 64")
		}
		f.enqueue(t)
		if f.count(t, "job") != 65 || f.count(t, "cookiecloud_sync_attempt") != 65 {
			t.Fatal("due owners starved or duplicated")
		}
		before := f.snapshot(t)
		f.enqueue(t)
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("active owner enqueued again")
		}
	})
}

func TestCookieCloudSyncPhysicalRecoveryAndConfigChangesPG(t *testing.T) {
	f := newCloudScheduleFixture(t)
	f.seed(t, f.owner)
	f.enqueue(t)
	var job uuid.UUID
	if err := f.p.QueryRow(t.Context(), "SELECT job_id FROM cookiecloud_sync_attempt").Scan(&job); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE job SET state='running',attempts=1,lease_owner='owned-native-reader',lease_token=1,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job)
	conn, err := f.p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err = db.New(conn).AcquireCookieCloudSyncGate(t.Context(), uuid.MustParse(f.owner.ID)); err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			ctx, cancel := context.WithTimeout(context.Background(), 2e9)
			defer cancel()
			_, _ = db.New(conn).ReleaseCookieCloudSyncGate(ctx, uuid.MustParse(f.owner.ID))
		}
	}()
	before := f.snapshot(t)
	if err = f.runtime.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("logical expiry released physically active attempt")
	}
	input := ports.CookieCloudConfigInput{ExpectedVersion: 1, Enabled: true, IntervalSeconds: 60}
	f.save(t, input, "changed-config")
	f.enqueue(t)
	if f.count(t, "job") != 1 {
		t.Fatal("configuration edit bypassed physical execution")
	}
	owned, err := db.New(conn).ReleaseCookieCloudSyncGate(t.Context(), uuid.MustParse(f.owner.ID))
	released = true
	if err != nil || !owned {
		t.Fatal("release owned gate", err)
	}
	if err = f.runtime.RecoverExpired(t.Context()); err != nil {
		t.Fatal("unknown recovery", err)
	}
	var state, schedule, jobState string
	if err = f.p.QueryRow(t.Context(), `SELECT a.state,s.state,j.state FROM cookiecloud_sync_attempt a JOIN cookiecloud_sync_schedule s ON s.owner_id=a.owner_id JOIN job j ON j.id=a.job_id WHERE a.job_id=$1`, job).Scan(&state, &schedule, &jobState); err != nil || state != "review_required" || schedule != "review_required" || jobState != "dead" {
		t.Fatal("unknown outcome not protected", state, schedule, jobState, err)
	}
	before = f.snapshot(t)
	f.enqueue(t)
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("configuration version reset unknown execution protection")
	}
}

func TestCookieCloudSyncQueueTailAndAuthorityRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "cookiecloud_sync_schedule", "permission"} {
		t.Run(table, func(t *testing.T) {
			f := newCloudScheduleFixture(t)
			f.seed(t, f.owner)
			if table == "permission" {
				f.exec(t, `CREATE FUNCTION cloud_sync_invalidate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE app_user SET role='member' WHERE id='`+f.owner.ID+`'::uuid; RETURN NEW; END $$; CREATE TRIGGER cloud_sync_invalidate AFTER INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION cloud_sync_invalidate();`)
			} else {
				f.exec(t, `CREATE FUNCTION cloud_sync_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned cloud sync queue tail'; END $$; CREATE TRIGGER cloud_sync_fail BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION cloud_sync_fail();`)
			}
			before := f.snapshot(t)
			if err := f.runtime.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failed to reject queue atomically", err)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("partial queue/authority tail committed")
			}
			f.exec(t, "DROP TRIGGER cloud_sync_"+map[bool]string{true: "invalidate", false: "fail"}[table == "permission"]+" ON "+map[bool]string{true: "outbox_event", false: table}[table == "permission"])
			f.enqueue(t)
		})
	}
}

func TestCookieCloudSyncSchema109HistoryPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newCloudScheduleFixture(t)
		if version, err := postgres.Rollback(t.Context(), f.dsn, f.runtimeMigrations); err != nil || version != 108 {
			t.Fatal("runtime empty downgrade", version, err)
		}
		if version, err := postgres.Migrate(t.Context(), f.dsn, f.runtimeMigrations); err != nil || version != 109 {
			t.Fatal("runtime reinstallation", version, err)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newCloudScheduleFixture(t)
		f.seed(t, f.owner)
		f.enqueue(t)
		before := f.snapshot(t)
		if _, err := postgres.Rollback(t.Context(), f.dsn, f.runtimeMigrations); err == nil {
			t.Fatal("runtime history dropped")
		}
		if _, err := f.p.Exec(t.Context(), "DELETE FROM cookiecloud_sync_attempt"); err == nil {
			t.Fatal("execution history deleted")
		}
		if _, err := f.p.Exec(t.Context(), "TRUNCATE cookiecloud_sync_attempt"); err == nil {
			t.Fatal("execution history truncated")
		}
		if !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("failed downgrade changed runtime facts")
		}
	})
}
