package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"time"
)

type movieUpgradeSendDispatchRepository struct{ store *UpgradePreparationStore }

// This constructor enables only the DB dispatch fence, never an external call.
func NewMovieUpgradeSendDispatchRepository(store *UpgradePreparationStore) (ports.MovieUpgradeSendDispatchRepository, error) {
	if store == nil {
		return nil, preparationConflict()
	}
	return &movieUpgradeSendDispatchRepository{store: store}, nil
}
func (r *movieUpgradeSendDispatchRepository) OpenMovieUpgradeSendDispatchSession(ctx context.Context, op uuid.UUID) (ports.MovieUpgradeSendDispatchSession, error) {
	original, err := r.store.OpenMovieUpgradeSendSession(ctx, op)
	if err != nil {
		return nil, err
	}
	s := original.(*movieUpgradeSendSession)
	s.dispatchAllowed = true
	return s, nil
}

// Only an in-package trusted execution boundary may ever consume this value.
// Public receipts deliberately omit it. No production consumer exists yet.
type movieUpgradeDispatchBinding struct {
	dispatchID uuid.UUID
	send       ports.MovieUpgradeSendReturnBinding
}

func (s *movieUpgradeSendSession) DispatchMovieUpgradeSend(ctx context.Context, actor *ports.Actor, proof ports.MovieUpgradeMetainfoProof, key string) (out *ports.MovieUpgradeSendDispatchReceipt, err error) {
	defer func() {
		var pgError *pgconn.PgError
		if ctx.Err() == nil && errors.As(err, &pgError) && pgError.Code == "23514" && pgError.ConstraintName == "movie_upgrade_dispatch_lease_valid" {
			err = preparationConflict()
		}
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
	out, _, err = s.commitMovieUpgradeDispatchLocked(work, actor, proof, key)
	return out, err
}

// The caller holds the original session gate. A future trusted synchronous
// driver must keep it held across this UoW and its one physical call.
func (s *movieUpgradeSendSession) commitMovieUpgradeDispatchLocked(ctx context.Context, actor *ports.Actor, proof ports.MovieUpgradeMetainfoProof, key string) (out *ports.MovieUpgradeSendDispatchReceipt, binding movieUpgradeDispatchBinding, err error) {
	defer func() {
		if err != nil {
			out = nil
			binding = movieUpgradeDispatchBinding{}
		}
	}()
	if s.closed || !s.spent || len(s.armExpected) == 0 || !s.dispatchAllowed || s.dispatchAttempted {
		return nil, binding, ports.ErrMovieUpgradeSendObservation
	}
	if err = episodeReviewActor(actor); err != nil {
		return nil, binding, err
	}
	if proof == nil || !upgradeKey(key) {
		return nil, binding, preparationConflict()
	}
	// Even an uncertain Commit or an earlier failure never reopens this session.
	s.dispatchAttempted = true
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return nil, binding, err
	}
	defer upgradeRollback(ctx, tx)
	actor, sub, err := s.store.authorizePreparation(ctx, tx, actor, s.operation)
	if err != nil {
		return nil, binding, err
	}
	b, err := s.store.loadMovieUpgradeBasis(ctx, tx, actor, s.operation, true)
	if err != nil {
		return nil, binding, err
	}
	if err = bindPreparedUpgradeBasis(ctx, tx, b); err != nil {
		return nil, binding, err
	}
	capture, err := s.store.capturePreparationProof(ctx, tx, b, proof, append([]byte(nil), proof.Bytes()...))
	if err != nil {
		return nil, binding, err
	}
	if err = s.lockSendJob(ctx, tx, actor); err != nil {
		return nil, binding, err
	}
	var expected json.RawMessage
	err = tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'operation_id',a.operation_id,'dispatch_id',$2::uuid,'task_id',a.task_id,'actor_id',a.actor_id,
 'owner',a.owner,'token',a.token,'session_nonce',a.session_nonce,'advisory_key',a.advisory_key,
 'backend_pid',a.backend_pid,'backend_start',a.backend_start,
 'arm_sha256',sha256(convert_to(to_jsonb(a)::text,'UTF8')),'raw_sha256',p.raw_sha256,
 'arm_lease_until',a.lease_until,'dispatched_at',clock_timestamp())
 FROM movie_upgrade_send_authorization a JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id
 WHERE a.operation_id=$1 FOR SHARE OF a,p`, s.operation, s.store.ids.NewUUID()).Scan(&expected)
	if err != nil {
		return nil, binding, preparationRequiredRow(err)
	}
	var row struct {
		OperationID  uuid.UUID `json:"operation_id"`
		DispatchID   uuid.UUID `json:"dispatch_id"`
		TaskID       uuid.UUID `json:"task_id"`
		Owner        uuid.UUID `json:"owner"`
		Token        uuid.UUID `json:"token"`
		DispatchedAt time.Time `json:"dispatched_at"`
		ArmSHA256    string    `json:"arm_sha256"`
		RawSHA256    string    `json:"raw_sha256"`
	}
	if err = json.Unmarshal(expected, &row); err != nil {
		return nil, binding, err
	}
	armHash, ok := pgByteaSHA256(row.ArmSHA256)
	if !ok {
		return nil, binding, preparationConflict()
	}
	rawHash, ok := pgByteaSHA256(row.RawSHA256)
	if !ok || rawHash != hex.EncodeToString(capture.sha[:]) {
		return nil, binding, preparationConflict()
	}
	binding = movieUpgradeDispatchBinding{dispatchID: row.DispatchID, send: ports.MovieUpgradeSendReturnBinding{OperationID: s.operation, TaskID: row.TaskID, Owner: row.Owner, Token: row.Token, ArmSHA256: armHash, RawSHA256: rawHash}}
	checkOriginal := func() error { return s.checkMovieUpgradeDispatch(ctx, tx, b, capture.proofJSON, rawHash, nil) }
	if err = checkOriginal(); err != nil {
		return nil, binding, err
	}
	var actual json.RawMessage
	err = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_send_dispatch
 SELECT d.* FROM jsonb_populate_record(NULL::movie_upgrade_send_dispatch,$1::jsonb)d
 RETURNING to_jsonb(movie_upgrade_send_dispatch)`, expected).Scan(&actual)
	if err != nil {
		return nil, binding, err
	}
	if !preparationSameJSON(actual, expected) {
		return nil, binding, preparationConflict()
	}
	check := func() error { return s.checkMovieUpgradeDispatch(ctx, tx, b, capture.proofJSON, rawHash, expected) }
	if err = check(); err != nil {
		return nil, binding, err
	}
	out = &ports.MovieUpgradeSendDispatchReceipt{OperationID: s.operation, DispatchID: row.DispatchID, TaskID: row.TaskID, DispatchedAt: row.DispatchedAt, Status: "dispatch_recorded_not_sent", ExecutionAllowed: false}
	hash := coverageDigest([]any{"upgrade.dispatch.v1", expected, capture.proofJSON})
	ledger := &UpgradeReservationStore{pool: s.store.pool, ids: s.store.ids}
	if err = ledger.upgradeSuccess(ctx, tx, actor, sub, "upgrade.dispatch:"+key, hash, "upgrade.dispatch", s.operation, out, b.own, check); err != nil {
		return nil, binding, err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return nil, binding, err
	}
	// This rechecks source deadlines and the Arm lease after all blocking reads.
	if err = check(); err != nil {
		return nil, binding, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, binding, err
	}
	s.dispatchExpected = append(json.RawMessage(nil), expected...)
	return out, binding, nil
}

func (s *movieUpgradeSendSession) checkMovieUpgradeDispatch(ctx context.Context, tx pgx.Tx, b *preparationBasis, proof json.RawMessage, rawHash string, expected json.RawMessage) error {
	if err := b.check(ctx, tx); err != nil {
		return err
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT to_jsonb(a)=$2::jsonb AND a.status='armed_not_sent'
 AND a.preparation_snapshot=to_jsonb(p) AND a.task_snapshot=to_jsonb(t)
 AND p.task_snapshot=to_jsonb(t) AND t.state='submitting' AND a.proof=$3::jsonb
 AND p.evidence->'proof'=$3::jsonb AND p.raw_sha256=decode($4,'hex')
 AND a.actor_id=$5 AND a.backend_pid=pg_backend_pid()
 AND a.backend_start=(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())
 AND a.session_nonce=$6 AND a.session_nonce::text=current_setting('pulsemedia.upgrade_send_nonce',true)
 AND a.advisory_key=$7 AND a.lease_until>clock_timestamp() AND $8::timestamptz>clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=$1)
 AND CASE WHEN $9::jsonb IS NULL THEN NOT EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch WHERE operation_id=$1)
 ELSE EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch d WHERE d.operation_id=$1 AND to_jsonb(d)=$9::jsonb) END
 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND mode='ExclusiveLock'
 AND granted AND objsubid=1 AND classid=((a.advisory_key>>32)&4294967295)::oid AND objid=(a.advisory_key&4294967295)::oid)
 FROM movie_upgrade_send_authorization a JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id
 JOIN download_task t ON t.id=a.task_id WHERE a.operation_id=$1`, s.operation, s.armExpected, proof, rawHash, b.actor.ID, s.nonce, s.key, b.snapshot.ValidUntil, expected).Scan(&valid)
	if err != nil {
		return preparationRequiredRow(err)
	}
	if !valid {
		return preparationConflict()
	}
	if s.sendJob != nil {
		return s.sendJob.check(ctx, tx, s)
	}
	return nil
}
