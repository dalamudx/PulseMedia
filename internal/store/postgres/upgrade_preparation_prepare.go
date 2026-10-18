package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *UpgradePreparationStore) PrepareMovieUpgrade(
	ctx context.Context, supplied *ports.Actor,
	c ports.PrepareMovieUpgrade, key string,
) (out *ports.MovieUpgradePreparationResult, err error) {
	if c.OperationID == uuid.Nil || c.Owner == uuid.Nil || c.Token == uuid.Nil ||
		c.Proof == nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(ctx, tx)

	a, sub, err := s.authorizePreparation(ctx, tx, supplied, c.OperationID)
	if err != nil {
		return nil, err
	}
	// Copy bytes. A caller cannot mutate the slice used to form the request hash.
	initialRaw := append([]byte(nil), c.Proof.Bytes()...)
	if len(initialRaw) == 0 || len(initialRaw) > 10<<20 {
		return nil, preparationConflict()
	}
	sum := sha256.Sum256(initialRaw)
	rawSHA := hex.EncodeToString(sum[:])
	key = "upgrade.prepare:" + key
	hash := coverageDigest([]any{
		"upgrade.prepare.v1", c.OperationID, c.Owner, c.Token, rawSHA,
	})
	q := db.New(tx)
	var old ports.MovieUpgradePreparationResult
	found, err := upgradeReceipt(ctx, q, a, key, hash, &old)
	if err != nil {
		return nil, err
	}
	if found {
		if err = verifyPreparationReplay(
			ctx, tx, a, c, key, hash, rawSHA, &old,
		); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &old, nil
	}

	b, err := s.loadPreparationBasis(ctx, tx, a, c.OperationID)
	if err != nil {
		return nil, fmt.Errorf("load frozen basis: %w", err)
	}
	_, err = b.lockLease(ctx, tx, c)
	if err != nil {
		return nil, fmt.Errorf("lock preparation lease: %w", err)
	}
	proof, err := s.capturePreparationProof(ctx, tx, b, c.Proof, initialRaw)
	if err != nil {
		return nil, fmt.Errorf("verify native metainfo: %w", err)
	}
	if err = b.check(ctx, tx); err != nil {
		return nil, fmt.Errorf("check initial basis: %w", err)
	}
	evidence, err := b.evidence(proof)
	if err != nil {
		return nil, fmt.Errorf("construct preparation evidence: %w", err)
	}
	task := s.ids.NewUUID()
	if task == uuid.Nil {
		return nil, preparationConflict()
	}
	expectedTask, err := preparationTaskTemplate(
		ctx, tx, b, task, proof.infoHash, proof.total)
	if err != nil {
		return nil, fmt.Errorf("construct task template: %w", err)
	}
	if err = insertPreparationTask(ctx, tx, task, expectedTask); err != nil {
		return nil, fmt.Errorf("verify inserted task: %w", err)
	}
	b.taskCount = 1
	b.rows["download_task"] = expectedTask
	if err = b.check(ctx, tx); err != nil {
		return nil, fmt.Errorf("check task write: %w", err)
	}

	prepared, err := insertMovieUpgradePreparation(
		ctx, tx, b, task, key, hash, c, proof, evidence, expectedTask)
	if err != nil {
		return nil, fmt.Errorf("verify inserted preparation: %w", err)
	}
	b.prepCount = 1
	b.rows["movie_upgrade_preparation"] = prepared
	if err = b.check(ctx, tx); err != nil {
		return nil, fmt.Errorf("check preparation write: %w", err)
	}

	// The only permitted mutation is the dedicated lease transition.
	var leaseRaw json.RawMessage
	err = tx.QueryRow(ctx, `
WITH written AS (
 UPDATE movie_upgrade_preparation_lease
 SET state='prepared'
 WHERE operation_id=$1 AND actor_id=$2 AND owner=$3 AND token=$4
  AND state='leased' AND lease_until>clock_timestamp()
 RETURNING *
)
SELECT to_jsonb(written) FROM written`,
		c.OperationID, uuid.MustParse(a.ID), c.Owner, c.Token).Scan(&leaseRaw)
	if err != nil {
		return nil, preparationRequiredRow(err)
	}
	var sameLease bool
	if err = tx.QueryRow(ctx, `SELECT $1::jsonb = jsonb_set($2::jsonb,'{state}','"prepared"'::jsonb)`, leaseRaw, b.rows["movie_upgrade_preparation_lease"]).Scan(&sameLease); err != nil {
		return nil, err
	}
	if !sameLease {
		return nil, preparationConflict()
	}
	b.rows["movie_upgrade_preparation_lease"] = leaseRaw
	if err = b.check(ctx, tx); err != nil {
		return nil, fmt.Errorf("check prepared lease: %w", err)
	}

	out = &ports.MovieUpgradePreparationResult{
		OperationID: c.OperationID, TaskID: task, RawSHA256: rawSHA,
		Status: "prepared_not_sent", ExecutionAllowed: false,
	}
	// Reuse the existing durable audit/outbox/receipt transaction helper.
	// Its checks run after each write, after SET CONSTRAINTS ALL IMMEDIATE,
	// and once more before this function commits.
	success := &UpgradeReservationStore{pool: s.pool, ids: s.ids}
	if err = success.upgradeSuccess(
		ctx, tx, a, sub, key, hash, "upgrade.prepare", c.OperationID,
		out, b.own, func() error { return b.check(ctx, tx) },
	); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func verifyPreparationReplay(
	ctx context.Context, tx pgx.Tx, a *ports.Actor,
	c ports.PrepareMovieUpgrade, key, hash, rawSHA string,
	out *ports.MovieUpgradePreparationResult,
) error {
	if out.OperationID != c.OperationID || out.TaskID == uuid.Nil ||
		out.RawSHA256 != rawSHA || out.Status != "prepared_not_sent" ||
		out.ExecutionAllowed {
		return preparationConflict()
	}
	var valid bool
	err := tx.QueryRow(ctx, `
SELECT p.operation_id=$1 AND p.actor_id=$2
 AND p.request_key=$3 AND p.request_hash=$4
 AND p.task_id=$5 AND encode(p.raw_sha256,'hex')=$6
 AND p.owner=$7 AND p.token=$8 AND p.status='prepared_not_sent'
FROM movie_upgrade_preparation p WHERE operation_id=$1`,
		c.OperationID, uuid.MustParse(a.ID), key, hash, out.TaskID,
		rawSHA, c.Owner, c.Token).Scan(&valid)
	if err != nil {
		return preparationRequiredRow(err)
	}
	if !valid {
		return preparationConflict()
	}
	// Historical replay deliberately does not require a current lease,
	// fresh source evidence, or native proof reissuance. Current account and
	// object permissions were checked before reading the receipt.
	return nil
}
