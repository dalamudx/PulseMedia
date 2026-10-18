package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"time"
)

type movieUpgradeObservationRepository struct {
	store    *UpgradePreparationStore
	verifier ports.MovieUpgradeObservationVerifier
}
type movieUpgradeObservationSession struct {
	base                *movieUpgradeSendSession
	verifier            ports.MovieUpgradeObservationVerifier
	attempted           bool
	admitted            bool
	admissionLedger     ownedUpgradeRows
	observation, permit json.RawMessage
	receipt             ports.MovieUpgradeObservationReceipt
	job                 *movieUpgradeObservationJob
}

// Only construction can supply the trusted verifier. No native issuer or
// downloader lookup is registered by this repository.
func NewMovieUpgradeObservationRepository(s *UpgradePreparationStore, verifier ports.MovieUpgradeObservationVerifier) (ports.MovieUpgradeObservationRepository, error) {
	if s == nil || verifier == nil {
		return nil, errors.New("missing upgrade observation dependency")
	}
	return &movieUpgradeObservationRepository{s, verifier}, nil
}
func (r *movieUpgradeObservationRepository) OpenMovieUpgradeObservationSession(ctx context.Context, op uuid.UUID) (ports.MovieUpgradeObservationSession, error) {
	opened, e := r.store.OpenMovieUpgradeSendSession(ctx, op)
	if e != nil {
		return nil, upgradeObservationError(ctx, e)
	}
	base, ok := opened.(*movieUpgradeSendSession)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	if _, e = base.conn.Exec(ctx, "SELECT set_config('pulsemedia.upgrade_observation_nonce',$1,false)", base.nonce.String()); e != nil {
		if closeErr := base.Close(context.WithoutCancel(ctx)); closeErr != nil {
			return nil, ports.ErrUnavailable
		}
		return nil, upgradeObservationError(ctx, e)
	}
	return &movieUpgradeObservationSession{base: base, verifier: r.verifier}, nil
}
func (s *movieUpgradeObservationSession) Close(ctx context.Context) error { return s.base.Close(ctx) }
func upgradeObservationError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, ports.ErrMovieUpgradeSendObservation) || errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
		return ports.ErrMovieUpgradeObservationReview
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && (p.Code == "P0001" || (p.Code == "23514" && p.ConstraintName == "movie_upgrade_observation_lease")) {
		return preparationConflict()
	}
	return upgradeError(ctx, e)
}

// This takes all existing current-account/business locks before reading return
// history. It does not call the source-freshness/old-send-lease preparation path.
func (s *movieUpgradeObservationSession) load(ctx context.Context, tx pgx.Tx, a *ports.Actor) (*ports.Actor, uuid.UUID, func() error, error) {
	a, sub, e := s.base.store.authorizePreparation(ctx, tx, a, s.base.operation)
	if e != nil {
		return nil, sub, nil, e
	}
	var arm json.RawMessage
	e = tx.QueryRow(ctx, `SELECT to_jsonb(a) FROM movie_upgrade_send_authorization a JOIN movie_upgrade_send_result r USING(operation_id) JOIN movie_upgrade_send_dispatch d USING(operation_id) WHERE a.operation_id=$1 AND r.status='unknown'`, s.base.operation).Scan(&arm)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, sub, nil, ports.ErrMovieUpgradeObservationReview
	}
	if e != nil {
		return nil, sub, nil, e
	}
	if len(s.base.armExpected) != 0 && !preparationSameJSON(s.base.armExpected, arm) {
		return nil, sub, nil, preparationConflict()
	}
	s.base.armExpected = arm
	_, verify, e := s.base.lockSendReturnLedger(ctx, tx, a, arm)
	if e != nil {
		return nil, sub, nil, e
	}
	var clientID uuid.UUID
	var stamp string
	e = tx.QueryRow(ctx, `SELECT client_id,evidence->>'client_digest' FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id) WHERE a.operation_id=$1`, s.base.operation).Scan(&clientID, &stamp)
	if e != nil {
		return nil, sub, nil, e
	}
	client, e := db.New(tx).GetDownloadClientByID(ctx, clientID)
	if e != nil {
		return nil, sub, nil, e
	}
	if episodeClientStamp(client) != stamp {
		return nil, sub, nil, preparationConflict()
	}
	if s.job != nil {
		if e = s.job.lock(ctx, tx, s, a); e != nil {
			return nil, sub, nil, e
		}
	}
	return a, sub, func() error {
		if e := verify(ctx); e != nil {
			return e
		}
		if s.job != nil {
			return s.job.check(ctx, tx, s)
		}
		return nil
	}, nil
}
func (s *movieUpgradeObservationSession) check(ctx context.Context, tx pgx.Tx, a *ports.Actor, verify func() error, own ownedUpgradeRows, result json.RawMessage) error {
	if _, _, e := s.base.store.authorizePreparation(ctx, tx, a, s.base.operation); e != nil {
		return e
	}
	if e := verify(); e != nil {
		return e
	}
	if e := own.verify(ctx, tx); e != nil {
		return e
	}
	if e := s.admissionLedger.verify(ctx, tx); e != nil {
		return e
	}
	for table, condition := range map[string]string{
		"audit_log":          "action IN ('upgrade.observation-admit','upgrade.observation-result') AND payload->>'operation_id'=$1::text",
		"outbox_event":       "event_type IN ('upgrade.observation-admit','upgrade.observation-result') AND aggregate_id=$1::text",
		"idempotency_record": "(idempotency_key LIKE 'upgrade.observation-admit:%' OR idempotency_key LIKE 'upgrade.observation-result:%') AND resource_id=$1::text",
	} {
		var count int
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+condition, s.base.operation.String()).Scan(&count); e != nil {
			return e
		}
		if count != len(own[table])+len(s.admissionLedger[table]) {
			return preparationConflict()
		}
	}
	for table, want := range map[string]json.RawMessage{"movie_upgrade_observation": s.observation, "movie_upgrade_observation_permit": s.permit, "movie_upgrade_observation_result": result} {
		var raw json.RawMessage
		var count int
		e := tx.QueryRow(ctx, "SELECT count(*)::int,min(to_jsonb(x)::text)::jsonb FROM "+table+" x WHERE observation_id=$1", s.receipt.ObservationID).Scan(&count, &raw)
		if e != nil {
			return e
		}
		if len(want) == 0 {
			if count != 0 {
				return preparationConflict()
			}
		} else if count != 1 || !preparationSameJSON(raw, want) {
			return preparationConflict()
		}
	}
	// Final SQL check proves the original observation session and checks the DB
	// clock AFTER all graph, ledger and deferred-trigger reads.
	if _, e := tx.Exec(ctx, "SELECT pulse_assert_upgrade_observation($1)", s.receipt.ObservationID); e != nil {
		return e
	}
	if s.job != nil {
		return s.job.check(ctx, tx, s)
	}
	return nil
}
func (s *movieUpgradeObservationSession) AdmitMovieUpgradeObservation(ctx context.Context, a *ports.Actor, owner uuid.UUID, key string) (out *ports.MovieUpgradeObservationReceipt, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if owner == uuid.Nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e := s.base.acquire(work); e != nil {
		return nil, e
	}
	defer func() { <-s.base.gate }()
	return s.admitMovieUpgradeObservationLocked(work, a, owner, key)
}

// Caller holds the original session gate; this method performs no network I/O.
func (s *movieUpgradeObservationSession) admitMovieUpgradeObservationLocked(work context.Context, a *ports.Actor, owner uuid.UUID, key string) (out *ports.MovieUpgradeObservationReceipt, err error) {
	if s.base.closed || s.attempted {
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	s.attempted = true
	tx, e := s.base.conn.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, verify, e := s.load(work, tx, a)
	if e != nil {
		return nil, e
	}
	var exists bool
	if e = tx.QueryRow(work, "SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation WHERE operation_id=$1)", s.base.operation).Scan(&exists); e != nil {
		return nil, e
	}
	if exists {
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	obs, token := s.base.store.ids.NewUUID(), s.base.store.ids.NewUUID()
	if obs == uuid.Nil || token == uuid.Nil {
		return nil, preparationConflict()
	}
	var expected, jsonPermit json.RawMessage
	e = tx.QueryRow(work, `SELECT jsonb_build_object('observation_id',$2::uuid,'operation_id',p.operation_id,'task_id',p.task_id,'actor_id',p.actor_id,'request_key',$7::text,'owner',$3::uuid,'token',$4::uuid,'session_nonce',$5::uuid,'backend_pid',pg_backend_pid(),'backend_start',(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()),'advisory_key',$6::bigint,'admitted_at',t.n,'lease_until',t.n+interval '60 seconds','client_id',a.client_id,'infohash',p.evidence->'proof'->>'info_hash','save_path',p.evidence->>'save_path','task_tag','pulsemedia:op:'||p.operation_id::text,'total_bytes',(p.evidence->'proof'->>'total_bytes')::bigint,'original_sha256',pulse_upgrade_observation_original_digest(p.operation_id),'protected_sha256',pulse_upgrade_observation_protected_digest(p.operation_id)) FROM movie_upgrade_preparation p JOIN movie_upgrade_authority a USING(operation_id) CROSS JOIN (SELECT clock_timestamp() AS n) t WHERE p.operation_id=$1`, s.base.operation, obs, owner, token, s.base.nonce, s.base.key, key).Scan(&expected)
	if e != nil {
		return nil, e
	}
	var row json.RawMessage
	if e = tx.QueryRow(work, `INSERT INTO movie_upgrade_observation SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_observation,$1::jsonb) RETURNING to_jsonb(movie_upgrade_observation)`, expected).Scan(&row); e != nil {
		return nil, e
	}
	if !preparationSameJSON(row, expected) {
		return nil, preparationConflict()
	}
	s.observation = expected
	if e = json.Unmarshal(expected, &s.receipt); e != nil {
		return nil, e
	}
	s.receipt.Status = "observation_only"
	e = tx.QueryRow(work, `SELECT jsonb_build_object('observation_id',observation_id,'operation_id',operation_id,'issued_at',admitted_at) FROM movie_upgrade_observation WHERE observation_id=$1`, obs).Scan(&jsonPermit)
	if e != nil {
		return nil, e
	}
	if e = tx.QueryRow(work, `INSERT INTO movie_upgrade_observation_permit SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_observation_permit,$1::jsonb) RETURNING to_jsonb(movie_upgrade_observation_permit)`, jsonPermit).Scan(&row); e != nil {
		return nil, e
	}
	if !preparationSameJSON(row, jsonPermit) {
		return nil, preparationConflict()
	}
	s.permit = jsonPermit
	if s.job != nil {
		if e = s.job.admit(work, tx, s); e != nil {
			return nil, e
		}
	}
	hash, e := observationRequestHash(struct{ Operation, Owner uuid.UUID }{s.base.operation, owner})
	if e != nil {
		return nil, e
	}
	own, e := s.ledger(work, tx, a, sub, key, hash, "upgrade.observation-admit", &s.receipt, false)
	if e != nil {
		return nil, e
	}
	check := func() error { return s.check(work, tx, a, verify, own, nil) }
	if e = check(); e != nil {
		return nil, e
	}
	writer := &UpgradeReservationStore{ids: s.base.store.ids}
	if e = writer.upgradeSuccess(work, tx, a, sub, "upgrade.observation-admit:"+key, hash, "upgrade.observation-admit", s.base.operation, &s.receipt, own, check); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(work, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	s.admitted = true
	s.admissionLedger = own
	receipt := s.receipt
	return &receipt, nil
}
