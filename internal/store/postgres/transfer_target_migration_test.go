package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferTargetMigrationQuarantinesUnfrozenButPreservesExecution(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	older := t.TempDir()
	paths, e := filepath.Glob("../../../migrations/*.sql")
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name >= "00038" {
			continue
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(older, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if v, e := postgres.Migrate(ctx, dsn, older); e != nil || v != 37 {
		t.Fatal(v, e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	root := uuid.New()
	if _, e = pool.Exec(ctx, "INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/fixture-target','movie','Fixture')", root); e != nil {
		t.Fatal(e)
	}
	approved, executing := uuid.New(), uuid.New()
	for id, state := range map[uuid.UUID]string{approved: "approved", executing: "executing"} {
		if _, e = pool.Exec(ctx, "INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy,execution_owner,execution_token,lease_until,root_path,root_generation,root_device,root_inode) VALUES($1,$2,$3,$4,'hardlink','old-owner',7,clock_timestamp()+interval '1 hour','/fixture-target',1,1,2)", id, uuid.New(), root, state); e != nil {
			t.Fatal(e)
		}
	}
	var lease time.Time
	if e = pool.QueryRow(ctx, "SELECT lease_until FROM transfer_plan WHERE id=$1", executing).Scan(&lease); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	var state, owner string
	var token int64
	var after time.Time
	if e = pool.QueryRow(ctx, "SELECT state FROM transfer_plan WHERE id=$1", approved).Scan(&state); e != nil || state != "review_required" {
		t.Fatal("old approved inferred target versions", state, e)
	}
	if e = pool.QueryRow(ctx, "SELECT state,execution_owner,execution_token,lease_until FROM transfer_plan WHERE id=$1", executing).Scan(&state, &owner, &token, &after); e != nil || state != "executing" || owner != "old-owner" || token != 7 || !lease.Equal(after) {
		t.Fatal("physical execution authority revoked by upgrade", state, owner, token, after, e)
	}
}
