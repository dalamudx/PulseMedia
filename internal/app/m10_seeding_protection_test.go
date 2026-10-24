package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Build the positive graph by running the production search/Add/observation/
// organization chain, then break one proof at a time in rollback-only transactions.
func TestM10OrdinarySeedingProtectionNativePG(t *testing.T) {
	runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, _ *App) func() {
		return func() {
			task, stamp := waitSeedingOrganization(t, f)
			input := db.ReadOrdinarySeedingProtectionParams{TaskID: task, ClientStamp: stamp}
			readOnly, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			proof, err := db.New(readOnly).ReadOrdinarySeedingProtection(t.Context(), input)
			_ = readOnly.Rollback(context.WithoutCancel(t.Context()))
			if err != nil || !proof.Owned || !proof.Imported || proof.PendingWork {
				t.Fatal("production completed graph lacks protection proof", proof, err)
			}
			for _, tc := range []struct {
				name, sql                string
				owned, imported, pending bool
			}{
				{"endpoint", `UPDATE download_client SET url='https://replacement.example' WHERE id=(SELECT client_id FROM download_task WHERE id=$1)`, false, true, false},
				{"send-receipt", `UPDATE job SET payload=jsonb_set(payload,'{operation_id}','"00000000-0000-0000-0000-000000000001"') WHERE id=(SELECT job_id FROM download_task_client_binding WHERE task_id=$1)`, false, true, false},
				{"not-completed", `UPDATE download_task SET organization_state='review_required' WHERE id=$1`, true, false, false},
				{"no-organization-receipt", `UPDATE job SET result='{}' WHERE kind='download_organization' AND result->>'task_id'=$1::uuid::text`, true, false, false},
				{"retired-file", `UPDATE library_file SET retired_at=clock_timestamp() WHERE id IN(SELECT s.target_file_id FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id JOIN download_task t ON t.operation_id=p.operation_id WHERE t.id=$1)`, true, false, false},
				{"retired-mapping", `UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id IN(SELECT s.target_file_id FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id JOIN download_task t ON t.operation_id=p.operation_id WHERE t.id=$1)`, true, false, false},
				{"revoked-fact", `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE stage='imported' AND source_file_id IN(SELECT s.target_file_id FROM transfer_step s JOIN transfer_plan p ON p.id=s.plan_id JOIN download_task t ON t.operation_id=p.operation_id WHERE t.id=$1)`, true, false, false},
				{"changed-root", `UPDATE library_root SET generation=generation+1 WHERE id IN(SELECT p.root_id FROM transfer_plan p JOIN download_task t ON t.operation_id=p.operation_id WHERE t.id=$1)`, true, false, false},
				{"offline-root", `UPDATE library_root SET health='offline' WHERE id IN(SELECT p.root_id FROM transfer_plan p JOIN download_task t ON t.operation_id=p.operation_id WHERE t.id=$1)`, true, false, false},
				{"pending-requirement", `UPDATE download_requirement SET state='pending' WHERE operation_id=(SELECT operation_id FROM download_task WHERE id=$1)`, true, false, true},
				{"pending-reservation", `UPDATE unit_reservation SET state='dispatched' WHERE operation_id=(SELECT operation_id FROM download_task WHERE id=$1)`, true, true, true},
				{"active-organization", `INSERT INTO job(id,kind,state,payload,max_attempts) SELECT gen_random_uuid(),'download_organization','queued',jsonb_build_object('operation_id',operation_id::text,'task_id',id::text),3 FROM download_task WHERE id=$1`, true, true, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tx, err := f.pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
					changed, err := tx.Exec(t.Context(), tc.sql, task)
					if err != nil || changed.RowsAffected() < 1 {
						t.Fatal("counterexample not installed", err)
					}
					p, err := db.New(tx).ReadOrdinarySeedingProtection(t.Context(), input)
					if err != nil || p.Owned != tc.owned || p.Imported != tc.imported || p.PendingWork != tc.pending {
						t.Fatal("broken proof accepted", p, err)
					}
				})
			}
			after, err := db.New(f.pool).ReadOrdinarySeedingProtection(t.Context(), input)
			if err != nil || after != proof {
				t.Fatal("rollback changed original graph", after, err)
			}
			t.Log("M10_SEEDING_ORDINARY_PROTECTION_APP_PASS native_Add=ONCE hardlink=REAL original_binding=REQUIRED imported=EXACT_GRAPH counterexamples=12 query=READ_ONLY removal=NOT_AUTHORIZED")
		}
	})
}

func waitSeedingOrganization(t *testing.T, f *organizationFixture) (uuid.UUID, string) {
	t.Helper()
	var task uuid.UUID
	var stamp string
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := f.pool.QueryRow(t.Context(), `SELECT t.id,b.client_stamp FROM download_task t JOIN download_task_client_binding b ON b.task_id=t.id WHERE t.organization_state='completed' AND EXISTS(SELECT 1 FROM job j WHERE j.kind='download_organization' AND j.state='succeeded' AND j.result->>'task_id'=t.id::text)`).Scan(&task, &stamp)
		if err == nil {
			return task, stamp
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-deadline.C:
			t.Fatal("organization completion ledger not committed")
		case <-ticker.C:
		}
	}
}
