package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func m10SeedingExecutionRepository(t *testing.T, f *organizationFixture, task, job, client uuid.UUID) {
	t.Helper()
	q := db.New(f.pool)
	claim := func(owner string) ports.JobLease {
		t.Helper()
		j, e := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"seeding_removal"}, Column2: owner, Column3: 120000})
		if e != nil || j.ID != job {
			t.Fatal("claim original task", e)
		}
		return ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	}
	lease := claim("seeding-repository-first")
	store := postgres.NewSeedingExecutionStore(f.pool, idgen.New())
	p, err := store.Acquire(t.Context(), lease)
	if err != nil {
		t.Fatal("acquire", err)
	}
	defer p.Close()
	if other, e := store.Acquire(t.Context(), lease); e == nil {
		other.Close()
		t.Fatal("concurrent physical execution allowed")
	}
	snapshot := p.Snapshot()
	if snapshot.ObserveOnly || !snapshot.Protection.Owned || !snapshot.Protection.Imported || snapshot.Protection.PendingWork {
		t.Fatal("invalid initial protection")
	}
	if snapshot.Rule.MinSeedingSeconds == nil {
		t.Fatal("missing rule")
	}
	*snapshot.Rule.MinSeedingSeconds = 999999
	if *p.Snapshot().Rule.MinSeedingSeconds != 60 {
		t.Fatal("rule snapshot alias")
	}
	snapshot = p.Snapshot()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if e := p.Check(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled check", e)
	}
	if e := p.Check(t.Context()); e != nil {
		t.Fatal("cancellation discarded physical session", e)
	}
	seconds := int64(60)
	observed := ports.SeedingRemoteTorrent{InfoHash: snapshot.Target.InfoHash, OperationTag: snapshot.Target.OperationTag, SavePath: snapshot.Target.SavePath, TotalBytes: snapshot.Target.TotalBytes, Facts: rule.Observation{Complete: true, SeedingSeconds: &seconds}}
	if e := p.Prepare(t.Context(), observed); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("unreserved observation prepared", e)
	}
	if e := p.Finish(t.Context(), ports.SeedingExecutionAcknowledged, nil); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("acknowledged without permit", e)
	}
	if _, e := q.GetSeedingRemovalPermit(t.Context(), task); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("unpermitted finish wrote permit", e)
	}
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := p.Prepare(t.Context(), observed); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("unreserved mutation prepared", e)
	}
	if e := p.Reserve(t.Context()); e != nil {
		t.Fatal(e)
	}
	changed := observed
	changed.SavePath += "/changed"
	if e := p.Prepare(t.Context(), changed); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("changed native identity prepared", e)
	}
	if e := p.Prepare(t.Context(), observed); e != nil {
		t.Fatal("prepare", e)
	}
	original, err := q.GetSeedingRemovalPermit(t.Context(), task)
	if err != nil || original.JobID != job || original.LeaseToken != lease.Token || original.LeaseOwner != lease.Owner {
		t.Fatal("permanent permit binding", err)
	}
	if e := p.Prepare(t.Context(), observed); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("permanent permission repeated", e)
	}
	ddl := `CREATE FUNCTION m10_seed_finish_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='seeding.removal.finished' THEN RAISE EXCEPTION 'owned finish fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER m10_seed_finish_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION m10_seed_finish_fault();`
	if _, err = f.pool.Exec(t.Context(), ddl); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_, e := f.pool.Exec(context.WithoutCancel(t.Context()), `DROP TRIGGER IF EXISTS m10_seed_finish_fault ON outbox_event; DROP FUNCTION IF EXISTS m10_seed_finish_fault();`)
		if e != nil {
			t.Error(e)
		}
	}
	t.Cleanup(cleanup)
	before := m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_permit", "seeding_removal_execution", "seeding_removal_schedule", "audit_log", "outbox_event")
	if e := p.Finish(t.Context(), ports.SeedingExecutionAcknowledged, nil); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("finish tail not rejected", e)
	}
	after := m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_permit", "seeding_removal_execution", "seeding_removal_schedule", "audit_log", "outbox_event")
	if before != after {
		t.Fatal("finish failure left partial writes")
	}
	cleanup()
	if e := p.Finish(t.Context(), ports.SeedingExecutionUnknown, ports.NewRateLimitError(300500*time.Millisecond, "owned rate-limit detail")); e != nil {
		t.Fatal("unknown outcome", e)
	}
	var deferred bool
	if err = f.pool.QueryRow(t.Context(), `SELECT j.not_before >= e.finished_at+interval '301 seconds' AND j.not_before < e.finished_at+interval '310 seconds' AND s.next_due_at >= e.finished_at+interval '1200 seconds' AND j.error='seeding native observation unavailable' FROM job j JOIN seeding_removal_execution e ON e.job_id=j.id AND e.lease_token=j.lease_token JOIN seeding_removal_schedule s ON s.active_job_id=j.id WHERE j.id=$1`, job).Scan(&deferred); err != nil || !deferred {
		t.Fatal("original observation recovery ignored bounded Retry-After or leaked error", err)
	}
	p.Close()
	released, err := q.LatestSeedingExecution(t.Context(), job)
	if err != nil || !released.ReleasedAt.Valid || released.Result.String != "unknown" {
		t.Fatal("missing release after joined operation", err)
	}
	// Controlled due time avoids a ten-second wait; it is not latency evidence.
	if _, err = f.pool.Exec(t.Context(), `UPDATE job SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1 AND state='queued'`, job); err != nil {
		t.Fatal(err)
	}
	recoveryLease := claim("seeding-repository-recovery")
	if recoveryLease.Token <= lease.Token {
		t.Fatal("recovery reused lease")
	}
	recovery, err := store.Acquire(t.Context(), recoveryLease)
	if err != nil {
		t.Fatal("read-only recovery", err)
	}
	defer recovery.Close()
	recovered := recovery.Snapshot()
	if !recovered.ObserveOnly || recovered.Target != snapshot.Target || recovered.ClientID != client || recovered.ClientStamp != snapshot.ClientStamp {
		t.Fatal("recovery changed original target")
	}
	if e := recovery.Prepare(t.Context(), observed); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("recovery prepared another write", e)
	}
	if e := recovery.Reserve(t.Context()); e != nil {
		t.Fatal("recovery observation", e)
	}
	if e := recovery.Reserve(t.Context()); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("recovery allowed another request", e)
	}
	if e := recovery.Finish(t.Context(), ports.SeedingExecutionPresent, nil); e != nil {
		t.Fatal("present result", e)
	}
	recovery.Close()
	same, err := q.GetSeedingRemovalPermit(t.Context(), task)
	if err != nil || !reflect.DeepEqual(original, same) {
		t.Fatal("recovery rebound original permission", err)
	}
	s, err := q.GetSeedingRemovalSchedule(t.Context(), task)
	if err != nil || s.State != "review_required" || s.ActiveJobID.Valid {
		t.Fatal("present original did not require review", err)
	}
	if e := postgres.NewSeedingScheduleStore(f.pool, idgen.New()).EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	var jobs int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("permanent permit scheduled new work", err)
	}
	t.Log("M10_SEEDING_REPOSITORY_PG_PASS claim=REAL_QUEUE physical=EXCLUSIVE prepare=ONCE finish_tail=FULL_ROLLBACK recovery=ORIGINAL_OBSERVE_ONLY present=REVIEW native_IO=NOT_EXECUTED")
}
