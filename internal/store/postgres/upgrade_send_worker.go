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

type MovieUpgradeSendWorkStore struct{ store *UpgradePreparationStore }

func NewMovieUpgradeSendWorkStore(s *UpgradePreparationStore) (*MovieUpgradeSendWorkStore, error) {
	if s == nil {
		return nil, ports.ErrUnavailable
	}
	return &MovieUpgradeSendWorkStore{store: s}, nil
}
func (r *MovieUpgradeSendWorkStore) LoadMovieUpgradeSendWork(ctx context.Context, req ports.MovieUpgradeSendJobRequest, actorID uuid.UUID) (out *ports.MovieUpgradeSendWork, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if actorID == uuid.Nil || req.OperationID == uuid.Nil || req.TaskID == uuid.Nil || req.CommandID == uuid.Nil || req.Lease.ID == uuid.Nil || req.Lease.Owner == "" || req.Lease.Token <= 0 {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := r.store.pool.Begin(work)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(work, tx)
	// This is an identity hint, never a permission check. Account authorization and
	// the established policy/subscription/root/unit/source lock order come next.
	actor, err := (&MovieUpgradeObservationRuntimeStore{store: r.store}).originalActor(work, tx, upgradeObservationHint{req.OperationID, req.TaskID, actorID})
	if err != nil {
		return nil, err
	}
	actor, _, err = r.store.authorizePreparation(work, tx, actor, req.OperationID)
	if err != nil {
		return nil, err
	}
	basis, err := r.store.loadMovieUpgradeBasis(work, tx, actor, req.OperationID, true)
	if err != nil {
		return nil, err
	}
	var claim, command json.RawMessage
	var valid bool
	err = tx.QueryRow(work, `SELECT to_jsonb(j),to_jsonb(b),b.operation_id=$2 AND b.task_id=$3 AND b.command_id=$4 AND b.actor_id=$5 AND j.state='running' AND j.lease_owner=$6 AND j.lease_token=$7 AND j.attempts=1 AND j.max_attempts=1 AND j.result IS NULL AND j.error IS NULL AND j.lease_until>clock_timestamp() AND pulse_is_upgrade_send_job(j) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_job_claim c WHERE c.job_id=j.id) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization a WHERE a.operation_id=b.operation_id) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 FOR UPDATE OF j`, req.Lease.ID, req.OperationID, req.TaskID, req.CommandID, actorID, req.Lease.Owner, req.Lease.Token).Scan(&claim, &command, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, preparationConflict()
	}
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, preparationConflict()
	}
	if len(req.ExpectedClaim) > 0 && !preparationSameJSON(req.ExpectedClaim, claim) {
		return nil, preparationConflict()
	}
	source, err := basisSource(work, tx, basis)
	if err != nil {
		return nil, err
	}
	req.ExpectedClaim = append([]byte(nil), claim...)
	out = &ports.MovieUpgradeSendWork{Request: req, Actor: actor, Target: basis.target, EndpointURL: source.endpoint, APIKeyEncrypted: source.key, DownloadURLEncrypted: source.link, MetainfoSHA256: [32]byte(source.digest)}
	verify := func() error {
		if _, _, e := r.store.authorizePreparation(work, tx, actor, req.OperationID); e != nil {
			return e
		}
		if e := basis.check(work, tx); e != nil {
			return e
		}
		var actual, actualCommand json.RawMessage
		var live bool
		e := tx.QueryRow(work, `SELECT to_jsonb(j),to_jsonb(b),j.state='running' AND j.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_job_claim c WHERE c.job_id=j.id) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization a WHERE a.operation_id=b.operation_id) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1`, req.Lease.ID).Scan(&actual, &actualCommand, &live)
		if e != nil {
			return e
		}
		if !live || !preparationSameJSON(claim, actual) || !preparationSameJSON(command, actualCommand) {
			return preparationConflict()
		}
		return nil
	}
	if err = verify(); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(work, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = verify(); err != nil {
		return nil, err
	}
	if err = tx.Commit(work); err != nil {
		return nil, err
	}
	return out, nil
}

type upgradeSendSource struct {
	endpoint  string
	key, link []byte
	digest    []byte
}

func basisSource(ctx context.Context, tx pgx.Tx, b *preparationBasis) (upgradeSendSource, error) {
	var out upgradeSendSource
	err := tx.QueryRow(ctx, `SELECT i.url,i.api_key_encrypted,c.link_encrypted,p.raw_sha256 FROM indexer i JOIN release_candidate c ON c.indexer_id=i.id JOIN movie_upgrade_preparation p ON p.operation_id=$3 WHERE i.id=$1 AND c.id=$2 AND i.config_version=c.source_config_version`, b.snapshot.SourceID, b.snapshot.CandidateID, b.operation).Scan(&out.endpoint, &out.key, &out.link, &out.digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, preparationConflict()
	}
	if err != nil {
		return out, err
	}
	if len(out.digest) != 32 {
		return out, preparationConflict()
	}
	return out, nil
}

var _ ports.MovieUpgradeSendWorkRepository = (*MovieUpgradeSendWorkStore)(nil)
