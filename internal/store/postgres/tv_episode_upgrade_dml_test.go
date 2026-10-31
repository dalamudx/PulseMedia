package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTVEpisodeUpgradeDMLRoleCannotForgePhysicalReturnPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	c, nonce, owner, token := tvUpgradeDBSession(t, f, r.OperationID)
	if _, e := c.Exec(t.Context(), tvUpgradeInsertTestPermit, r.OperationID, "send", owner, token, nonce, "60 seconds", "dml-original"); e != nil {
		t.Fatal(e)
	}
	role := "mp22_dml_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e := f.pool.Exec(t.Context(), "CREATE ROLE "+role+" LOGIN"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, "DROP OWNED BY "+role)
		if _, e := f.pool.Exec(ctx, "DROP ROLE "+role); e != nil {
			t.Error(e)
		}
	})
	if _, e := f.pool.Exec(t.Context(), "GRANT USAGE ON SCHEMA public TO "+role); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(t.Context(), "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO "+role); e != nil {
		t.Fatal(e)
	}
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = ""
	dml, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(dml.Close)
	// Every secret-free physical identity is readable; copying them is insufficient.
	var pid int
	var nonceText string
	if e = dml.QueryRow(t.Context(), "SELECT backend_pid,nonce::text FROM tv_episode_upgrade_permit WHERE operation_id=$1", r.OperationID).Scan(&pid, &nonceText); e != nil {
		t.Fatal(e)
	}
	if _, e = dml.Exec(t.Context(), "SELECT set_config('pulse.tv_upgrade_nonce',$1,false)", nonceText); e != nil {
		t.Fatal(e)
	}
	_, e = dml.Exec(t.Context(), `INSERT INTO tv_episode_upgrade_return(operation_id,action,owner,token,status,evidence) VALUES($1,'send',$2,$3,'confirmed','{}')`, r.OperationID, owner, token)
	if e == nil {
		t.Fatal("DML role forged original native return", fmt.Sprint(pid))
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_return"); n != 0 {
		t.Fatal("forged native return survived", n)
	}
}
func TestTVEpisodeUpgradeLeaseRecheckedAtCommitPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	c, nonce, owner, token := tvUpgradeDBSession(t, f, r.OperationID)
	tx, e := c.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), tvUpgradeInsertTestPermit, r.OperationID, "send", owner, token, nonce, "100 milliseconds", "commit-lease"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), "SELECT pg_sleep(0.15)"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e == nil {
		t.Fatal("lease expired during transaction committed")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_permit"); n != 0 {
		t.Fatal("expired commit left permit", n)
	}
}
