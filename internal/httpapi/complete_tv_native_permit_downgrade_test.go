package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func atCompleteTVPermitMigration(t *testing.T, n *nativeObserverFixture) {
	t.Helper()
	dsn := n.f.a.DBPool().Config().ConnString()
	for version := postgres.CurrentMigrationVersion; version > 62; version-- {
		if got, err := postgres.Rollback(context.Background(), dsn, "../../migrations"); err != nil || got != int64(version-1) {
			t.Fatalf("rollback unrelated migration %d: version=%d error=%v", version, got, err)
		}
	}
	if err := postgres.CheckSchemaVersion(context.Background(), n.f.a.DBPool(), 62); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTVNativeObserverNativePGConcurrentPermitDowngrade(t *testing.T) {
	n := newNativeObserverFixture(t)
	atCompleteTVPermitMigration(t, n)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inserter, e := n.f.a.DBPool().Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = inserter.Rollback(context.WithoutCancel(ctx)) }()
	// Already hold the insert lock before Down requests AccessExclusive. This lets
	// this transaction insert while Down waits, after the old unprotected check.
	if _, e = inserter.Exec(ctx, `LOCK TABLE complete_tv_physical_permit IN ROW EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, err := postgres.Rollback(ctx, n.f.a.DBPool().Config().ConnString(), "../../migrations")
		done <- err
	}()
	defer func() {
		_ = inserter.Rollback(context.WithoutCancel(ctx))
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("downgrade not joined")
		}
	}()
	waitCompleteTVLock(t, n.completeTVFixture, "complete_tv_physical_permit")
	token := uuid.New()
	if _, e = inserter.Exec(ctx, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key) VALUES($1,$2,1,1)`, n.request.OperationID, token); e != nil {
		t.Fatal(e)
	}
	if e = inserter.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		done <- e
		if e == nil || !strings.Contains(e.Error(), "cannot discard retained complete TV physical execution permits") {
			t.Errorf("concurrent permit was discarded: %v", e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var saved uuid.UUID
	if e = n.f.a.DBPool().QueryRow(ctx, `SELECT token FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&saved); e != nil || saved != token {
		t.Errorf("permit lost: %v %v", saved, e)
	}
	if e = postgres.CheckSchemaVersion(ctx, n.f.a.DBPool(), 62); e != nil {
		t.Error(e)
	}
}
