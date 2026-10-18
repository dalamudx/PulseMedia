package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestMovieUpgradeSendArmSQLLockNegativePG(t *testing.T) {
	for _, kind := range []string{"wrong_shared", "correct_shared", "correct_xact"} {
		t.Run(kind, func(t *testing.T) {
			f := armFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sum := sha256.Sum256([]byte("pulsemedia.movie-upgrade.physical.v1:" + f.operation.String()))
			key := int64(binary.BigEndian.Uint64(sum[:8]))
			mode := "pg_advisory_xact_lock_shared"
			switch kind {
			case "wrong_shared":
				_ = armSession(t, f)
				key = 123
			case "correct_xact":
				mode = "pg_advisory_xact_lock"
			}
			tx, e := f.p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer rollbackFrozenUpgradeTest(t, tx)
			nonce := uuid.New()
			if _, e = tx.Exec(ctx, `SELECT set_config('pulsemedia.upgrade_send_nonce',$1,false)`, nonce.String()); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(ctx, `SELECT `+mode+`($1::bigint)`, key); e != nil {
				t.Fatal(e)
			}
			_, e = tx.Exec(ctx, `WITH stamp AS(SELECT clock_timestamp() now) INSERT INTO movie_upgrade_send_authorization
 (operation_id,task_id,actor_id,owner,token,session_nonce,advisory_key,backend_pid,backend_start,armed_at,lease_until,status,preparation_snapshot,task_snapshot,proof)
 SELECT p.operation_id,p.task_id,p.actor_id,$2,$3,$4,$5,pg_backend_pid(),a.backend_start,stamp.now,stamp.now+interval '60 seconds','armed_not_sent',to_jsonb(p),p.task_snapshot,p.evidence->'proof'
 FROM movie_upgrade_preparation p,pg_stat_activity a,stamp WHERE p.operation_id=$1 AND a.pid=pg_backend_pid()`, f.operation, uuid.New(), uuid.New(), nonce, key)
			if e != nil {
				t.Fatal("failed before deferred lock assertion", e)
			}
			if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e == nil {
				t.Fatal("accepted noncanonical/shared/transaction-only execution lock")
			}
		})
	}
}
func TestMovieUpgradeSendArmGateCancellationPG(t *testing.T) {
	f := armFixture(t)
	s := armSession(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	holder, e := f.p.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = holder.Exec(ctx, `UPDATE app_user SET is_active=true WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "blocked"); done <- e }()
	defer func() { cancel(); rollbackFrozenUpgradeTest(t, holder); <-done }()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	other, stop := context.WithCancel(context.Background())
	stop()
	second := make(chan error, 1)
	go func() { _, e := s.ArmMovieUpgradeSend(other, f.actor, uuid.New(), f.proof, "canceled"); second <- e }()
	select {
	case e := <-second:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("canceled Arm waited for another Arm's gate")
	}
}
