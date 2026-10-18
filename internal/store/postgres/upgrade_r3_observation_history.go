package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Receipt reads use a separate authorized transaction. Neither the original
// session gate nor a persisted permit becomes executable by recovering history.
func (r *movieUpgradeObservationRepository) ReadMovieUpgradeObservationAdmission(ctx context.Context, a *ports.Actor, op, owner uuid.UUID, key string) (out *ports.MovieUpgradeObservationReceipt, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if op == uuid.Nil || owner == uuid.Nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, e := r.store.authorizePreparation(work, tx, a, op)
	if e != nil {
		return nil, e
	}
	out, own, e := readUpgradeObservationAdmission(work, tx, a, sub, op, owner, key)
	if e != nil {
		return nil, e
	}
	if e = own.verify(work, tx); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}
func readUpgradeObservationAdmission(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op, owner uuid.UUID, key string) (*ports.MovieUpgradeObservationReceipt, ownedUpgradeRows, error) {
	var raw json.RawMessage
	var storedKey string
	var storedOwner uuid.UUID
	e := tx.QueryRow(ctx, `SELECT to_jsonb(o),o.request_key,o.owner FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit p USING(observation_id,operation_id) WHERE o.operation_id=$1 FOR SHARE OF o,p`, op).Scan(&raw, &storedKey, &storedOwner)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, ports.ErrMovieUpgradeObservationReview
	}
	if e != nil {
		return nil, nil, e
	}
	if storedKey != key || storedOwner != owner {
		return nil, nil, preparationConflict()
	}
	var out ports.MovieUpgradeObservationReceipt
	if e = json.Unmarshal(raw, &out); e != nil {
		return nil, nil, e
	}
	out.Status = "observation_only"
	hash, e := observationRequestHash(struct{ Operation, Owner uuid.UUID }{op, owner})
	if e != nil {
		return nil, nil, e
	}
	own, e := readUpgradeObservationLedger(ctx, tx, op, a, sub, key, hash, "upgrade.observation-admit", &out, true)
	return &out, own, e
}
func (r *movieUpgradeObservationRepository) ReadMovieUpgradeObservationResult(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (out *ports.MovieUpgradeObservationResult, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if op == uuid.Nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, e := r.store.authorizePreparation(work, tx, a, op)
	if e != nil {
		return nil, e
	}
	var binding ports.MovieUpgradeObservationBinding
	var outcome, storedKey, admissionKey string
	var returned time.Time
	e = tx.QueryRow(work, `SELECT o.observation_id,o.operation_id,o.task_id,o.owner,o.token,encode(sha256(convert_to(to_jsonb(o)::text,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(p)::text,'UTF8')),'hex'),o.request_key,r.request_key,r.outcome,r.returned_at FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit p USING(observation_id,operation_id) JOIN movie_upgrade_observation_result r USING(observation_id) WHERE o.operation_id=$1 FOR SHARE OF o,p,r`, op).Scan(&binding.ObservationID, &binding.OperationID, &binding.TaskID, &binding.Owner, &binding.Token, &binding.ObservationSHA256, &binding.PermitSHA256, &admissionKey, &storedKey, &outcome, &returned)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	if e != nil {
		return nil, e
	}
	if storedKey != key {
		return nil, preparationConflict()
	}
	_, admissionOwn, e := readUpgradeObservationAdmission(work, tx, a, sub, op, binding.Owner, admissionKey)
	if e != nil {
		return nil, e
	}
	out = &ports.MovieUpgradeObservationResult{ObservationID: binding.ObservationID, OperationID: op, TaskID: binding.TaskID, Outcome: outcome, Disposition: "manual_review"}
	hash, e := observationRequestHash(struct {
		Binding    ports.MovieUpgradeObservationBinding
		Outcome    string
		ReturnedAt time.Time
	}{binding, outcome, returned.UTC().Truncate(time.Microsecond)})
	if e != nil {
		return nil, e
	}
	own, e := readUpgradeObservationLedger(work, tx, op, a, sub, key, hash, "upgrade.observation-result", out, true)
	if e != nil {
		return nil, e
	}
	if e = admissionOwn.verify(work, tx); e != nil {
		return nil, e
	}
	if e = own.verify(work, tx); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}
