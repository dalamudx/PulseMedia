package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM10SeedingPermanentPermitNativePG(t *testing.T) {
	runFullMoviePipelineLifecycle(t, false, "", "", false, true, true, func(f *organizationFixture, member *ports.Actor, _ uuid.UUID, _ *App) func() {
		return func() {
			adminID := uuid.New()
			added, err := f.pool.Exec(t.Context(), `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'seeding-permit-admin',password_hash,'admin',true FROM app_user WHERE id=$2`, adminID, uuid.MustParse(member.ID))
			if err != nil || added.RowsAffected() != 1 {
				t.Fatal("administrator fixture", err)
			}
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminID.String(), Role: ports.RoleAdmin}
			task, stamp := waitSeedingOrganization(t, f)
			q := db.New(f.pool)
			original, err := q.GetSubmissionTask(t.Context(), task)
			if err != nil {
				t.Fatal(err)
			}
			client, err := q.GetDownloadClientByID(t.Context(), original.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			seconds := int64(60)
			cfg := seed.NewConfigService(postgres.NewSeedingConfigStore(f.pool, idgen.New()))
			receipt, err := cfg.Save(t.Context(), actor, client.ID, ports.SeedingConfigInput{ExpectedClientUpdatedAt: client.UpdatedAt.Time, Enabled: true, IntervalSeconds: 1200, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}}, "permit-config")
			if err != nil {
				t.Fatal(err)
			}
			base := db.CreateSeedingRemovalPermitParams{TaskID: task, OperationID: original.OperationID, ClientID: client.ID, ClientStamp: stamp, ConfigVersion: receipt.Config.Version, OwnerID: uuid.MustParse(actor.ID), LeaseOwner: "seeding-permit-test", LeaseToken: 1, InfoHash: original.InfoHash, OperationTag: "pulsemedia:op:" + original.OperationID.String(), SavePath: original.SavePath, TotalBytes: original.TotalBytes, SeedingSeconds: pgtype.Int8{Int64: 60, Valid: true}}
			prepare := func(t *testing.T, tx pgx.Tx, skip string) db.CreateSeedingRemovalPermitParams {
				t.Helper()
				in := base
				in.JobID = uuid.New()
				if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&in.ObservedAt); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO job(id,kind,state,payload,max_attempts,lease_owner,lease_token,lease_until) VALUES($1,'seeding_removal','running',jsonb_build_object('task_id',$2::uuid,'client_id',$3::uuid,'config_version',$4::bigint),3,$5,1,clock_timestamp()+interval '1 minute')`, in.JobID, task, in.ClientID, in.ConfigVersion, in.LeaseOwner); err != nil {
					t.Fatal(err)
				}
				if skip != "audit" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload) VALUES($1,'user',$2,'seeding.removal.prepared','download_task',$3,jsonb_build_object('job_id',$4::uuid))`, uuid.New(), actor.ID, task.String(), in.JobID); err != nil {
						t.Fatal(err)
					}
				}
				if skip != "event" {
					if _, err := tx.Exec(t.Context(), `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'seeding.removal',$2,'seeding.removal.prepared',jsonb_build_object('job_id',$3::uuid),1)`, uuid.New(), task.String(), in.JobID); err != nil {
						t.Fatal(err)
					}
				}
				return in
			}
			for _, tc := range []struct{ name, sql string }{
				{"permission", `UPDATE app_user SET is_active=false WHERE id=$1`},
				{"configuration", `UPDATE seeding_configuration SET enabled=false,version=version+1 WHERE client_id=$2`},
				{"endpoint", `UPDATE download_client SET url='https://changed.example' WHERE id=$2`},
				{"lease", `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$3`},
				{"pending-demand", `UPDATE download_requirement SET state='pending' WHERE operation_id=$4`},
				{"revoked-fact", `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE stage='imported' AND source_file_id IN(SELECT s.target_file_id FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id WHERE p.operation_id=$4)`},
				{"audit", ""}, {"event", ""}, {"threshold", ""}, {"unknown-seconds", ""}, {"stale-observation", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tx, err := f.pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
					in := prepare(t, tx, tc.name)
					if tc.sql != "" {
						// Explicit casts keep all parameters typed, including unused case arguments.
						sql := `WITH args AS(SELECT $1::uuid a,$2::uuid b,$3::uuid c,$4::uuid d) ` + tc.sql
						if _, err = tx.Exec(t.Context(), sql, in.OwnerID, in.ClientID, in.JobID, in.OperationID); err != nil {
							t.Fatal(err)
						}
					}
					switch tc.name {
					case "threshold":
						in.SeedingSeconds.Int64 = 59
					case "unknown-seconds":
						in.SeedingSeconds.Valid = false
					case "stale-observation":
						in.ObservedAt.Time = in.ObservedAt.Time.Add(-time.Minute)
					}
					if _, err = db.New(tx).CreateSeedingRemovalPermit(t.Context(), in); err != nil {
						t.Fatal("insert should defer the authority check", err)
					}
					if err = tx.Commit(t.Context()); err == nil {
						t.Fatal("unsafe permit committed")
					}
					var writes int
					if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM seeding_removal_permit WHERE task_id=$1)+(SELECT count(*) FROM job WHERE id=$2)+(SELECT count(*) FROM audit_log WHERE payload->>'job_id'=$2::uuid::text)+(SELECT count(*) FROM outbox_event WHERE payload->>'job_id'=$2::uuid::text)`, task, in.JobID).Scan(&writes); err != nil || writes != 0 {
						t.Fatal("tail rollback incomplete", writes, err)
					}
				})
			}
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			in := prepare(t, tx, "")
			if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('acquisition:'||$1::uuid::text,0))`, in.OperationID); err != nil {
				t.Fatal(err)
			}
			permit, err := db.New(tx).CreateSeedingRemovalPermit(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, e := f.pool.Exec(waitCtx, `UPDATE download_requirement SET state='pending' WHERE operation_id=$1`, in.OperationID)
				finished <- e
			}()
			select {
			case e := <-finished:
				t.Fatal("demand bypassed acquisition lock", e)
			case <-time.After(50 * time.Millisecond):
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal("valid permit commit", err)
			}
			if err = <-finished; err == nil {
				t.Fatal("new demand accepted after permanent permit")
			}
			for _, sql := range []string{
				`UPDATE seeding_removal_permit SET lease_token=2 WHERE task_id=$1`,
				`DELETE FROM seeding_removal_permit WHERE task_id=$1`,
				`UPDATE transfer_plan SET updated_at=clock_timestamp() WHERE operation_id=(SELECT operation_id FROM download_task WHERE id=$1)`,
				`INSERT INTO job(id,kind,state,payload) SELECT gen_random_uuid(),'download_organization','queued',jsonb_build_object('task_id',id) FROM download_task WHERE id=$1`,
			} {
				if _, err = f.pool.Exec(t.Context(), sql, task); err == nil {
					t.Fatal("permanent barrier could be changed or bypassed")
				}
			}
			if _, err = q.CreateSeedingRemovalPermit(t.Context(), in); err == nil {
				t.Fatal("permanent permit reissued")
			}
			if _, err = f.pool.Exec(t.Context(), `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, in.JobID); err != nil {
				t.Fatal(err)
			}
			restored, err := q.GetSeedingRemovalPermit(t.Context(), task)
			if err != nil || !reflect.DeepEqual(restored, permit) {
				t.Fatal("recovery rewrote original permit", err)
			}
			t.Log("M10_SEEDING_PERMANENT_PERMIT_PG_PASS original=IMMUTABLE authority=COMMIT_TIME rollback=ALL new_demand=BLOCKED recovery=ORIGINAL file_operations=NONE")
		}
	})
}
