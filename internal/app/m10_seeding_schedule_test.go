package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
)

func TestM10SeedingSchedulingNativePG(t *testing.T) {
	runM10SeedingScheduling(t, m10SeedingExecutionLedger)
}

func TestM10SeedingExecutionRepositoryNativePG(t *testing.T) {
	runM10SeedingScheduling(t, m10SeedingExecutionRepository)
}

func runM10SeedingScheduling(t *testing.T, exercise func(*testing.T, *organizationFixture, uuid.UUID, uuid.UUID, uuid.UUID)) {
	runFullMoviePipelineLifecycle(t, false, "", "", false, true, true, func(f *organizationFixture, member *ports.Actor, _ uuid.UUID, _ *App) func() {
		return func() {
			task, _ := waitSeedingOrganization(t, f)
			q := db.New(f.pool)
			original, err := q.GetSubmissionTask(t.Context(), task)
			if err != nil {
				t.Fatal(err)
			}
			client, err := q.GetDownloadClientByID(t.Context(), original.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			admin := uuid.New()
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'seeding-scheduler-admin',password_hash,'admin',true FROM app_user WHERE id=$2`, admin, uuid.MustParse(member.ID)); err != nil {
				t.Fatal(err)
			}
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.String(), Role: ports.RoleAdmin}
			seconds := int64(60)
			cfg := seed.NewConfigService(postgres.NewSeedingConfigStore(f.pool, idgen.New()))
			input := ports.SeedingConfigInput{ExpectedClientUpdatedAt: client.UpdatedAt.Time, Enabled: true, IntervalSeconds: 1200, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}}
			if _, err = cfg.Save(t.Context(), actor, client.ID, input, "schedule-config"); err != nil {
				t.Fatal(err)
			}
			scheduler := postgres.NewSeedingScheduleStore(f.pool, idgen.New())
			count := func() int {
				t.Helper()
				var n int
				if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if err = scheduler.EnqueueDue(t.Context()); err != nil || count() != 0 {
				t.Fatal("saved configuration triggered early scheduling", err)
			}
			// This explicit due-state fixture tests semantics, not wall-clock latency.
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO seeding_removal_schedule(task_id,config_version,next_due_at,state) VALUES($1,1,clock_timestamp()-interval '1 second','idle')`, task); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				return m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "download_task", "download_task_client_binding", "download_requirement", "acquisition_reservation", "unit_reservation", "transfer_plan", "transfer_step", "library_root", "library_file", "library_file_unit", "unit_fact", "media", "media_unit", "media_identity", "subscription", "wanted_item")
			}
			for _, tc := range []struct{ name, breakSQL, restoreSQL string }{
				{"permission", `UPDATE app_user SET is_active=false WHERE id=$1`, `UPDATE app_user SET is_active=true WHERE id=$1`},
				{"pending-demand", `UPDATE download_requirement SET state='pending' WHERE operation_id=$2`, `UPDATE download_requirement SET state='imported' WHERE operation_id=$2`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					prefix := `WITH args AS(SELECT $1::uuid a,$2::uuid b) `
					if _, e := f.pool.Exec(t.Context(), prefix+tc.breakSQL, admin, original.OperationID); e != nil {
						t.Fatal(e)
					}
					e := scheduler.EnqueueDue(t.Context())
					if _, restoreErr := f.pool.Exec(t.Context(), prefix+tc.restoreSQL, admin, original.OperationID); restoreErr != nil {
						t.Fatal(restoreErr)
					}
					if e != nil || count() != 0 {
						t.Fatal("ineligible task scheduled", e)
					}
				})
			}
			for _, failure := range []string{"audit", "event", "late-permission"} {
				t.Run(failure, func(t *testing.T) {
					table, condition := "audit_log", "NEW.action='seeding.removal.queued'"
					body := "RAISE EXCEPTION 'owned schedule fault';"
					if failure != "audit" {
						table = "outbox_event"
						condition = "NEW.event_type='seeding.removal.queued'"
					}
					if failure == "late-permission" {
						body = fmt.Sprintf("UPDATE app_user SET is_active=false WHERE id='%s'::uuid;", admin)
					}
					ddl := fmt.Sprintf(`CREATE FUNCTION m10_seed_queue_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF %s THEN %s END IF; RETURN NEW; END $$; CREATE TRIGGER m10_seed_queue_fault BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION m10_seed_queue_fault();`, condition, body, table)
					if _, e := f.pool.Exec(t.Context(), ddl); e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() {
						_, _ = f.pool.Exec(context.WithoutCancel(t.Context()), fmt.Sprintf(`DROP TRIGGER IF EXISTS m10_seed_queue_fault ON %s; DROP FUNCTION IF EXISTS m10_seed_queue_fault();`, table))
					})
					before := m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_schedule", "seeding_removal_dispatch", "audit_log", "outbox_event", "app_user")
					e := scheduler.EnqueueDue(t.Context())
					if !errors.Is(e, ports.ErrUnavailable) || count() != 0 {
						t.Fatal("schedule tail not safely rolled back", e)
					}
					after := m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_schedule", "seeding_removal_dispatch", "audit_log", "outbox_event", "app_user")
					if before != after {
						t.Fatal("schedule rollback left writes")
					}
				})
			}
			before := snapshot()
			var wg sync.WaitGroup
			results := make([]error, 2)
			start := make(chan struct{})
			for i := range results {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; results[i] = scheduler.EnqueueDue(t.Context()) }(i)
			}
			close(start)
			wg.Wait()
			if results[0] != nil || results[1] != nil || count() != 1 {
				t.Fatal("concurrent scheduling duplicated work", results)
			}
			if snapshot() != before {
				t.Fatal("scheduling modified ingestion graph")
			}
			var job uuid.UUID
			if err = f.pool.QueryRow(t.Context(), `SELECT active_job_id FROM seeding_removal_schedule WHERE task_id=$1`, task).Scan(&job); err != nil {
				t.Fatal(err)
			}
			dispatch, err := q.GetSeedingRemovalDispatch(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			t.Run("physical-execution-ledger", func(t *testing.T) { exercise(t, f, task, job, client.ID) })
			input.ExpectedVersion = 1
			input.Enabled = false
			if _, err = cfg.Save(t.Context(), actor, client.ID, input, "disable-after-queue"); err != nil {
				t.Fatal(err)
			}
			if err = scheduler.EnqueueDue(t.Context()); err != nil || count() != 1 {
				t.Fatal("configuration edit requeued active work", err)
			}
			restored, err := q.GetSeedingRemovalDispatch(t.Context(), job)
			if err != nil || !reflect.DeepEqual(dispatch, restored) {
				t.Fatal("active dispatch changed with configuration", err)
			}
			if _, err = f.pool.Exec(t.Context(), `UPDATE seeding_removal_dispatch SET config_version=2 WHERE job_id=$1`, job); err == nil {
				t.Fatal("dispatch rebound")
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if err = scheduler.EnqueueDue(cancelled); !errors.Is(err, context.Canceled) || count() != 1 {
				t.Fatal("canceled scheduling wrote", err)
			}
			t.Log("M10_SEEDING_SCHEDULE_PG_PASS native_source=REAL due=CONTROLLED concurrent=ONE authority=LIVE_AND_COMMIT tail=FULL_ROLLBACK original_dispatch=IMMUTABLE protected=UNCHANGED network=NONE")
		}
	})
}
