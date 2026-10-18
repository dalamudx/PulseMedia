package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type movieUpgradeTrackingSession struct {
	base                *movieUpgradeSendSession
	descriptor          ports.MovieUpgradeTrackingDescriptor
	binding             ports.MovieUpgradeObservationBinding
	observation, permit json.RawMessage
	admissionLedger     ownedUpgradeRows
	job                 *movieUpgradeTrackingJob
}

func (s *movieUpgradeTrackingSession) load(ctx context.Context, tx pgx.Tx, actor *ports.Actor) (*ports.Actor, uuid.UUID, func() error, error) {
	a, sub, e := s.base.store.authorizePreparation(ctx, tx, actor, s.base.operation)
	if e != nil {
		return nil, sub, nil, e
	}
	var arm json.RawMessage
	e = tx.QueryRow(ctx, `SELECT to_jsonb(a) FROM movie_upgrade_send_authorization a JOIN movie_upgrade_send_result r USING(operation_id) WHERE a.operation_id=$1 AND r.status='confirmed'`, s.base.operation).Scan(&arm)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, sub, nil, ports.ErrMovieUpgradeTrackingReview
	}
	if e != nil {
		return nil, sub, nil, e
	}
	if len(s.base.armExpected) > 0 && !preparationSameJSON(s.base.armExpected, arm) {
		return nil, sub, nil, preparationConflict()
	}
	s.base.armExpected = arm
	_, verify, e := s.base.lockSendReturnLedger(ctx, tx, a, arm)
	if e != nil {
		return nil, sub, nil, e
	}
	var raw json.RawMessage
	e = tx.QueryRow(ctx, `SELECT jsonb_build_object('identity',jsonb_build_object('OperationID',p.operation_id,'TaskID',p.task_id,'ClientID',a.client_id,'ClientDigest',p.evidence->>'client_digest','InfoHash',p.evidence->'proof'->>'info_hash','OperationTag','pulsemedia:op:'||p.operation_id::text,'SavePath',p.evidence->>'save_path','TotalBytes',(p.evidence->'proof'->>'total_bytes')::bigint),'target',p.evidence->'proof'->'target','files',p.evidence->'proof'->'files') FROM movie_upgrade_preparation p JOIN movie_upgrade_authority a USING(operation_id) WHERE p.operation_id=$1`, s.base.operation).Scan(&raw)
	if e != nil {
		return nil, sub, nil, e
	}
	var d ports.MovieUpgradeTrackingDescriptor
	if json.Unmarshal(raw, &d) != nil {
		return nil, sub, nil, preparationConflict()
	}
	if s.descriptor.Identity.OperationID != uuid.Nil {
		old, _ := json.Marshal(s.descriptor)
		if !preparationSameJSON(old, raw) {
			return nil, sub, nil, preparationConflict()
		}
	}
	s.descriptor = d
	client, e := db.New(tx).GetDownloadClientByID(ctx, d.Identity.ClientID)
	if e != nil {
		return nil, sub, nil, e
	}
	if episodeClientStamp(client) != d.Identity.ClientDigest {
		return nil, sub, nil, preparationConflict()
	}
	if s.job != nil {
		if e = s.job.lock(ctx, tx, s, a); e != nil {
			return nil, sub, nil, e
		}
	}
	return a, sub, func() error { return verify(ctx) }, nil
}

func (s *movieUpgradeTrackingSession) check(ctx context.Context, tx pgx.Tx, a *ports.Actor, verify func() error, own ownedUpgradeRows, result json.RawMessage) error {
	if _, _, e := s.base.store.authorizePreparation(ctx, tx, a, s.base.operation); e != nil {
		return e
	}
	if e := verify(); e != nil {
		return e
	}
	for _, rows := range []ownedUpgradeRows{s.admissionLedger, own} {
		if e := rows.verify(ctx, tx); e != nil {
			return e
		}
	}
	if s.job != nil {
		if e := s.job.check(ctx, tx); e != nil {
			return e
		}
	}
	for table, want := range map[string]json.RawMessage{"movie_upgrade_tracking_observation": s.observation, "movie_upgrade_tracking_permit": s.permit, "movie_upgrade_tracking_result": result} {
		var n int
		var raw json.RawMessage
		if e := tx.QueryRow(ctx, "SELECT count(*)::int,min(to_jsonb(x)::text)::jsonb FROM "+table+" x WHERE observation_id=$1", s.binding.ObservationID).Scan(&n, &raw); e != nil {
			return e
		}
		if len(want) == 0 {
			if n != 0 {
				return preparationConflict()
			}
		} else if n != 1 || !preparationSameJSON(raw, want) {
			return preparationConflict()
		}
	}
	for table, condition := range map[string]string{"audit_log": "action IN ('upgrade.tracking-admit','upgrade.tracking-result') AND payload->>'observation_id'=$1", "outbox_event": "event_type IN ('upgrade.tracking-admit','upgrade.tracking-result') AND aggregate_id=$1", "idempotency_record": "idempotency_key LIKE 'upgrade.tracking-%' AND resource_id=$1"} {
		var n int
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+condition, s.binding.ObservationID.String()).Scan(&n); e != nil {
			return e
		}
		if n != len(own[table])+len(s.admissionLedger[table]) {
			return preparationConflict()
		}
	}
	_, e := tx.Exec(ctx, `SELECT pulse_assert_upgrade_tracking($1)`, s.binding.ObservationID)
	return e
}

func (s *movieUpgradeTrackingSession) admit(ctx context.Context, a *ports.Actor, owner uuid.UUID, key string) error {
	tx, e := s.base.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, verify, e := s.load(ctx, tx, a)
	if e != nil {
		return e
	}
	var prior *uuid.UUID
	e = tx.QueryRow(ctx, `SELECT o.observation_id FROM movie_upgrade_tracking_observation o WHERE o.operation_id=$1 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=o.observation_id) FOR UPDATE`, s.base.operation).Scan(&prior)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if prior != nil {
		var ready bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_result WHERE observation_id=$1 AND outcome='tracking' AND recorded_at<=clock_timestamp()-interval '30 seconds')`, *prior).Scan(&ready)
		if e != nil {
			return e
		}
		if !ready {
			return ports.ErrMovieUpgradeTrackingReview
		}
	}
	descriptor, e := json.Marshal(s.descriptor)
	if e != nil {
		return e
	}
	id, token := s.base.store.ids.NewUUID(), s.base.store.ids.NewUUID()
	fields, e := json.Marshal(map[string]any{"observation_id": id, "operation_id": s.base.operation, "task_id": s.descriptor.Identity.TaskID, "actor_id": a.ID, "owner": owner, "token": token, "session_nonce": s.base.nonce, "advisory_key": s.base.key, "descriptor": json.RawMessage(descriptor), "prior_observation_id": prior, "request_key": key})
	if e != nil {
		return e
	}
	var expected json.RawMessage
	e = tx.QueryRow(ctx, `WITH t AS(SELECT clock_timestamp() now) SELECT $1::jsonb||jsonb_build_object('backend_pid',pg_backend_pid(),'backend_start',backend_start,'admitted_at',t.now,'lease_until',t.now+interval '60 seconds','original_sha256',pulse_upgrade_tracking_original_digest($2),'protected_sha256',pulse_upgrade_observation_protected_digest($2)) FROM pg_stat_activity,t WHERE pid=pg_backend_pid()`, fields, s.base.operation).Scan(&expected)
	if e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_tracking_observation SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_tracking_observation,$1::jsonb) RETURNING to_jsonb(movie_upgrade_tracking_observation)`, expected).Scan(&s.observation); e != nil {
		return e
	}
	if !preparationSameJSON(expected, s.observation) {
		return preparationConflict()
	}
	var permitExpected json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT jsonb_build_object('observation_id',observation_id,'operation_id',operation_id,'owner',owner,'token',token,'session_nonce',session_nonce,'backend_pid',backend_pid,'backend_start',backend_start,'advisory_key',advisory_key,'issued_at',admitted_at) FROM movie_upgrade_tracking_observation WHERE observation_id=$1`, id).Scan(&permitExpected); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_tracking_permit SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_tracking_permit,$1::jsonb) RETURNING to_jsonb(movie_upgrade_tracking_permit)`, permitExpected).Scan(&s.permit); e != nil {
		return e
	}
	if !preparationSameJSON(s.permit, permitExpected) {
		return preparationConflict()
	}
	s.binding = ports.MovieUpgradeObservationBinding{ObservationID: id, OperationID: s.base.operation, TaskID: s.descriptor.Identity.TaskID, Owner: owner, Token: token}
	if e = tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(to_jsonb(o)::text,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(p)::text,'UTF8')),'hex') FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_permit p USING(observation_id) WHERE observation_id=$1`, id).Scan(&s.binding.ObservationSHA256, &s.binding.PermitSHA256); e != nil {
		return e
	}
	if s.job != nil {
		if e = s.job.capture(ctx, tx, s); e != nil {
			return e
		}
	}
	own := ownedUpgradeRows{}
	receipt := map[string]any{"observation_id": id, "operation_id": s.base.operation, "task_id": s.binding.TaskID, "status": "query_only", "execution_allowed": false}
	check := func() error { return s.check(ctx, tx, a, verify, own, nil) }
	ledger := &UpgradeReservationStore{pool: s.base.store.pool, ids: s.base.store.ids}
	if e = ledger.upgradeSuccess(ctx, tx, a, sub, "upgrade.tracking-admit:"+key, coverageDigest(s.binding), "upgrade.tracking-admit", id, receipt, own, check); e != nil {
		return e
	}
	if e = check(); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	s.admissionLedger = own
	return nil
}

func (s *movieUpgradeTrackingSession) record(ctx context.Context, a *ports.Actor, key string, attempt ports.MovieUpgradeTrackingAttempt, proof ports.MovieUpgradeTrackingProof) (*ports.MovieUpgradeTrackingResult, error) {
	tx, e := s.base.conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, verify, e := s.load(ctx, tx, a)
	if e != nil {
		return nil, e
	}
	if e = s.check(ctx, tx, a, verify, ownedUpgradeRows{}, nil); e != nil {
		return nil, e
	}
	v, e := attempt.VerifyMovieUpgradeTracking(ctx, proof, s.binding)
	if e != nil {
		return nil, e
	}
	if v.Binding != s.binding {
		return nil, preparationConflict()
	}
	disposition := "manual_review"
	switch v.Outcome {
	case "tracking":
		disposition = "tracking"
	case "completed":
		disposition = "organization_review_required"
	case "manual_review":
	default:
		return nil, preparationConflict()
	}
	fields, e := json.Marshal(map[string]any{"observation_id": s.binding.ObservationID, "request_key": key, "outcome": v.Outcome, "reason": v.Reason, "returned_at": v.ReturnedAt.UTC().Truncate(time.Microsecond), "observation_sha256": "\\x" + s.binding.ObservationSHA256, "permit_sha256": "\\x" + s.binding.PermitSHA256, "evidence": v.Evidence, "disposition": disposition})
	if e != nil {
		return nil, e
	}
	var expected, actual json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT to_jsonb(jsonb_populate_record(NULL::movie_upgrade_tracking_result,$1::jsonb||jsonb_build_object('recorded_at',clock_timestamp())))`, fields).Scan(&expected); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_tracking_result SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_tracking_result,$1::jsonb) RETURNING to_jsonb(movie_upgrade_tracking_result)`, expected).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(expected, actual) {
		return nil, preparationConflict()
	}
	if s.job != nil {
		if e = s.job.finish(ctx, tx, s); e != nil {
			return nil, e
		}
	}
	out := &ports.MovieUpgradeTrackingResult{OperationID: s.base.operation, TaskID: s.binding.TaskID, ObservationID: s.binding.ObservationID, Outcome: v.Outcome, Reason: v.Reason, Disposition: disposition}
	own := ownedUpgradeRows{}
	check := func() error { return s.check(ctx, tx, a, verify, own, expected) }
	ledger := &UpgradeReservationStore{pool: s.base.store.pool, ids: s.base.store.ids}
	if e = ledger.upgradeSuccess(ctx, tx, a, sub, "upgrade.tracking-result:"+key, coverageDigest(json.RawMessage(expected)), "upgrade.tracking-result", s.binding.ObservationID, out, own, check); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

// A historical receipt grants no new query, and remains readable only after the
// current account and object locks succeed. An orphan permit always blocks.
func (r *movieUpgradeNativeTrackingRepository) recover(ctx context.Context, a *ports.Actor, op uuid.UUID, admissionKey, resultKey string) (*ports.MovieUpgradeTrackingResult, error) {
	tx, e := r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, e := r.store.authorizePreparation(ctx, tx, a, op)
	if e != nil {
		return nil, e
	}
	var out ports.MovieUpgradeTrackingResult
	var raw json.RawMessage
	var actualKey string
	e = tx.QueryRow(ctx, `SELECT o.observation_id,o.task_id,r.request_key,r.outcome,r.reason,r.disposition,to_jsonb(r) FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE o.operation_id=$1 AND o.request_key=$2`, op, admissionKey).Scan(&out.ObservationID, &out.TaskID, &actualKey, &out.Outcome, &out.Reason, &out.Disposition, &raw)
	if errors.Is(e, pgx.ErrNoRows) {
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation WHERE operation_id=$1 AND request_key=$2)`, op, admissionKey).Scan(&exists); e != nil {
			return nil, e
		}
		if exists {
			return nil, ports.ErrMovieUpgradeTrackingReview
		}
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if actualKey != resultKey {
		return nil, preparationConflict()
	}
	out.OperationID = op
	body, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	// Hash the same canonical typed database row used at original commit.
	hash := coverageDigest(json.RawMessage(raw))
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		condition := "event_type='upgrade.tracking-result' AND aggregate_id=$1"
		if table == "audit_log" {
			condition = "action='upgrade.tracking-result' AND payload->>'observation_id'=$1"
		}
		if table == "idempotency_record" {
			condition = "idempotency_key=$2 AND resource_id=$1"
		}
		query := "SELECT id FROM " + table + " WHERE " + condition
		args := []any{out.ObservationID.String()}
		if table == "idempotency_record" {
			args = append(args, "upgrade.tracking-result:"+resultKey)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		ids := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(ids) != 1 {
			return nil, preparationConflict()
		}
		if e = upgradeValidateSuccess(ctx, tx, table, ids[0], a, sub, "upgrade.tracking-result:"+resultKey, hash, "upgrade.tracking-result", out.ObservationID, body); e != nil {
			return nil, e
		}
	}
	if _, _, e = r.store.authorizePreparation(ctx, tx, a, op); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &out, nil
}
