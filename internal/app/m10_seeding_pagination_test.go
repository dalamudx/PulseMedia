package app

import (
	"context"
	"fmt"
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

// One real native ingestion supplies the physical evidence. The 65 additional
// independent SQL graphs test scheduling structure, not 65 native downloads.
func TestM10SeedingScheduling65IndependentTasksPG(t *testing.T) {
	runFullMoviePipelineLifecycle(t, false, "", "", false, true, true, func(f *organizationFixture, member *ports.Actor, _ uuid.UUID, _ *App) func() {
		return func() {
			original, stamp := waitSeedingOrganization(t, f)
			q := db.New(f.pool)
			source, err := q.GetSubmissionTask(t.Context(), original)
			if err != nil {
				t.Fatal(err)
			}
			client, err := q.GetDownloadClientByID(t.Context(), source.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			admin := uuid.New()
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::uuid,'username','seeding-page-admin','role','admin','is_active',true))).* FROM app_user u WHERE u.id=$2`, admin, uuid.MustParse(member.ID)); err != nil {
				t.Fatal(err)
			}
			seconds := int64(60)
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.String(), Role: ports.RoleAdmin}
			input := ports.SeedingConfigInput{ExpectedClientUpdatedAt: client.UpdatedAt.Time, Enabled: true, IntervalSeconds: 1200, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}}
			if _, err = seed.NewConfigService(postgres.NewSeedingConfigStore(f.pool, idgen.New())).Save(t.Context(), actor, client.ID, input, "page-config"); err != nil {
				t.Fatal(err)
			}
			expected := make(map[uuid.UUID]uuid.UUID, 65)
			for i := 1; i <= 65; i++ {
				task, op := m10SeedingPageGraph(t, f, original, i)
				expected[task] = op
			}
			for task := range expected {
				proof, e := q.ReadOrdinarySeedingProtection(t.Context(), db.ReadOrdinarySeedingProtectionParams{TaskID: task, ClientStamp: stamp})
				if e != nil || !proof.Owned || !proof.Imported || proof.PendingWork {
					t.Fatal("independent graph lacks admission proof", task, proof, e)
				}
			}
			protected := func() string {
				return m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "download_task", "download_task_client_binding", "download_requirement", "release_candidate", "acquisition_reservation", "unit_reservation", "transfer_plan", "transfer_step", "library_root", "library_file", "library_file_unit", "unit_fact", "media", "media_unit", "media_identity", "subscription", "wanted_item")
			}
			receipts := func() string {
				t.Helper()
				var raw string
				if e := f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY j.id),'[]')::text FROM job j WHERE kind<>'seeding_removal'`).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				return raw
			}
			baseline, originalReceipts := protected(), receipts()
			first, err := q.ListDueSeedingTasks(t.Context())
			if err != nil || len(first) != 64 {
				t.Fatal("first page", len(first), err)
			}
			seen := map[uuid.UUID]bool{}
			for _, row := range first {
				if seen[row.TaskID] || expected[row.TaskID] != row.OperationID {
					t.Fatal("first page changed identity")
				}
				seen[row.TaskID] = true
			}
			scheduler := postgres.NewSeedingScheduleStore(f.pool, idgen.New())
			if err = scheduler.EnqueueDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM seeding_removal_dispatch`).Scan(&count); err != nil || count != 64 {
				t.Fatal("first batch not bounded to 64", count, err)
			}
			second, err := q.ListDueSeedingTasks(t.Context())
			if err != nil || len(second) != 1 {
				t.Fatal("second page", len(second), err)
			}
			if seen[second[0].TaskID] || expected[second[0].TaskID] != second[0].OperationID {
				t.Fatal("second page repeated or changed identity")
			}
			var wg sync.WaitGroup
			failures := make([]error, 2)
			start := make(chan struct{})
			for i := range failures {
				wg.Add(1)
				go func(i int) { defer wg.Done(); <-start; failures[i] = scheduler.EnqueueDue(t.Context()) }(i)
			}
			close(start)
			wg.Wait()
			for _, e := range failures {
				if e != nil {
					t.Fatal("concurrent final-page scheduling", e)
				}
			}
			if err = scheduler.EnqueueDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			rest, err := q.ListDueSeedingTasks(t.Context())
			if err != nil || len(rest) != 0 {
				t.Fatal("queued work remains eligible", err)
			}
			rows, err := f.pool.Query(t.Context(), `SELECT d.task_id,t.operation_id,d.owner_id,d.client_id,d.client_stamp,d.config_version,j.id,j.payload->>'task_id',j.state,s.active_job_id,s.state FROM seeding_removal_dispatch d JOIN download_task t ON t.id=d.task_id JOIN job j ON j.id=d.job_id JOIN seeding_removal_schedule s ON s.task_id=t.id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			seen = map[uuid.UUID]bool{}
			for rows.Next() {
				var task, op, owner, endpoint, job, active uuid.UUID
				var frozenStamp, payload, state, scheduleState string
				var version int64
				if err = rows.Scan(&task, &op, &owner, &endpoint, &frozenStamp, &version, &job, &payload, &state, &active, &scheduleState); err != nil {
					t.Fatal(err)
				}
				if seen[task] || expected[task] != op || owner != admin || endpoint != client.ID || frozenStamp != stamp || version != 1 || payload != task.String() || active != job || state != "queued" || scheduleState != "queued" {
					t.Fatal("dispatch binding changed or duplicated", task)
				}
				seen[task] = true
			}
			if err = rows.Err(); err != nil || len(seen) != 65 {
				t.Fatal("missing task", len(seen), err)
			}
			rows.Close()
			for _, sql := range []string{`SELECT count(*) FROM job WHERE kind='seeding_removal'`, `SELECT count(*) FROM audit_log WHERE action='seeding.removal.queued'`, `SELECT count(*) FROM outbox_event WHERE event_type='seeding.removal.queued'`} {
				if err = f.pool.QueryRow(t.Context(), sql).Scan(&count); err != nil || count != 65 {
					t.Fatal("duplicated or missing scheduling ledger", count, err)
				}
			}
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM seeding_removal_permit)+(SELECT count(*) FROM seeding_removal_execution)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("scheduling issued execution authority", err)
			}
			if protected() != baseline || receipts() != originalReceipts || f.seedingProbe.removes.Load() != 0 {
				t.Fatal("pagination changed protected graph or performed removal")
			}
			t.Log("M10_SEEDING_PAGINATION_PG_PASS native_source=ONE_REAL_INGESTION additional_tasks=65_SQL_GRAPHS pages=64+1 concurrent_tail=DEDUPLICATED bindings=ORIGINAL protected=UNCHANGED permits=ZERO native_remove=ZERO latency=NOT_MEASURED")
		}
	})
}

func m10SeedingPageGraph(t *testing.T, f *organizationFixture, original uuid.UUID, index int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	task, op, candidate, plan, send, organize := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	prefix := `WITH args AS(SELECT $1::uuid old_task,$2::uuid task,$3::uuid op,$4::uuid candidate,$5::uuid plan,$6::uuid send,$7::uuid organize,$8::text hash) `
	args := []any{original, task, op, candidate, plan, send, organize, fmt.Sprintf("%040x", index)}
	phases := [][]string{
		{
			`INSERT INTO release_candidate SELECT (jsonb_populate_record(NULL::release_candidate,to_jsonb(c)||jsonb_build_object('id',a.candidate,'guid',a.candidate::text,'info_hash',a.hash))).* FROM release_candidate c JOIN download_task t ON t.candidate_id=c.id CROSS JOIN args a WHERE t.id=a.old_task`,
			`INSERT INTO job SELECT (jsonb_populate_record(NULL::job,to_jsonb(j)||jsonb_build_object('id',a.send,'dedupe_key',a.op::text,'state','running','payload',j.payload||jsonb_build_object('operation_id',a.op,'candidate_id',a.candidate),'result',NULL,'lease_owner','owned-page-binding','lease_token',1,'attempts',1,'lease_until',clock_timestamp()+interval '2 minutes'))).* FROM job j JOIN download_task_client_binding b ON b.job_id=j.id CROSS JOIN args a WHERE b.task_id=a.old_task`,
			`INSERT INTO download_task SELECT (jsonb_populate_record(NULL::download_task,to_jsonb(t)||jsonb_build_object('id',a.task,'operation_id',a.op,'candidate_id',a.candidate,'info_hash',a.hash,'state','submitting','organization_state','pending','transfer_plan_id',NULL))).* FROM download_task t CROSS JOIN args a WHERE t.id=a.old_task`,
			`INSERT INTO download_task_client_binding(task_id,client_id,client_stamp,job_id,lease_owner,lease_token) SELECT a.task,b.client_id,b.client_stamp,a.send,'owned-page-binding',1 FROM download_task_client_binding b CROSS JOIN args a WHERE b.task_id=a.old_task`,
		},
		{
			`INSERT INTO transfer_plan SELECT (jsonb_populate_record(NULL::transfer_plan,to_jsonb(p)||jsonb_build_object('id',a.plan,'operation_id',a.op))).* FROM transfer_plan p JOIN download_task t ON t.transfer_plan_id=p.id CROSS JOIN args a WHERE t.id=a.old_task`,
			`INSERT INTO transfer_step SELECT (jsonb_populate_record(NULL::transfer_step,to_jsonb(s)||jsonb_build_object('id',gen_random_uuid(),'plan_id',a.plan))).* FROM transfer_step s JOIN download_task t ON t.transfer_plan_id=s.plan_id CROSS JOIN args a WHERE t.id=a.old_task`,
			`INSERT INTO download_requirement SELECT (jsonb_populate_record(NULL::download_requirement,to_jsonb(d)||jsonb_build_object('id',gen_random_uuid(),'operation_id',a.op))).* FROM download_requirement d JOIN download_task t ON t.operation_id=d.operation_id CROSS JOIN args a WHERE t.id=a.old_task`,
			`UPDATE job SET state='succeeded',result=jsonb_build_object('task_id',a.task,'operation_id',a.op),lease_until=NULL FROM args a WHERE id=a.send`,
			`INSERT INTO job SELECT (jsonb_populate_record(NULL::job,to_jsonb(j)||jsonb_build_object('id',a.organize,'dedupe_key',a.task::text,'payload',jsonb_build_object('task_id',a.task),'result',jsonb_build_object('task_id',a.task,'plan_id',a.plan)))).* FROM job j CROSS JOIN args a WHERE j.kind='download_organization' AND j.state='succeeded' AND j.result->>'task_id'=a.old_task::text`,
			`UPDATE download_task SET state='completed',organization_state='completed',transfer_plan_id=a.plan FROM args a WHERE id=a.task`,
			`INSERT INTO seeding_removal_schedule(task_id,config_version,next_due_at,state) SELECT a.task,1,clock_timestamp()-interval '1 second','idle' FROM args a`,
		},
	}
	for phase, commands := range phases {
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for n, sql := range commands {
			changed, e := tx.Exec(t.Context(), prefix+sql, args...)
			if e != nil || changed.RowsAffected() != 1 {
				_ = tx.Rollback(context.WithoutCancel(t.Context()))
				t.Fatalf("graph %d phase %d statement %d: rows=%d error=%v", index, phase, n, changed.RowsAffected(), e)
			}
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatalf("graph %d phase %d commit: %v", index, phase, err)
		}
	}
	return task, op
}
