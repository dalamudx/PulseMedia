package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type movieUpgradeSendResultRepository struct {
	store    *UpgradePreparationStore
	verifier ports.MovieUpgradeSendReturnVerifier
}

// NewMovieUpgradeSendResultRepository enables recording only with a trusted verifier.
func NewMovieUpgradeSendResultRepository(store *UpgradePreparationStore, verifier ports.MovieUpgradeSendReturnVerifier) (ports.MovieUpgradeSendSessionRepository, error) {
	if store == nil || verifier == nil {
		return nil, preparationConflict()
	}
	return &movieUpgradeSendResultRepository{store: store, verifier: verifier}, nil
}
func (r *movieUpgradeSendResultRepository) OpenMovieUpgradeSendSession(ctx context.Context, op uuid.UUID) (ports.MovieUpgradeSendSession, error) {
	session, e := r.store.OpenMovieUpgradeSendSession(ctx, op)
	if e != nil {
		return nil, e
	}
	s := session.(*movieUpgradeSendSession)
	s.resultVerifier = r.verifier
	return s, nil
}

type movieUpgradeSendResultRow struct {
	OperationID uuid.UUID       `json:"operation_id"`
	TaskID      uuid.UUID       `json:"task_id"`
	ActorID     uuid.UUID       `json:"actor_id"`
	Owner       uuid.UUID       `json:"owner"`
	Token       uuid.UUID       `json:"token"`
	RequestKey  string          `json:"request_key"`
	Status      string          `json:"status"`
	ReturnedAt  time.Time       `json:"returned_at"`
	RecordedAt  time.Time       `json:"recorded_at"`
	RawSHA256   string          `json:"raw_sha256"`
	ArmSHA256   string          `json:"arm_sha256"`
	ArmSnapshot json.RawMessage `json:"arm_snapshot"`
}

// pgByteaSHA256 validates PostgreSQL's JSON representation of a BYTEA digest.
func pgByteaSHA256(value string) (string, bool) {
	if len(value) != 66 || value[:2] != `\x` {
		return "", false
	}
	b, err := hex.DecodeString(value[2:])
	if err != nil || len(b) != sha256.Size {
		return "", false
	}
	return hex.EncodeToString(b), true
}

func resultReceipt(
	row movieUpgradeSendResultRow,
) *ports.MovieUpgradeSendResultReceipt {
	return &ports.MovieUpgradeSendResultReceipt{
		OperationID:      row.OperationID,
		TaskID:           row.TaskID,
		Owner:            row.Owner,
		Token:            row.Token,
		Status:           row.Status,
		ReturnedAt:       row.ReturnedAt,
		RecordedAt:       row.RecordedAt,
		ExecutionAllowed: false,
	}
}

func (s *movieUpgradeSendSession) RecordMovieUpgradeSendResult(
	ctx context.Context,
	actor *ports.Actor,
	key string,
	proof ports.MovieUpgradeSendReturnProof,
) (out *ports.MovieUpgradeSendResultReceipt, err error) {
	defer func() {
		if !errors.Is(err, ports.ErrMovieUpgradeSendObservation) {
			err = upgradeError(ctx, err)
		}
	}()

	// Includes gate wait, verification, locking and all writes.
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err = s.acquire(work); err != nil {
		return nil, err
	}
	defer func() { <-s.gate }()

	return s.recordMovieUpgradeSendResultLocked(work, actor, key, proof)
}

// The caller retains the original gate through physical return and result writeback.
// This method performs no network I/O and preserves the independent schema67 path.
func (s *movieUpgradeSendSession) recordMovieUpgradeSendResultLocked(work context.Context, actor *ports.Actor, key string, proof ports.MovieUpgradeSendReturnProof) (out *ports.MovieUpgradeSendResultReceipt, err error) {
	if s.closed || !s.spent || len(s.armExpected) == 0 {
		return nil, ports.ErrMovieUpgradeSendObservation
	}
	if s.resultVerifier == nil ||
		proof == nil || !upgradeKey(key) || len(key) > 256 {
		return nil, preparationConflict()
	}

	tx, err := s.conn.Begin(work)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(work, tx)

	// Explicitly retain the supplied existing account authorization contract.
	actor, _, err = s.store.authorizePreparation(
		work, tx, actor, s.operation,
	)
	if err != nil {
		return nil, err
	}

	writeLedger, verifyLedger, err := s.lockSendReturnLedger(
		work, tx, actor, append(json.RawMessage(nil), s.armExpected...),
	)
	if err != nil {
		return nil, err
	}
	if err = s.lockSendResultJob(work, tx, actor); err != nil {
		return nil, err
	}
	if writeLedger == nil || verifyLedger == nil {
		return nil, preparationConflict()
	}

	// Business row locks must already have been acquired in the real order.
	// This query only reads the original immutable graph.
	var arm json.RawMessage
	var armHash, rawHash string
	var taskID, owner, token, armedActor uuid.UUID
	var valid bool
	err = tx.QueryRow(work, `
SELECT to_jsonb(a),
 encode(sha256(convert_to(to_jsonb(a)::text,'UTF8')),'hex'),
 encode(p.raw_sha256,'hex'),
 a.task_id,a.owner,a.token,a.actor_id,
 to_jsonb(a)=$2::jsonb
 AND a.operation_id=$1
 AND a.status='armed_not_sent'
 AND a.preparation_snapshot=to_jsonb(p)
 AND a.task_snapshot=to_jsonb(t)
 AND p.task_snapshot=to_jsonb(t)
 AND p.task_id=a.task_id
 AND p.actor_id=a.actor_id
 AND p.evidence->'proof'=a.proof
 AND a.session_nonce=$3
 AND a.advisory_key=$4
 AND a.backend_pid=pg_backend_pid()
 AND a.backend_start=(
  SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()
 )
 AND a.session_nonce::text=
  current_setting('pulsemedia.upgrade_send_nonce',true)
FROM movie_upgrade_send_authorization a
JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id
JOIN download_task t ON t.id=a.task_id
WHERE a.operation_id=$1`,
		s.operation, s.armExpected, s.nonce, s.key,
	).Scan(
		&arm, &armHash, &rawHash,
		&taskID, &owner, &token, &armedActor, &valid,
	)
	if err != nil {
		return nil, err
	}
	if !valid || actor == nil || armedActor.String() != actor.ID {
		return nil, preparationConflict()
	}

	binding := ports.MovieUpgradeSendReturnBinding{
		OperationID: s.operation,
		TaskID:      taskID,
		Owner:       owner,
		Token:       token,
		ArmSHA256:   armHash,
		RawSHA256:   rawHash,
	}
	verified, verifyErr := s.resultVerifier.VerifyMovieUpgradeSendReturn(
		work, proof, binding,
	)
	if verifyErr != nil {
		if work.Err() != nil {
			return nil, work.Err()
		}
		// Never propagate a verifier's potentially external error text.
		return nil, preparationConflict()
	}
	if verified.Binding != binding ||
		verified.ReturnedAt.IsZero() ||
		(verified.Status != "confirmed" && verified.Status != "unknown") {
		return nil, preparationConflict()
	}

	// PostgreSQL timestamps have microsecond precision. Require canonical
	// precision rather than silently changing a verified timestamp.
	returnedAt := verified.ReturnedAt.UTC()
	if !returnedAt.Equal(returnedAt.Truncate(time.Microsecond)) {
		return nil, preparationConflict()
	}

	// Compare the existing row against complete verifier-derived fields.
	// A historical read cannot change key, outcome, binding or return time.
	var historical json.RawMessage
	err = tx.QueryRow(work, `
SELECT to_jsonb(r)
FROM movie_upgrade_send_result r
WHERE operation_id=$1`, s.operation).Scan(&historical)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var expected json.RawMessage
	if exists {
		var row movieUpgradeSendResultRow
		if json.Unmarshal(historical, &row) != nil {
			return nil, preparationConflict()
		}
		originalRawHash, okRaw := pgByteaSHA256(row.RawSHA256)
		originalArmHash, okArm := pgByteaSHA256(row.ArmSHA256)
		if !okRaw || !okArm ||
			row.OperationID != binding.OperationID ||
			row.TaskID != binding.TaskID ||
			row.ActorID.String() != actor.ID ||
			row.Owner != binding.Owner ||
			row.Token != binding.Token ||
			row.RequestKey != key ||
			row.Status != verified.Status ||
			!row.ReturnedAt.Equal(returnedAt) ||
			row.RecordedAt.IsZero() ||
			originalRawHash != binding.RawSHA256 ||
			originalArmHash != binding.ArmSHA256 ||
			!preparationSameJSON(row.ArmSnapshot, arm) {
			return nil, preparationConflict()
		}
		expected = append(json.RawMessage(nil), historical...)
	} else {
		// Construct the entire expected row before INSERT, including DB time.
		fields, marshalErr := json.Marshal(map[string]any{
			"operation_id": s.operation,
			"task_id":      taskID,
			"actor_id":     actor.ID,
			"owner":        owner,
			"token":        token,
			"request_key":  key,
			"status":       verified.Status,
			"returned_at":  returnedAt,
			"raw_sha256":   `\x` + rawHash,
			"arm_sha256":   `\x` + armHash,
			"arm_snapshot": arm,
		})
		if marshalErr != nil {
			return nil, marshalErr
		}
		err = tx.QueryRow(work, `
SELECT $1::jsonb ||
 jsonb_build_object('recorded_at',clock_timestamp(),'returned_at',$2::timestamptz)`,
			fields, returnedAt,
		).Scan(&expected)
		if err != nil {
			return nil, err
		}
	}

	var row movieUpgradeSendResultRow
	if json.Unmarshal(expected, &row) != nil {
		return nil, preparationConflict()
	}
	out = resultReceipt(row)

	// All graph checks operate on original persisted identities.
	// Do not reload a freshness-based preparation basis here.
	checkGraph := func() error {
		currentActor, _, e := s.store.authorizePreparation(
			work, tx, actor, s.operation,
		)
		if e != nil {
			return e
		}
		if currentActor == nil || currentActor.ID != armedActor.String() {
			return preparationConflict()
		}

		var current json.RawMessage
		e = tx.QueryRow(work, `
SELECT to_jsonb(r)
FROM movie_upgrade_send_result r
WHERE operation_id=$1`, s.operation).Scan(&current)
		if e != nil {
			return e
		}
		if !preparationSameJSON(expected, current) {
			return preparationConflict()
		}

		// Re-run the same DB graph/session/lease guard. A manually invoked
		// trigger function is unavailable, so enqueue its constraint trigger
		// only through INSERT, then retain an equivalent read check here.
		var same bool
		e = tx.QueryRow(work, `
SELECT to_jsonb(a)=$2::jsonb
 AND a.preparation_snapshot=to_jsonb(p)
 AND a.task_snapshot=to_jsonb(t)
 AND p.task_snapshot=to_jsonb(t)
 AND p.task_id=a.task_id AND p.actor_id=a.actor_id
 AND p.evidence->'proof'=a.proof
 AND encode(p.raw_sha256,'hex')=$3
 AND a.owner=$4 AND a.token=$5
 AND a.session_nonce=$6 AND a.advisory_key=$7
 AND a.backend_pid=pg_backend_pid()
 AND a.backend_start=(
  SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()
 )
 AND a.session_nonce::text=
  current_setting('pulsemedia.upgrade_send_nonce',true)
 AND a.armed_at<=clock_timestamp()
 AND a.lease_until>clock_timestamp()
 AND $8::timestamptz>=a.armed_at
 AND $8::timestamptz<=$9::timestamptz
 AND $9::timestamptz<=clock_timestamp()
 AND $9::timestamptz<a.lease_until
 AND EXISTS(
  SELECT 1 FROM pg_locks
  WHERE pid=pg_backend_pid() AND locktype='advisory'
   AND mode='ExclusiveLock' AND granted AND objsubid=1
   AND classid=((a.advisory_key>>32)&4294967295)::oid
   AND objid=(a.advisory_key&4294967295)::oid
 )
FROM movie_upgrade_send_authorization a
JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id
JOIN download_task t ON t.id=a.task_id
WHERE a.operation_id=$1`,
			s.operation, s.armExpected, rawHash, owner, token,
			s.nonce, s.key, row.ReturnedAt, row.RecordedAt,
		).Scan(&same)
		if e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}

		// Repeat the session-lock ownership proof on every check. The
		// immutable Arm already binds this key to the canonical operation.
		_, e = tx.Exec(work,
			`SELECT pg_advisory_xact_lock_shared($1::bigint)`, s.key)
		if e != nil {
			return e
		}
		var unlocked, restored bool
		e = tx.QueryRow(work,
			`SELECT pg_advisory_unlock($1::bigint)`, s.key,
		).Scan(&unlocked)
		if e != nil {
			return e
		}
		if !unlocked {
			return preparationConflict()
		}
		e = tx.QueryRow(work,
			`SELECT pg_try_advisory_lock($1::bigint)`, s.key,
		).Scan(&restored)
		if e != nil {
			return e
		}
		if !restored {
			return preparationConflict()
		}

		// Strict lease check after the lock ownership proof.
		e = tx.QueryRow(work, `
SELECT owner=$2 AND token=$3 AND lease_until>clock_timestamp()
FROM movie_upgrade_send_authorization
WHERE operation_id=$1`, s.operation, owner, token).Scan(&same)
		if e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		return nil
	}

	if !exists {
		// The ledger bridge must have captured and checked the expected
		// pre-write state, including absence of success ledger records.
		if err = verifyLedger(work); err != nil {
			return nil, err
		}

		var actual json.RawMessage
		err = tx.QueryRow(work, `
INSERT INTO movie_upgrade_send_result
SELECT * FROM jsonb_populate_record(
 NULL::movie_upgrade_send_result,$1::jsonb
)
RETURNING to_jsonb(movie_upgrade_send_result)`,
			expected,
		).Scan(&actual)
		if err != nil {
			return nil, err
		}
		if !preparationSameJSON(expected, actual) {
			return nil, preparationConflict()
		}
	}

	check := func() error {
		if e := checkGraph(); e != nil {
			return e
		}
		if _, e := tx.Exec(work, `SELECT pulse_assert_upgrade_preparation($1)`, s.operation); e != nil {
			return e
		}
		if err := verifyLedger(work); err != nil {
			return err
		}
		if s.sendJob != nil {
			return s.sendJob.check(work, tx, s)
		}
		return nil
	}
	if err = check(); err != nil {
		return nil, err
	}

	if !exists {
		// Hash the complete expected row, including key/outcome/timestamps.
		// Canonicalize in PostgreSQL rather than relying on Go JSON order.
		var requestHash string
		err = tx.QueryRow(work, `
SELECT encode(sha256(convert_to(($1::jsonb)::text,'UTF8')),'hex')`,
			expected,
		).Scan(&requestHash)
		if err != nil {
			return nil, err
		}
		if s.sendJob != nil {
			if err = s.sendJob.finish(work, tx); err != nil {
				return nil, err
			}
		}
		err = writeLedger(
			work,
			"upgrade.send-result:"+key,
			requestHash,
			out,
			check,
		)
		if err != nil {
			return nil, err
		}
		if err = check(); err != nil {
			return nil, err
		}
	}

	// Force all pending deferred checks, including checks produced by ledger
	// writes. Recheck after their triggers have had an opportunity to mutate.
	if _, err = tx.Exec(work, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = check(); err != nil {
		return nil, err
	}

	// Last blocking check: no subsequent preparation/ledger read can cross expiry.
	var validLease bool
	if err = tx.QueryRow(work, `SELECT to_jsonb(a)=$2::jsonb AND owner=$3 AND token=$4 AND status='armed_not_sent' AND lease_until>clock_timestamp() AND backend_pid=pg_backend_pid() FROM movie_upgrade_send_authorization a WHERE operation_id=$1`, s.operation, s.armExpected, owner, token).Scan(&validLease); err != nil {
		return nil, err
	}
	if !validLease {
		return nil, preparationConflict()
	}
	if s.sendJob != nil {
		if err = s.sendJob.check(work, tx, s); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(work); err != nil {
		return nil, err
	}
	return out, nil
}
