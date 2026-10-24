package app

import (
	"context"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
)

type movieSeedingProtocol struct {
	mode     atomic.Int32
	removes  atomic.Int32
	removed  atomic.Bool
	bad      atomic.Bool
	factGate atomic.Pointer[movieSeedingReadGate]
}

func (p *movieSeedingProtocol) remove(w http.ResponseWriter, r *http.Request, hash string) {
	if p.mode.Load() == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	p.removes.Add(1)
	if r.Method != http.MethodPost || r.ParseForm() != nil || len(r.Form) != 2 || r.Form.Get("hashes") != hash || r.Form.Get("deleteFiles") != "false" {
		p.bad.Store(true)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.removed.Store(true)
	if p.mode.Load() == 2 {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			p.bad.Store(true)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			p.bad.Store(true)
			return
		}
		_ = conn.Close()
		return
	}
	w.WriteHeader(http.StatusOK)
}
func TestM10SeedingNativeRemovalRuntimePG(t *testing.T)     { runM10SeedingNative(t, false) }
func TestM10SeedingNativeLostReceiptRuntimePG(t *testing.T) { runM10SeedingNative(t, true) }
func runM10SeedingNative(t *testing.T, lost bool)           { runM10SeedingNativeState(t, lost, false) }
func runM10SeedingNativeState(t *testing.T, lost, disable bool) {
	runM10SeedingNativeProtocol(t, lost, disable, "qbittorrent")
}
func runM10SeedingNativeProtocol(t *testing.T, lost, disable bool, kind string) {
	runM10SeedingNativeScenario(t, m10SeedingNativeScenario{kind: kind, lost: lost, disable: disable})
}

type m10SeedingNativeScenario struct {
	kind                          string
	lost, disable, finishRollback bool
	ecosystem                     bool
}

func runM10SeedingNativeScenario(t *testing.T, scenario m10SeedingNativeScenario) {
	lost, disable, kind := scenario.lost, scenario.disable, scenario.kind
	runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, member *ports.Actor, _ uuid.UUID, _ *App) func() {
		verifyEcosystem := func() {}
		if scenario.ecosystem {
			verifyEcosystem = m10JointImportedConsumers(t, f)
		}
		mode := int32(1)
		if lost {
			mode = 2
		}
		f.seedingProbe.mode.Store(mode)
		if kind == "transmission" {
			if _, err := f.pool.Exec(t.Context(), `UPDATE download_client SET client_type='transmission',updated_at=clock_timestamp() WHERE id=$1`, f.client); err != nil {
				t.Fatal("configure Transmission before authorization", err)
			}
		}
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
			var source, target string
			if err = f.pool.QueryRow(t.Context(), `SELECT s.source_path,r.path||'/'||lf.rel_path FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id JOIN library_file lf ON lf.id=s.target_file_id JOIN library_root r ON r.id=lf.root_id WHERE p.operation_id=$1 AND s.state='completed'`, original.OperationID).Scan(&source, &target); err != nil {
				t.Fatal("source and imported target", err)
			}
			sourceBefore, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			targetBefore, err := os.Stat(target)
			if err != nil || !os.SameFile(sourceBefore, targetBefore) {
				t.Fatal("missing initial hardlink", err)
			}
			content, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			protected := func() string {
				return m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "download_task", "download_task_client_binding", "download_requirement", "acquisition_reservation", "unit_reservation", "transfer_plan", "transfer_step", "library_file", "library_file_unit", "unit_fact", "media", "media_unit", "media_identity", "subscription")
			}
			baseline := protected()
			admin := uuid.New()
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'seeding-native-admin',password_hash,'admin',true FROM app_user WHERE id=$2`, admin, uuid.MustParse(member.ID)); err != nil {
				t.Fatal(err)
			}
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.String(), Role: ports.RoleAdmin}
			seconds := int64(60)
			input := ports.SeedingConfigInput{ExpectedClientUpdatedAt: client.UpdatedAt.Time, Enabled: true, IntervalSeconds: 1200, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}}
			if _, err = seed.NewConfigService(postgres.NewSeedingConfigStore(f.pool, idgen.New())).Save(t.Context(), actor, client.ID, input, "native-seeding-config"); err != nil {
				t.Fatal(err)
			}
			if f.seedingProbe.removes.Load() != 0 {
				t.Fatal("save triggered immediate removal")
			}
			restoreFinish := func() {}
			if scenario.finishRollback {
				ddl := `CREATE FUNCTION m10_native_finish_fault() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='seeding.removal.finished' THEN RAISE EXCEPTION 'owned native finish fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER m10_native_finish_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION m10_native_finish_fault();`
				if _, err = f.pool.Exec(t.Context(), ddl); err != nil {
					t.Fatal(err)
				}
				restoreFinish = sync.OnceFunc(func() {
					cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
					defer stop()
					if _, e := f.pool.Exec(cleanupCtx, `DROP TRIGGER IF EXISTS m10_native_finish_fault ON outbox_event; DROP FUNCTION IF EXISTS m10_native_finish_fault();`); e != nil {
						t.Error("finish fault cleanup", e)
					}
				})
				defer restoreFinish()
			}
			var gate *movieSeedingReadGate
			unblock := func() {}
			if disable {
				gate = &movieSeedingReadGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
				unblock = sync.OnceFunc(func() { close(gate.release) })
				defer unblock()
				f.seedingProbe.factGate.Store(gate)
			}
			// Advancing this due row tests dispatch semantics, not interval wall-clock timing.
			if _, err = f.pool.Exec(t.Context(), `INSERT INTO seeding_removal_schedule(task_id,config_version,next_due_at,state) VALUES($1,1,clock_timestamp()-interval '1 second','idle')`, task); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if disable {
				select {
				case <-gate.entered:
				case <-waitCtx.Done():
					t.Fatal("native read did not enter gate")
				}
				var removalJob uuid.UUID
				waitOutboxCondition(t, waitCtx, func() bool {
					return f.pool.QueryRow(waitCtx, `SELECT e.job_id FROM seeding_removal_execution e JOIN seeding_removal_dispatch d ON d.job_id=e.job_id WHERE d.task_id=$1 AND e.requests=1 AND e.released_at IS NULL`, task).Scan(&removalJob) == nil
				})
				input.ExpectedVersion = 1
				input.Enabled = false
				if _, err = seed.NewConfigService(postgres.NewSeedingConfigStore(f.pool, idgen.New())).Save(waitCtx, actor, client.ID, input, "disable-during-native-read"); err != nil {
					t.Fatal("disable during blocked read", err)
				}
				unblock()
				waitOutboxCondition(t, waitCtx, func() bool {
					e, readErr := q.LatestSeedingExecution(waitCtx, removalJob)
					return readErr == nil && e.ReleasedAt.Valid
				})
				if _, err = f.pool.Exec(waitCtx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND state='running'`, removalJob); err != nil {
					t.Fatal(err)
				}
				if err = postgres.NewSeedingScheduleStore(f.pool, idgen.New()).RecoverExpired(waitCtx); err != nil {
					t.Fatal(err)
				}
				job, readErr := q.GetJobByID(waitCtx, removalJob)
				if readErr != nil || job.State != "dead" || job.Attempts != 1 {
					t.Fatal("disabled original task replayed", readErr)
				}
				var permits int
				if err = f.pool.QueryRow(waitCtx, `SELECT count(*) FROM seeding_removal_permit WHERE task_id=$1`, task).Scan(&permits); err != nil || permits != 0 || f.seedingProbe.removes.Load() != 0 {
					t.Fatal("late observation granted removal", permits, err)
				}
				if err = postgres.NewSeedingScheduleStore(f.pool, idgen.New()).EnqueueDue(waitCtx); err != nil {
					t.Fatal(err)
				}
				var jobs int
				if err = f.pool.QueryRow(waitCtx, `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&jobs); err != nil || jobs != 1 {
					t.Fatal("disabled configuration scheduled another task", err)
				}
				sourceAfter, e1 := os.Stat(source)
				targetAfter, e2 := os.Stat(target)
				after, e3 := os.ReadFile(target)
				if e1 != nil || e2 != nil || e3 != nil || !os.SameFile(sourceBefore, sourceAfter) || !os.SameFile(targetBefore, targetAfter) || string(content) != string(after) || protected() != baseline {
					t.Fatal("disabled native read changed files or protected graph", e1, e2, e3)
				}
				t.Log("M10_SEEDING_DISABLE_NATIVE_APP_PASS native_read=HELD config_disable=COMMITTED late_result=REJECTED permits=ZERO remove=ZERO original_job=DEAD files_and_graph=UNCHANGED")
				return
			}
			originalPermit := ""
			if scenario.finishRollback {
				var originalJob uuid.UUID
				waitOutboxCondition(t, waitCtx, func() bool {
					return f.pool.QueryRow(waitCtx, `SELECT p.job_id FROM seeding_removal_permit p JOIN seeding_removal_execution e ON e.job_id=p.job_id AND e.lease_token=p.lease_token WHERE p.task_id=$1 AND e.released_at IS NOT NULL AND e.result IS NULL`, task).Scan(&originalJob) == nil
				})
				if f.seedingProbe.bad.Load() || !f.seedingProbe.removed.Load() || f.seedingProbe.removes.Load() != 1 {
					t.Fatal("native removal not completed before rollback")
				}
				j, e := q.GetJobByID(waitCtx, originalJob)
				if e != nil || j.State != "running" || j.Attempts != 1 {
					t.Fatal("failed finish changed original job", e)
				}
				s, e := q.GetSeedingRemovalSchedule(waitCtx, task)
				if e != nil || s.State == "retired" || !s.ActiveJobID.Valid || uuid.UUID(s.ActiveJobID.Bytes) != originalJob {
					t.Fatal("failed finish changed schedule", e)
				}
				var writes int
				if e = f.pool.QueryRow(waitCtx, `SELECT (SELECT count(*) FROM audit_log WHERE action='seeding.removal.finished')+(SELECT count(*) FROM outbox_event WHERE event_type='seeding.removal.finished')+(SELECT count(*) FROM seeding_removal_execution WHERE result IS NOT NULL)`).Scan(&writes); e != nil || writes != 0 {
					t.Fatal("finish tail left partial completion", writes, e)
				}
				originalPermit = m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_permit")
				restoreFinish()
				changed, e := f.pool.Exec(waitCtx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND state='running' AND lease_token=$2`, originalJob, j.LeaseToken)
				if e != nil || changed.RowsAffected() != 1 {
					t.Fatal("expire released execution lease", e)
				}
				if e = postgres.NewSeedingScheduleStore(f.pool, idgen.New()).RecoverExpired(waitCtx); e != nil {
					t.Fatal("recover failed original finish", e)
				}
			}
			waitOutboxCondition(t, waitCtx, func() bool {
				s, e := q.GetSeedingRemovalSchedule(waitCtx, task)
				return e == nil && s.State == "retired"
			})
			if f.seedingProbe.bad.Load() || f.seedingProbe.removes.Load() != 1 || !f.seedingProbe.removed.Load() {
				t.Fatal("removal not single original retaining-files request")
			}
			permit, err := q.GetSeedingRemovalPermit(t.Context(), task)
			if err != nil {
				t.Fatal(err)
			}
			job, err := q.GetJobByID(t.Context(), permit.JobID)
			if err != nil || job.State != "succeeded" {
				t.Fatal("original job incomplete", err)
			}
			executions := 1
			if lost || scenario.finishRollback {
				executions = 2
			}
			var count, released, observe, prepared int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE released_at IS NOT NULL),count(*) FILTER(WHERE observe_only) FROM seeding_removal_execution WHERE job_id=$1`, job.ID).Scan(&count, &released, &observe); err != nil || count != executions || released != executions || observe != executions-1 || int(job.Attempts) != executions {
				t.Fatal("execution and observation count", count, released, observe, job.Attempts, err)
			}
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_event WHERE aggregate_id=$1 AND event_type='seeding.removal.prepared'`, task).Scan(&prepared); err != nil || prepared != 1 {
				t.Fatal("repeated permanent permit", err)
			}
			latest, err := q.LatestSeedingExecution(t.Context(), job.ID)
			expected := "acknowledged"
			if lost || scenario.finishRollback {
				expected = "absent"
			}
			if err != nil || latest.Result.String != expected {
				t.Fatal("native final observation", err)
			}
			if (lost || scenario.finishRollback) && permit.LeaseToken >= latest.LeaseToken {
				t.Fatal("recovery rebound original permit")
			}
			sourceAfter, e1 := os.Stat(source)
			targetAfter, e2 := os.Stat(target)
			after, e3 := os.ReadFile(target)
			if e1 != nil || e2 != nil || e3 != nil || !os.SameFile(sourceBefore, sourceAfter) || !os.SameFile(targetBefore, targetAfter) || string(content) != string(after) {
				t.Fatal("removal changed source or imported file", e1, e2, e3)
			}
			if protected() != baseline {
				t.Fatal("removal changed protected ingestion or demand graph")
			}
			if err = postgres.NewSeedingScheduleStore(f.pool, idgen.New()).EnqueueDue(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("retired task scheduled another removal", err)
			}
			if scenario.finishRollback && m8ConfigSnapshot(t, &m7ScrapeFixture{pool: f.pool}, "seeding_removal_permit") != originalPermit {
				t.Fatal("recovery rebound permanent permit")
			}
			verifyEcosystem()
			t.Logf("M10_SEEDING_NATIVE_APP_PASS configuration=PERSISTED App=REAL native=%s remove=ONCE retain_files=TRUE source_and_target=SAME_INODE lost_receipt=%v finish_rollback=%v executions=%d observe_only=%d protected=UNCHANGED", kind, lost, scenario.finishRollback, executions, observe)
		}
	})
}
