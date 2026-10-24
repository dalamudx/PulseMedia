package postgres_test

import (
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

type personScheduleFixture struct {
	personFollowFixture
	history123 string
	scheduler  *postgres.PersonFollowSyncStore
}

func newPersonScheduleFixture(t *testing.T) personScheduleFixture {
	t.Helper()
	f := newPersonFollowFixture(t)
	history := repositoryMigrationsThrough(t, 123)
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), history); err != nil || version != 123 {
		t.Fatal(version, err)
	}
	return personScheduleFixture{f, history, postgres.NewPersonFollowSyncStore(f.p, idgen.New())}
}
func (f personScheduleFixture) schedule(t *testing.T) {
	t.Helper()
	if err := f.scheduler.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func (f personScheduleFixture) scheduleCounts(t *testing.T) []int {
	t.Helper()
	counts := make([]int, 4)
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM person_follow_sync_run),(SELECT count(*) FROM person_follow_sync_page),(SELECT count(*) FROM job WHERE kind='person_follow_sync'),(SELECT count(*) FROM job WHERE kind<>'person_follow_sync')`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
		t.Fatal(err)
	}
	return counts
}
func TestPersonFollowScheduleConcurrentAndFrozenPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	id := uuid.New()
	input := f.input()
	original := f.save(t, id, input, "original")
	before := f.counts(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.scheduler.EnqueueDue(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{1, 1, 1, 0}) {
		t.Fatal("concurrent schedule duplicated work", got)
	}
	var matches bool
	if err := f.p.QueryRow(t.Context(), `SELECT r.owner_id=$1::uuid AND r.config_version=1 AND r.config_snapshot=h.snapshot AND r.status='queued' AND p.page_start=0 AND NOT p.finished AND j.state='queued' AND j.max_attempts=3 AND j.payload=jsonb_build_object('version',1,'run_id',r.id,'start',0) FROM person_follow_sync_run r JOIN person_follow_sync_page p ON p.run_id=r.id JOIN job j ON j.id=p.job_id JOIN person_follow_config_history h ON h.config_id=r.config_id AND h.version=r.config_version`, f.actor.ID).Scan(&matches); err != nil || !matches {
		t.Fatal("dispatch lost original evidence", err)
	}
	input.ExpectedVersion = 1
	input.Settings.IntervalMinutes = 30
	updated := f.save(t, id, input, "changed")
	if updated.Config.Version != 2 {
		t.Fatal("configuration did not advance")
	}
	f.schedule(t)
	var snapshot []byte
	var version int64
	if err := f.p.QueryRow(t.Context(), `SELECT config_version,config_snapshot FROM person_follow_sync_run`).Scan(&version, &snapshot); err != nil || version != original.Config.Version {
		t.Fatal("active dispatch rebound", err)
	}
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{1, 1, 1, 0}) {
		t.Fatal("configuration change scheduled overlapping work", got)
	}
	after := f.counts(t)
	if after[5] != before[5]+1 || after[6] != before[6] || after[7] != before[7] || after[8] != before[8] || after[9] != before[9] {
		t.Fatal("scheduling wrote business state", after)
	}
}
func TestPersonFollowSchedule65ConfigPaginationPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	for i := 0; i < 65; i++ {
		input := f.input()
		input.Settings.PersonID += int64(i)
		f.save(t, uuid.New(), input, fmt.Sprintf("follow-%d", i))
	}
	before := f.counts(t)
	f.schedule(t)
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{64, 64, 64, 0}) {
		t.Fatal("first bounded page", got)
	}
	f.schedule(t)
	f.schedule(t)
	if got := f.scheduleCounts(t); !reflect.DeepEqual(got, []int{65, 65, 65, 0}) {
		t.Fatal("missing or duplicate continuation", got)
	}
	var exact bool
	if err := f.p.QueryRow(t.Context(), `SELECT count(*)=65 AND count(DISTINCT r.config_id)=65 AND bool_and(r.owner_id=c.owner_id AND r.config_version=c.version AND r.config_snapshot=h.snapshot AND j.payload=jsonb_build_object('version',1,'run_id',r.id,'start',0)) FROM person_follow_sync_run r JOIN person_follow_config c ON c.id=r.config_id JOIN person_follow_config_history h ON h.config_id=r.config_id AND h.version=r.config_version JOIN person_follow_sync_page p ON p.run_id=r.id JOIN job j ON j.id=p.job_id`).Scan(&exact); err != nil || !exact {
		t.Fatal("binding or task pagination mismatch", err)
	}
	after := f.counts(t)
	for _, i := range []int{0, 1, 2, 3, 4, 6, 7, 8, 9} {
		if before[i] != after[i] {
			t.Fatal("pagination changed protected ledger", i)
		}
	}
	t.Log("M11_PERSON_SCHEDULE_65_PASS tasks=65 pages=64+1 queued=ONCE evidence=FROZEN external=ZERO execution=NOT_STARTED")
}
func TestPersonFollowScheduleEligibilityAndIntervalsPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	id := uuid.New()
	input := f.input()
	input.Settings.Bindings = f.bindings(t)
	f.save(t, id, input, "original")
	site := input.Settings.Bindings.IndexerIDs[0]
	f.exec(t, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, site)
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 0 {
		t.Fatal("disabled endpoint scheduled")
	}
	f.exec(t, `UPDATE indexer_lifecycle SET enabled=true WHERE indexer_id=$1`, site)
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 0 {
		t.Fatal("revoked owner scheduled")
	}
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, f.actor.ID)
	f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 0 {
		t.Fatal("offline root scheduled")
	}
	f.exec(t, `UPDATE library_root SET health='online' WHERE id=$1`, f.root)
	f.schedule(t)
	f.exec(t, `UPDATE person_follow_sync_run SET status='succeeded',finished_at=clock_timestamp()`)
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 1 {
		t.Fatal("finished interval ignored")
	}
	input.ExpectedVersion = 1
	input.Settings.Enabled = false
	f.save(t, id, input, "disable")
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 1 {
		t.Fatal("disabled configuration scheduled")
	}
	input.ExpectedVersion = 2
	input.Settings.Enabled = true
	f.save(t, id, input, "enable-new-version")
	f.schedule(t)
	if f.scheduleCounts(t)[0] != 2 {
		t.Fatal("new enabled version not scheduled")
	}
}
func TestPersonFollowScheduleTailRollbackPG(t *testing.T) {
	for _, table := range []string{"person_follow_sync_run", "job", "person_follow_sync_page"} {
		t.Run(table, func(t *testing.T) {
			f := newPersonScheduleFixture(t)
			f.save(t, uuid.New(), f.input(), "original")
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_follow_schedule_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned private schedule'; END $$`)
			f.exec(t, "CREATE TRIGGER person_follow_schedule_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION person_follow_schedule_fault()")
			if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("tail failure not classified", err)
			}
			if !reflect.DeepEqual(f.counts(t), before) || !reflect.DeepEqual(f.scheduleCounts(t), []int{0, 0, 0, 0}) {
				t.Fatal("tail failure committed partial dispatch")
			}
			f.exec(t, "DROP TRIGGER person_follow_schedule_fault ON "+table)
			f.schedule(t)
			if !reflect.DeepEqual(f.scheduleCounts(t), []int{1, 1, 1, 0}) {
				t.Fatal("tail rollback prevented scheduling")
			}
		})
	}
}
func TestPersonFollowScheduleCommitAuthorizationPG(t *testing.T) {
	for _, mutation := range []string{"UPDATE app_user SET is_active=false", "UPDATE person_follow_config SET enabled=false,version=version+1,updated_at=clock_timestamp()", "UPDATE library_root SET health='offline'", "UPDATE policy SET current_revision=current_revision+1"} {
		t.Run(fmt.Sprintf("case-%d", len(mutation)), func(t *testing.T) {
			f := newPersonScheduleFixture(t)
			f.save(t, uuid.New(), f.input(), "original")
			before := f.counts(t)
			f.exec(t, `CREATE FUNCTION person_follow_schedule_late() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+`; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER person_follow_schedule_late AFTER INSERT ON person_follow_sync_page FOR EACH ROW EXECUTE FUNCTION person_follow_schedule_late()`)
			if err := f.scheduler.EnqueueDue(t.Context()); err == nil {
				t.Fatal("late authorization accepted")
			}
			if !reflect.DeepEqual(f.counts(t), before) || !reflect.DeepEqual(f.scheduleCounts(t), []int{0, 0, 0, 0}) {
				t.Fatal("late rejection did not roll back")
			}
			f.exec(t, `DROP TRIGGER person_follow_schedule_late ON person_follow_sync_page`)
			f.schedule(t)
			if f.scheduleCounts(t)[0] != 1 {
				t.Fatal("late rollback damaged original configuration")
			}
		})
	}
}
func TestPersonFollowScheduleOriginalLedgerAndHistoryPG(t *testing.T) {
	f := newPersonScheduleFixture(t)
	if version, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history123); err != nil || version != 122 {
		t.Fatal("empty downgrade", version, err)
	}
	if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing contract treated as no work", err)
	}
	if version, err := postgres.Migrate(t.Context(), f.p.Config().ConnString(), f.history123); err != nil || version != 123 {
		t.Fatal(version, err)
	}
	id := uuid.New()
	f.save(t, id, f.input(), "original")
	f.exec(t, `DELETE FROM idempotency_record WHERE idempotency_key='person.follow.config:original'`)
	if err := f.scheduler.EnqueueDue(t.Context()); !errors.Is(err, ports.ErrPersonFollowConflict) {
		t.Fatal("missing configuration receipt scheduled", err)
	}
	if f.scheduleCounts(t)[0] != 0 {
		t.Fatal("missing original ledger created a task")
	}
	input := f.input()
	input.ExpectedVersion = 1
	f.save(t, id, input, "new-configuration")
	f.schedule(t)
	var run, job uuid.UUID
	if err := f.p.QueryRow(t.Context(), `SELECT run_id,job_id FROM person_follow_sync_page`).Scan(&run, &job); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE person_follow_sync_run SET config_version=config_version+1`, `UPDATE person_follow_sync_run SET config_snapshot='{}'::jsonb`, `DELETE FROM person_follow_sync_run`, `TRUNCATE person_follow_sync_run CASCADE`, `UPDATE person_follow_sync_page SET page_start=64`, `DELETE FROM person_follow_sync_page`} {
		if _, err := f.p.Exec(t.Context(), query); err == nil {
			t.Fatal("dispatch history rewritten")
		}
	}
	if _, err := postgres.Rollback(t.Context(), f.p.Config().ConnString(), f.history123); err == nil {
		t.Fatal("downgrade discarded persistent jobs")
	}
	if _, err := db.New(f.p).ReadPersonFollowConfig(t.Context(), db.ReadPersonFollowConfigParams{ID: id, OwnerID: uuid.MustParse(f.actor.ID)}); err != nil {
		t.Fatal("history protection broke configuration", err)
	}
	if !reflect.DeepEqual(f.scheduleCounts(t), []int{1, 1, 1, 0}) {
		t.Fatal("history test damaged dispatch")
	}
}
