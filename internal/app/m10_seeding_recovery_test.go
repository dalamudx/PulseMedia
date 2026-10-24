package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestM10SeedingReleasedRecoveryNativePG(t *testing.T) {
	runM10SeedingScheduling(t, func(t *testing.T, f *organizationFixture, task, job, client uuid.UUID) {
		m10SeedingRecovery(t, f, task, job, client, false)
	})
}
func TestM10SeedingLostSessionRecoveryNativePG(t *testing.T) {
	runM10SeedingScheduling(t, func(t *testing.T, f *organizationFixture, task, job, client uuid.UUID) {
		m10SeedingRecovery(t, f, task, job, client, true)
	})
}
func m10SeedingRecovery(t *testing.T, f *organizationFixture, task, job, client uuid.UUID, lost bool) {
	t.Helper()
	q := db.New(f.pool)
	scheduler := postgres.NewSeedingScheduleStore(f.pool, idgen.New())
	store := postgres.NewSeedingExecutionStore(f.pool, idgen.New())
	claim := func() ports.JobLease {
		t.Helper()
		j, e := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"seeding_removal"}, Column2: "seeding-recovery-matrix", Column3: 120000})
		if e != nil || j.ID != job {
			t.Fatal("claim original job", e)
		}
		return ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	}
	expire := func() {
		t.Helper()
		if _, e := f.pool.Exec(t.Context(), `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND state='running'`, job); e != nil {
			t.Fatal(e)
		}
	}
	lease := claim()
	if !lost {
		expire()
		if e := scheduler.RecoverExpired(t.Context()); e != nil {
			t.Fatal("pre-execution recovery", e)
		}
		lease = claim()
		if lease.Token != 2 {
			t.Fatal("pre-execution recovery did not preserve original job")
		}
	}
	p, err := store.Acquire(t.Context(), lease)
	if err != nil {
		t.Fatal("physical acquire", err)
	}
	defer p.Close()
	snapshot := p.Snapshot()
	seconds := int64(60)
	o := ports.SeedingRemoteTorrent{InfoHash: snapshot.Target.InfoHash, OperationTag: snapshot.Target.OperationTag, SavePath: snapshot.Target.SavePath, TotalBytes: snapshot.Target.TotalBytes, Facts: rule.Observation{Complete: true, SeedingSeconds: &seconds}}
	if err = p.Reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.Reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.Prepare(t.Context(), o); err != nil {
		t.Fatal("prepare", err)
	}
	original, err := q.GetSeedingRemovalPermit(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := q.LatestSeedingExecution(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	expire()
	before, err := q.GetJobByID(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal("recover while held", err)
	}
	still, err := q.GetJobByID(t.Context(), job)
	if err != nil || !reflect.DeepEqual(before, still) {
		t.Fatal("logical expiry released active physical job", err)
	}
	if lost {
		var stopped bool
		if err = f.pool.QueryRow(t.Context(), `SELECT pg_terminate_backend($1::integer)`, execution.BackendPid).Scan(&stopped); err != nil || !stopped {
			t.Fatal("owned backend termination", err)
		}
		p.Close()
		if err = scheduler.RecoverExpired(t.Context()); err != nil {
			t.Fatal("lost physical session recovery", err)
		}
		state, e := q.GetSeedingRemovalSchedule(t.Context(), task)
		if e != nil || state.State != "review_required" || state.ActiveJobID.Valid {
			t.Fatal("lost physical result not isolated", e)
		}
		j, e := q.GetJobByID(t.Context(), job)
		if e != nil || j.State != "dead" || j.Error.String != "physical_unconfirmed" || j.Attempts >= j.MaxAttempts {
			t.Fatal("lost session not distinguished from attempt exhaustion", e)
		}
		orphan, e := q.UnreleasedSeedingExecution(t.Context(), client)
		if e != nil || !reflect.DeepEqual(orphan, execution) {
			t.Fatal("recovery erased or changed unconfirmed physical evidence", e)
		}
		t.Log("M10_SEEDING_LOST_SESSION_RECOVERY_PG_PASS backend=TERMINATED physical_release=UNCONFIRMED remaining_attempts=UNUSED state=REVIEW original_permit=RETAINED native_IO=NOT_EXECUTED")
	} else {
		p.Close()
		// The original owner records physical completion even after its logical expiry.
		released, e := q.LatestSeedingExecution(t.Context(), job)
		if e != nil || !released.ReleasedAt.Valid {
			t.Fatal("missing joined release", e)
		}
		ddl := `CREATE FUNCTION m10_seed_recovery_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='seeding.removal.recovered' THEN RAISE EXCEPTION 'owned recovery fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER m10_seed_recovery_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION m10_seed_recovery_fault();`
		if _, err = f.pool.Exec(t.Context(), ddl); err != nil {
			t.Fatal(err)
		}
		cleanup := func() {
			_, e := f.pool.Exec(context.WithoutCancel(t.Context()), `DROP TRIGGER IF EXISTS m10_seed_recovery_fault ON outbox_event; DROP FUNCTION IF EXISTS m10_seed_recovery_fault();`)
			if e != nil {
				t.Error(e)
			}
		}
		t.Cleanup(cleanup)
		snap := func() string {
			return m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_permit", "seeding_removal_execution", "seeding_removal_schedule", "seeding_removal_dispatch", "audit_log", "outbox_event")
		}
		baseline := snap()
		if err = scheduler.RecoverExpired(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("recovery tail failure", err)
		}
		current, e := q.GetJobByID(t.Context(), job)
		if e != nil || !reflect.DeepEqual(before, current) || snap() != baseline {
			t.Fatal("recovery tail not fully rolled back", e)
		}
		cleanup()
		if err = scheduler.RecoverExpired(t.Context()); err != nil {
			t.Fatal("known release recovery", err)
		}
		next := claim()
		if next.Token != lease.Token+1 {
			t.Fatal("recovery did not claim new lease")
		}
		observation, e := store.Acquire(t.Context(), next)
		if e != nil {
			t.Fatal("observe-only acquisition", e)
		}
		defer observation.Close()
		v := observation.Snapshot()
		if !v.ObserveOnly || v.Target != snapshot.Target || v.ClientStamp != snapshot.ClientStamp {
			t.Fatal("new lease changed original target")
		}
		if e = observation.Prepare(t.Context(), o); !errors.Is(e, ports.ErrRejected) {
			t.Fatal("recovery granted another mutation", e)
		}
		if e = observation.Reserve(t.Context()); e != nil {
			t.Fatal(e)
		}
		if e = observation.Finish(t.Context(), ports.SeedingExecutionAbsent, nil); e != nil {
			t.Fatal("read-only absence finish", e)
		}
		observation.Close()
		state, e := q.GetSeedingRemovalSchedule(t.Context(), task)
		if e != nil || state.State != "retired" || state.ActiveJobID.Valid {
			t.Fatal("absence did not finish original schedule", e)
		}
		t.Log("M10_SEEDING_RELEASED_RECOVERY_PG_PASS original_job=PRESERVED expired_physical=HELD release=DURABLE tail=FULL_ROLLBACK new_lease=OBSERVE_ONLY absent=RETIRED native_IO=NOT_EXECUTED")
	}
	same, err := q.GetSeedingRemovalPermit(t.Context(), task)
	if err != nil || !reflect.DeepEqual(original, same) {
		t.Fatal("recovery changed permanent permit", err)
	}
	if err = scheduler.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery created another job", err)
	}
}
