package search_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"testing"
)

func TestSharedAttachmentEventFailureRollsBackNewTarget(t *testing.T) {
	f, svc, cmd, rootB := globalCommandFixture(t)
	ctx := context.Background()
	actor := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	first, e := svc.AcquireMovie(ctx, actor, cmd)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "CREATE FUNCTION reject_shared_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='acquisition.demand_attached' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_shared_event BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_shared_event()"); e != nil {
		t.Fatal(e)
	}
	cmd.RootID = rootB
	other := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	if _, e = svc.AcquireMovie(ctx, other, cmd); e == nil {
		t.Fatal("late event acknowledged partial attachment")
	}
	var demands, reservations, jobs int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_requirement WHERE operation_id=$1),(SELECT count(*) FROM unit_reservation WHERE operation_id=$1),(SELECT count(*) FROM job WHERE kind='download_submission')", first.OperationID).Scan(&demands, &reservations, &jobs); e != nil || demands != 1 || reservations != 1 || jobs != 1 {
		t.Fatal("late shared event failure left partial target", demands, reservations, jobs, e)
	}
	if _, e = f.pool.Exec(ctx, "DROP TRIGGER reject_shared_event ON outbox_event"); e != nil {
		t.Fatal(e)
	}
	attached, e := svc.AcquireMovie(ctx, other, cmd)
	if e != nil || attached.OperationID != first.OperationID {
		t.Fatal("attachment retry split physical op", attached, e)
	}
}
