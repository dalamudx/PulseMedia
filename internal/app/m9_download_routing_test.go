package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

func TestM9MovieAutomaticRoutingRuntimePG(t *testing.T) {
	runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, _ *App) func() {
		return func() {
			waitSeedingOrganization(t, f)
			var n int
			err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM download_task t
    JOIN download_task_client_binding b ON b.task_id=t.id AND b.client_id=t.client_id
    JOIN job j ON j.id=b.job_id
    WHERE t.state='completed' AND t.organization_state='completed'
      AND b.client_stamp=pulse_global_rates_binding(t.client_id)
      AND j.kind='download_submission'
      AND j.payload->>'operation_id'=t.operation_id::text
      AND j.payload->>'candidate_id'=t.candidate_id::text`).Scan(&n)
			if err != nil || n != 1 {
				t.Fatal("original native Add client binding", n, err)
			}
			t.Log("M10_SEEDING_ORIGINAL_CLIENT_APP_PASS native_Add=ONCE frozen_client=ORIGINAL hardlink=REAL removal=NOT_ENABLED")
		}
	})
}
