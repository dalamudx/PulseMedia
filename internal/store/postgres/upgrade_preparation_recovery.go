package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ ports.PublicMovieUpgradePreparationRepository = (*UpgradePreparationStore)(nil)

// Historical recovery has no freshness or lease requirement. It grants no new
// work and validates account/object permission before reading any receipt.
func (s *UpgradePreparationStore) ReadMovieUpgradePreparation(ctx context.Context, a *ports.Actor, c ports.PublicMovieUpgradePreparation, key string) (out *ports.MovieUpgradePreparationResult, err error) {
	defer func() { err = upgradeError(ctx, err) }()
	if c.SubscriptionID == uuid.Nil || c.OperationID == uuid.Nil || !c.Confirmed || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(ctx, tx)
	_, sub, err := s.authorizePreparation(ctx, tx, a, c.OperationID)
	if err != nil {
		return nil, err
	}
	if sub != c.SubscriptionID {
		return nil, ports.ErrNotFound
	}
	key = "upgrade.prepare:" + key
	q := db.New(tx)
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "upgrade.command", AggregateID: a.ID + ":" + key}); err != nil {
		return nil, err
	}
	receipt, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		// A prepared operation without this original receipt is not a fresh attempt.
		var prepared bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_preparation WHERE operation_id=$1)`, c.OperationID).Scan(&prepared); err != nil {
			return nil, err
		}
		if prepared {
			return nil, preparationConflict()
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	var owner, token, task uuid.UUID
	var raw []byte
	var requestKey, hash string
	err = tx.QueryRow(ctx, `SELECT owner,token,task_id,raw_sha256,request_key,request_hash FROM movie_upgrade_preparation WHERE operation_id=$1`, c.OperationID).Scan(&owner, &token, &task, &raw, &requestKey, &hash)
	if err != nil {
		return nil, preparationRequiredRow(err)
	}
	if owner == uuid.Nil || token == uuid.Nil || task == uuid.Nil || len(raw) != 32 || requestKey != key {
		return nil, preparationConflict()
	}
	rawSHA := hex.EncodeToString(raw)
	expectedHash := coverageDigest([]any{"upgrade.prepare.v1", c.OperationID, owner, token, rawSHA})
	out = &ports.MovieUpgradePreparationResult{OperationID: c.OperationID, TaskID: task, RawSHA256: rawSHA, Status: "prepared_not_sent", ExecutionAllowed: false}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if hash != expectedHash || receipt.RequestHash != hash || receipt.ResponseCode != 202 || !preparationSameJSON(receipt.ResponseBody, body) {
		return nil, preparationConflict()
	}
	if err = verifyPreparationReplay(ctx, tx, a, ports.PrepareMovieUpgrade{OperationID: c.OperationID, Owner: owner, Token: token}, key, hash, rawSHA, out); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UpgradePreparationStore) LoadMovieUpgradePreparationWork(ctx context.Context, a *ports.Actor, c ports.PublicMovieUpgradePreparation, l ports.MovieUpgradePreparationLease) (out *ports.MovieUpgradePreparationWork, err error) {
	defer func() { err = upgradeError(ctx, err) }()
	if c.SubscriptionID == uuid.Nil || c.OperationID == uuid.Nil || !c.Confirmed || l.OperationID != c.OperationID || l.Owner == uuid.Nil || l.Token == uuid.Nil {
		return nil, preparationConflict()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(ctx, tx)
	_, sub, err := s.authorizePreparation(ctx, tx, a, c.OperationID)
	if err != nil {
		return nil, err
	}
	if sub != c.SubscriptionID {
		return nil, ports.ErrNotFound
	}
	b, err := s.loadPreparationBasis(ctx, tx, a, c.OperationID)
	if err != nil {
		return nil, err
	}
	claim := ports.PrepareMovieUpgrade{OperationID: c.OperationID, Owner: l.Owner, Token: l.Token}
	if _, err = b.lockLease(ctx, tx, claim); err != nil {
		return nil, err
	}
	out = &ports.MovieUpgradePreparationWork{Target: b.target}
	err = tx.QueryRow(ctx, `SELECT i.url,i.api_key_encrypted,r.link_encrypted FROM indexer i JOIN release_candidate r ON r.indexer_id=i.id WHERE i.id=$1 AND r.id=$2 AND i.config_version=r.source_config_version`, b.snapshot.SourceID, b.snapshot.CandidateID).Scan(&out.EndpointURL, &out.APIKeyEncrypted, &out.DownloadURLEncrypted)
	if err != nil {
		return nil, preparationRequiredRow(err)
	}
	out.EndpointURL, err = resolveUpgradeSourceURL(ctx, tx, b, out.EndpointURL)
	if err != nil {
		return nil, err
	}
	verify := func() error {
		if _, _, e := s.authorizePreparation(ctx, tx, a, c.OperationID); e != nil {
			return e
		}
		_, e := b.lockLease(ctx, tx, claim)
		return e
	}
	if err = verify(); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = verify(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
