package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type movieUpgradeSendSession struct {
	store                              *UpgradePreparationStore
	conn                               *pgx.Conn
	operation, nonce                   uuid.UUID
	key                                int64
	gate                               chan struct{}
	closed, spent                      bool
	resultVerifier                     ports.MovieUpgradeSendReturnVerifier
	sendJob                            *movieUpgradeSendJob
	armExpected                        json.RawMessage
	dispatchAllowed, dispatchAttempted bool
	dispatchExpected                   json.RawMessage
}

var _ ports.MovieUpgradeSendSessionRepository = (*UpgradePreparationStore)(nil)

func (s *UpgradePreparationStore) OpenMovieUpgradeSendSession(ctx context.Context, op uuid.UUID) (ports.MovieUpgradeSendSession, error) {
	if op == uuid.Nil {
		return nil, preparationConflict()
	}
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, upgradeError(ctx, err)
	}
	session := &movieUpgradeSendSession{store: s, conn: conn, operation: op, nonce: s.ids.NewUUID(), gate: make(chan struct{}, 1)}
	sum := sha256.Sum256([]byte("pulsemedia.movie-upgrade.physical.v1:" + op.String()))
	session.key = int64(binary.BigEndian.Uint64(sum[:8]))
	var held bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, session.key).Scan(&held)
	if err == nil && !held {
		err = ports.ErrMovieUpgradeSendObservation
	}
	if err == nil {
		_, err = conn.Exec(ctx, `SELECT set_config('pulsemedia.upgrade_send_nonce',$1,false)`, session.nonce.String())
	}
	if err != nil {
		_ = session.Close(ctx)
		if errors.Is(err, ports.ErrMovieUpgradeSendObservation) {
			return nil, err
		}
		return nil, upgradeError(ctx, err)
	}
	return session, nil
}
func (s *movieUpgradeSendSession) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *movieUpgradeSendSession) Close(ctx context.Context) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.acquire(cleanup); err != nil {
		return err
	}
	defer func() { <-s.gate }()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Close(cleanup)
}
func (s *movieUpgradeSendSession) ArmMovieUpgradeSend(ctx context.Context, a *ports.Actor, owner uuid.UUID, proof ports.MovieUpgradeMetainfoProof, key string) (out *ports.MovieUpgradeSendArmReceipt, err error) {
	defer func() {
		if !errors.Is(err, ports.ErrMovieUpgradeSendObservation) {
			err = upgradeError(ctx, err)
		}
	}()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = s.acquire(work); err != nil {
		return nil, err
	}
	defer func() { <-s.gate }()
	if s.closed || s.spent {
		return nil, ports.ErrMovieUpgradeSendObservation
	}
	if owner == uuid.Nil || !upgradeKey(key) || proof == nil {
		return nil, preparationConflict()
	}
	tx, err := s.conn.Begin(work)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(work, tx)
	a, sub, err := s.store.authorizePreparation(work, tx, a, s.operation)
	if err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=$1)`, s.operation).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ports.ErrMovieUpgradeSendObservation
	}
	b, err := s.store.loadMovieUpgradeBasis(work, tx, a, s.operation, true)
	if err != nil {
		return nil, err
	}
	capture, err := s.store.capturePreparationProof(work, tx, b, proof, append([]byte(nil), proof.Bytes()...))
	if err != nil {
		return nil, err
	}
	if err = s.lockSendJob(work, tx, a); err != nil {
		return nil, err
	}
	var matches bool
	if err = tx.QueryRow(work, `SELECT raw_sha256=$2 AND evidence->'proof'=$3::jsonb FROM movie_upgrade_preparation WHERE operation_id=$1`, s.operation, capture.sha[:], capture.proofJSON).Scan(&matches); err != nil {
		return nil, err
	}
	if !matches {
		return nil, preparationConflict()
	}
	var task struct {
		ID uuid.UUID `json:"id"`
	}
	if json.Unmarshal(b.rows["download_task"], &task) != nil || task.ID == uuid.Nil {
		return nil, preparationConflict()
	}
	token := s.store.ids.NewUUID()
	fields, err := json.Marshal(map[string]any{"operation_id": s.operation, "task_id": task.ID, "actor_id": a.ID, "owner": owner, "token": token, "session_nonce": s.nonce, "advisory_key": s.key, "status": "armed_not_sent", "preparation_snapshot": b.rows["movie_upgrade_preparation"], "task_snapshot": b.rows["download_task"], "proof": capture.proofJSON})
	if err != nil {
		return nil, err
	}
	var expected json.RawMessage
	err = tx.QueryRow(work, `WITH t AS(SELECT clock_timestamp() AS now)
 SELECT $1::jsonb||jsonb_build_object('backend_pid',pg_backend_pid(),'backend_start',backend_start,'armed_at',t.now,'lease_until',t.now+interval '60 seconds')
 FROM pg_stat_activity,t WHERE pid=pg_backend_pid()`, fields).Scan(&expected)
	if err != nil {
		return nil, err
	}
	var actual json.RawMessage
	err = tx.QueryRow(work, `INSERT INTO movie_upgrade_send_authorization SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_send_authorization,$1::jsonb) RETURNING to_jsonb(movie_upgrade_send_authorization)`, expected).Scan(&actual)
	if err != nil {
		return nil, err
	}
	if !preparationSameJSON(expected, actual) {
		return nil, preparationConflict()
	}
	check := func() error {
		if e := b.check(work, tx); e != nil {
			return e
		}
		var valid bool
		e := tx.QueryRow(work, `SELECT to_jsonb(x)=$2::jsonb AND x.backend_pid=pg_backend_pid()
 AND x.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 AND x.session_nonce::text=current_setting('pulsemedia.upgrade_send_nonce',true)
 AND x.lease_until>clock_timestamp() AND $3::timestamptz>clock_timestamp()
 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock'
 AND objsubid=1 AND classid=((x.advisory_key>>32)&4294967295)::oid AND objid=(x.advisory_key&4294967295)::oid)
 FROM movie_upgrade_send_authorization x WHERE operation_id=$1`, s.operation, expected, b.snapshot.ValidUntil).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return preparationConflict()
		}
		if s.sendJob != nil {
			return s.sendJob.check(work, tx, s)
		}
		return nil
	}
	var times struct {
		LeaseUntil time.Time `json:"lease_until"`
	}
	if json.Unmarshal(expected, &times) != nil {
		return nil, preparationConflict()
	}
	out = &ports.MovieUpgradeSendArmReceipt{OperationID: s.operation, TaskID: task.ID, Token: token, LeaseUntil: times.LeaseUntil, Status: "armed_not_sent"}
	hashBytes := sha256.Sum256(append([]byte(s.operation.String()+owner.String()), capture.sha[:]...))
	ledger := &UpgradeReservationStore{pool: s.store.pool, ids: s.store.ids}
	if err = ledger.upgradeSuccess(work, tx, a, sub, "upgrade.arm:"+key, hex.EncodeToString(hashBytes[:]), "upgrade.arm", s.operation, out, b.own, check); err != nil {
		return nil, err
	}
	// A commit error is ambiguous; this object never authorizes another attempt.
	s.spent = true
	if err = tx.Commit(work); err != nil {
		return nil, err
	}
	s.armExpected = append(json.RawMessage(nil), expected...)
	return out, nil
}
