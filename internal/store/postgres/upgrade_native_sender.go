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
	"github.com/jackc/pgx/v5/pgconn"
)

type movieUpgradeNativeRepository struct {
	store  *UpgradePreparationStore
	driver ports.MovieUpgradeNativeSendDriver
	issuer *MovieUpgradeNativeDispatchVerifier
}

// Not registered with workers, HTTP or App. Driver and issuer must be assembled as one trusted pair.
func NewMovieUpgradeNativeSendRepository(store *UpgradePreparationStore, driver ports.MovieUpgradeNativeSendDriver, issuer *MovieUpgradeNativeDispatchVerifier) (ports.MovieUpgradeNativeSendRepository, error) {
	if store == nil || driver == nil || issuer == nil {
		return nil, preparationConflict()
	}
	return &movieUpgradeNativeRepository{store, driver, issuer}, nil
}

func (r *movieUpgradeNativeRepository) SendMovieUpgrade(ctx context.Context, actor *ports.Actor, op, owner uuid.UUID, proof ports.MovieUpgradeMetainfoProof, key string) (*ports.MovieUpgradeSendResultReceipt, error) {
	return r.sendMovieUpgrade(ctx, actor, op, owner, proof, key, nil)
}

func (r *movieUpgradeNativeRepository) sendMovieUpgrade(ctx context.Context, actor *ports.Actor, op, owner uuid.UUID, proof ports.MovieUpgradeMetainfoProof, key string, job *movieUpgradeSendJob) (out *ports.MovieUpgradeSendResultReceipt, err error) {
	defer func() {
		var e *pgconn.PgError
		if ctx.Err() == nil && errors.As(err, &e) && e.Code == "23514" && e.ConstraintName == "movie_upgrade_dispatch_lease_valid" {
			err = preparationConflict()
		}
		if !errors.Is(err, ports.ErrMovieUpgradeSendObservation) {
			err = upgradeError(ctx, err)
		}
	}()
	if owner == uuid.Nil || proof == nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	original, err := r.store.OpenMovieUpgradeSendSession(ctx, op)
	if err != nil {
		return nil, err
	}
	s := original.(*movieUpgradeSendSession)
	s.sendJob = job
	defer func() {
		if e := s.Close(ctx); err == nil && e != nil {
			err = e
		}
	}()
	descriptor, err := s.nativeDescriptor(ctx, actor, proof)
	if err != nil {
		return nil, err
	}
	attempt, err := r.driver.OpenMovieUpgradeNativeSend(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		return nil, ports.ErrUnavailable
	}
	defer func() {
		if e := attempt.Close(context.WithoutCancel(ctx)); err == nil && e != nil {
			err = e
		}
	}()
	if _, err = s.ArmMovieUpgradeSend(ctx, actor, owner, proof, key); err != nil {
		return nil, err
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = s.acquire(work); err != nil {
		return nil, err
	}
	defer func() { <-s.gate }()
	s.dispatchAllowed = true
	_, binding, err := s.commitMovieUpgradeDispatchLocked(work, actor, proof, key)
	if err != nil {
		return nil, err
	}
	raw := append([]byte(nil), proof.Bytes()...)
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != descriptor.RawSHA256 || binding.send.RawSHA256 != descriptor.RawSHA256 {
		return nil, preparationConflict()
	}
	permit := &movieUpgradeNativeDispatchProof{issuer: r.issuer, session: s, descriptor: descriptor, binding: binding, raw: raw}
	returned, err := attempt.ExecuteMovieUpgradeSend(ctx, permit)
	if err != nil {
		return nil, err
	}
	s.resultVerifier = attempt
	if job != nil {
		job.returning = true
	}
	// Physical return, never logical timeout, is the prerequisite for writeback.
	resultCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer done()
	return s.recordMovieUpgradeSendResultLocked(resultCtx, actor, key, returned)
}

func (s *movieUpgradeSendSession) nativeDescriptor(ctx context.Context, actor *ports.Actor, proof ports.MovieUpgradeMetainfoProof) (ports.MovieUpgradeNativeDescriptor, error) {
	var out ports.MovieUpgradeNativeDescriptor
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.conn.Begin(work)
	if err != nil {
		return out, err
	}
	defer upgradeRollback(work, tx)
	actor, _, err = s.store.authorizePreparation(work, tx, actor, s.operation)
	if err != nil {
		return out, err
	}
	var alreadyArmed bool
	if err = tx.QueryRow(work, "SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=$1)", s.operation).Scan(&alreadyArmed); err != nil {
		return out, err
	}
	if alreadyArmed {
		return out, ports.ErrMovieUpgradeSendObservation
	}
	b, err := s.store.loadMovieUpgradeBasis(work, tx, actor, s.operation, true)
	if err != nil {
		return out, err
	}
	if err = bindPreparedUpgradeBasis(work, tx, b); err != nil {
		return out, err
	}
	capture, err := s.store.capturePreparationProof(work, tx, b, proof, append([]byte(nil), proof.Bytes()...))
	if err != nil {
		return out, err
	}
	var task struct {
		ID       uuid.UUID `json:"id"`
		InfoHash string    `json:"info_hash"`
		SavePath string    `json:"save_path"`
	}
	if json.Unmarshal(b.rows["download_task"], &task) != nil || task.ID == uuid.Nil || task.InfoHash != capture.infoHash || task.SavePath != b.savePath {
		return out, preparationConflict()
	}
	if err = b.check(work, tx); err != nil {
		return out, err
	}
	if err = s.lockSendJob(work, tx, actor); err != nil {
		return out, err
	}
	out = ports.MovieUpgradeNativeDescriptor{OperationID: s.operation, TaskID: task.ID, ClientID: b.snapshot.ClientID, ClientDigest: b.snapshot.ClientDigest, SavePath: task.SavePath, InfoHash: task.InfoHash, RawSHA256: hex.EncodeToString(capture.sha[:])}
	return out, tx.Commit(work)
}
