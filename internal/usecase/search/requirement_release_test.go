package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"testing"
)

func TestRequirementReleaseKeepsPhysicalProtection(t *testing.T) {
	for _, state := range []string{"submitting", "unknown", "tracking", "completed"} {
		t.Run(state, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			round := f.collect(t)
			job := f.finalJob(t, round)
			result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if e != nil {
				t.Fatal(e)
			}
			requirements, e := f.q.ListRequirementsForOperation(ctx, result.OperationID)
			if e != nil || len(requirements) != 1 {
				t.Fatal(requirements, e)
			}
			r := requirements[0]
			repo := postgres.NewDownloadSubmissionStore(f.pool, idgen.New())
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: r.ActorID, Role: f.actor.Role}
			if _, e = f.q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: result.OperationID, ClientID: f.downloader, CandidateID: *result.SelectedCandidateID, State: state, SavePath: "/fixture"}); e != nil {
				t.Fatal(e)
			}
			foreign := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
			if _, e = repo.ReleaseRequirement(ctx, foreign, result.OperationID, r.UnitID, r.RootID); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("admin without owned demand implicitly forced cancellation", e)
			}
			released, e := repo.ReleaseRequirement(ctx, actor, result.OperationID, r.UnitID, r.RootID)
			if e != nil || released {
				t.Fatal("in-flight task protection released", state, released, e)
			}
			rows, e := f.q.ListRequirementsForOperation(ctx, result.OperationID)
			if e != nil || len(rows) != 1 || rows[0].State != "released" || !rows[0].ReleasedAt.Valid {
				t.Fatal("requirement history deleted", rows, e)
			}
			if _, e = f.q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: r.UnitID, RootID: r.RootID, Purpose: "manual_acquisition"}); e != nil {
				t.Fatal("physical reservation disappeared", state, e)
			}
		})
	}
}
func TestRequirementReleaseRollsBackAndCancelsUnsent(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	round := f.collect(t)
	job := f.finalJob(t, round)
	result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
	if e != nil {
		t.Fatal(e)
	}
	requirements, e := f.q.ListRequirementsForOperation(ctx, result.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	r := requirements[0]
	repo := postgres.NewDownloadSubmissionStore(f.pool, idgen.New())
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: r.ActorID, Role: f.actor.Role}
	if _, e = f.pool.Exec(ctx, "CREATE FUNCTION reject_requirement_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='download.requirement_released' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_requirement_release BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_requirement_release()"); e != nil {
		t.Fatal(e)
	}
	if released, e := repo.ReleaseRequirement(ctx, actor, result.OperationID, r.UnitID, r.RootID); e == nil || released {
		t.Fatal("late event failure acknowledged release", released, e)
	}
	rows, e := f.q.ListRequirementsForOperation(ctx, result.OperationID)
	if e != nil || rows[0].State != "pending" {
		t.Fatal("released state partially committed", rows, e)
	}
	if _, e = f.q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: r.UnitID, RootID: r.RootID, Purpose: "manual_acquisition"}); e != nil {
		t.Fatal("late event failure released protection", e)
	}
	if _, e = f.pool.Exec(ctx, "DROP TRIGGER reject_requirement_release ON outbox_event"); e != nil {
		t.Fatal(e)
	}
	if released, e := repo.ReleaseRequirement(ctx, actor, result.OperationID, r.UnitID, r.RootID); e != nil || !released {
		t.Fatal("known unsent acquisition did not release", released, e)
	}
	var state string
	if e = f.pool.QueryRow(ctx, "SELECT state FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1", result.OperationID.String()).Scan(&state); e != nil || state != "dead" {
		t.Fatal("unsent job still runnable", state, e)
	}
	if _, e = f.q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: r.UnitID, RootID: r.RootID, Purpose: "manual_acquisition"}); e == nil {
		t.Fatal("known unsent reservation retained")
	}
}
