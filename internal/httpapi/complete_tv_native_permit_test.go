package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
)

func TestCompleteTVNativeObserverNativePGRetainedPermit(t *testing.T) {
	n := newNativeObserverFixture(t)
	atCompleteTVPermitMigration(t, n)
	token := uuid.New()
	n.exec(t, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key,created_at) VALUES($1,$2,1,1,clock_timestamp()-interval '1 year')`, n.request.OperationID, token)
	before := n.snapshot(t)
	r, e := n.observe(context.Background())
	if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || n.snapshot(t) != before {
		t.Fatal("retained permit reclaimed", r, e)
	}
	n.wire(t, 0)
	var saved uuid.UUID
	if e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT token FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&saved); e != nil || saved != token {
		t.Fatal("foreign owner removed", e, saved)
	}
	if _, e = postgres.Rollback(context.Background(), n.f.a.DBPool().Config().ConnString(), "../../migrations"); e == nil {
		t.Fatal("downgrade discarded retained physical permit")
	}
	if e = postgres.CheckSchemaVersion(context.Background(), n.f.a.DBPool(), 62); e != nil {
		t.Fatal(e)
	}
}

func TestCompleteTVNativeObserverNativePGPermitReleasedAfterReturn(t *testing.T) {
	n := newNativeObserverFixture(t)
	if r, e := n.observe(context.Background()); e != nil || r == nil || r.State != "tracking" {
		t.Fatal(r, e)
	}
	var count int
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&count); e != nil || count != 0 {
		t.Fatal("finished synchronous permit retained", count, e)
	}
	n.wire(t, 1)
}
