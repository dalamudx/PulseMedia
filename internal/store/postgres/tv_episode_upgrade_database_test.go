package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func tvUpgradeDBSession(t *testing.T, f *tvUpgradeFixture, op uuid.UUID) (*pgxpool.Conn, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	c, e := f.pool.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	nonce, owner, token := uuid.New(), uuid.New(), uuid.New()
	if _, e = c.Exec(t.Context(), "SELECT pg_advisory_lock(pulse_tv_upgrade_lock($1)),set_config('pulse.tv_upgrade_nonce',$2,false)", op, nonce.String()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = c.Exec(context.Background(), "SELECT pg_advisory_unlock(pulse_tv_upgrade_lock($1)),set_config('pulse.tv_upgrade_nonce','',false)", op)
		c.Release()
	})
	return c, nonce, owner, token
}

const tvUpgradeInsertTestPermit = `INSERT INTO tv_episode_upgrade_permit(operation_id,action,owner,token,backend_pid,backend_start,lock_key,nonce,lease_until,request_key,request_hash) SELECT $1,$2,$3,$4,pg_backend_pid(),backend_start,pulse_tv_upgrade_lock($1),$5,clock_timestamp()+$6::interval,$7,repeat('a',64) FROM pg_stat_activity WHERE pid=pg_backend_pid()`

func TestTVEpisodeUpgradeDatabaseForgeryAndExpiryPG(t *testing.T) {
	for _, kind := range []string{"wrong-pid", "wrong-start", "wrong-nonce", "wrong-lock", "expired", "truncate", "missing-boolean", "action"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVUpgradeFixture(t)
			r := f.prepared(t)
			c, nonce, owner, token := tvUpgradeDBSession(t, f, r.OperationID)
			query := tvUpgradeInsertTestPermit
			args := []any{r.OperationID, "send", owner, token, nonce, "60 seconds", "db-probe"}
			switch kind {
			case "wrong-pid":
				query = strings.Replace(query, "pg_backend_pid(),backend_start", "pg_backend_pid()+1,backend_start", 1)
			case "wrong-start":
				query = strings.Replace(query, "pg_backend_pid(),backend_start", "pg_backend_pid(),backend_start-interval '1 second'", 1)
			case "wrong-nonce":
				args[4] = uuid.New()
			case "wrong-lock":
				query = strings.Replace(query, "pulse_tv_upgrade_lock($1),$5", "pulse_tv_upgrade_lock($1)+1,$5", 1)
			case "action":
				args[1] = "movie-upgrade"
			case "expired":
				args[5] = "-1 second"
			case "truncate":
				query = "TRUNCATE tv_episode_upgrade_return"
				args = nil
			case "missing-boolean":
				query = `INSERT INTO tv_episode_upgrade_phase(operation_id,action,actor_id,request_key,request_hash,result) VALUES($1,'observe:forged',$2,'unsafe',repeat('a',64),jsonb_build_object('operation_id',$1::text,'unit_id',$3::text,'current_file_id',$4::text,'candidate_id',$5::text))`
				args = []any{r.OperationID, f.actor.ID, f.unit, f.request.CurrentFileID, f.request.CandidateID}
			}
			if _, e := c.Exec(t.Context(), query, args...); e == nil {
				t.Fatal("database admitted forged authority", kind)
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_permit"); n != 0 {
				t.Fatal("forged permit survived", n)
			}
		})
	}
}
func TestTVEpisodeUpgradeDatabaseOriginalReturnAndSiblingPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	c, nonce, owner, token := tvUpgradeDBSession(t, f, r.OperationID)
	if _, e := c.Exec(t.Context(), tvUpgradeInsertTestPermit, r.OperationID, "send", owner, token, nonce, "200 milliseconds", "db-send"); e != nil {
		t.Fatal(e)
	}
	// A different connection cannot authenticate the original physical return even
	// when it can read every copied owner/token/nonce and the original lease is live.
	if _, e := f.pool.Exec(t.Context(), `INSERT INTO tv_episode_upgrade_return(operation_id,action,owner,token,status,evidence) VALUES($1,'send',$2,$3,'confirmed','{}')`, r.OperationID, owner, token); e == nil {
		t.Fatal("copied return accepted")
	}
	if _, e := c.Exec(t.Context(), tvUpgradeInsertTestPermit, r.OperationID, "observe:sibling", uuid.New(), uuid.New(), nonce, "60 seconds", "sibling"); e == nil {
		t.Fatal("sibling started before physical return")
	}
	time.Sleep(250 * time.Millisecond)
	if _, e := c.Exec(t.Context(), `INSERT INTO tv_episode_upgrade_return(operation_id,action,owner,token,status,evidence) VALUES($1,'send',$2,$3,'confirmed','{}')`, r.OperationID, owner, token); e == nil {
		t.Fatal("expired original return accepted")
	}
	got, e := f.service.ReadCommand(t.Context(), f.actor, f.sub, "db-send")
	if e != nil || got.Status != "pending_requires_observation" || got.OperationID != r.OperationID {
		t.Fatal("safe unresolved receipt", got, e)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_return"); n != 0 {
		t.Fatal("forged return persisted", n)
	}
}
func TestTVEpisodeUpgradeLostReserveReadReceiptPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	r := f.reserved(t)
	recovered, e := f.service.ReadCommand(t.Context(), f.actor, f.sub, "reserve")
	if e != nil || recovered.OperationID != r.OperationID || recovered.Status != r.Status {
		t.Fatal(recovered, e)
	}
	if f.native.calls.Load() != 0 {
		t.Fatal("receipt issued native IO")
	}
	execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
	if _, e = f.service.ReadCommand(t.Context(), f.actor, f.sub, "reserve"); e == nil {
		t.Fatal("revoked account read old receipt")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_authority"); n != 1 {
		t.Fatal("GET changed authority", n)
	}
}

var _ = ports.ErrTVEpisodeUpgradeConflict