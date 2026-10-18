package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

type movieUpgradeNativeObservationRepository struct {
	store  *UpgradePreparationStore
	driver ports.MovieUpgradeNativeObservationDriver
	issuer *MovieUpgradeNativeQueryVerifier
}

// Construction is explicit; this repository is not registered with App/workers.
func NewMovieUpgradeNativeObservationRepository(s *UpgradePreparationStore, d ports.MovieUpgradeNativeObservationDriver, v *MovieUpgradeNativeQueryVerifier) (ports.MovieUpgradeNativeObservationRepository, error) {
	if s == nil || d == nil || v == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeNativeObservationRepository{s, d, v}, nil
}
func (r *movieUpgradeNativeObservationRepository) ObserveMovieUpgrade(ctx context.Context, a *ports.Actor, op, owner uuid.UUID, admissionKey, resultKey string) (*ports.MovieUpgradeObservationResult, error) {
	return r.observeMovieUpgrade(ctx, a, op, owner, admissionKey, resultKey, nil)
}
func (r *movieUpgradeNativeObservationRepository) observeMovieUpgrade(ctx context.Context, a *ports.Actor, op, owner uuid.UUID, admissionKey, resultKey string, job *movieUpgradeObservationJob) (out *ports.MovieUpgradeObservationResult, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if op == uuid.Nil || owner == uuid.Nil || !upgradeKey(admissionKey) || !upgradeKey(resultKey) {
		return nil, preparationConflict()
	}
	opened, err := (&movieUpgradeObservationRepository{store: r.store}).OpenMovieUpgradeObservationSession(ctx, op)
	if err != nil {
		return nil, err
	}
	s := opened.(*movieUpgradeObservationSession)
	s.job = job
	defer func() {
		if e := s.Close(ctx); err == nil && e != nil {
			err = e
		}
	}()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err = s.base.acquire(work); err != nil {
		return nil, err
	}
	defer func() { <-s.base.gate }()
	descriptor, err := s.nativeObservationDescriptor(work, a)
	if err != nil {
		return nil, err
	}
	attempt, err := r.driver.OpenMovieUpgradeNativeObservation(ctx, descriptor)
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
	admissionCtx, done := context.WithTimeout(ctx, 15*time.Second)
	_, err = s.admitMovieUpgradeObservationLocked(admissionCtx, a, owner, admissionKey)
	done()
	if err != nil {
		return nil, err
	}
	query := &movieUpgradeNativeQueryProof{issuer: r.issuer, session: s, actor: a, descriptor: descriptor}
	returned, err := attempt.QueryMovieUpgrade(ctx, query)
	if err != nil {
		return nil, err
	}
	s.verifier = attempt
	resultCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finish()
	return s.recordMovieUpgradeObservationLocked(resultCtx, a, resultKey, returned)
}
func (s *movieUpgradeObservationSession) nativeObservationDescriptor(ctx context.Context, a *ports.Actor) (ports.MovieUpgradeNativeObservationDescriptor, error) {
	var out ports.MovieUpgradeNativeObservationDescriptor
	tx, err := s.base.conn.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer upgradeRollback(ctx, tx)
	_, _, verify, err := s.load(ctx, tx, a)
	if err != nil {
		return out, err
	}
	var existing bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation WHERE operation_id=$1)", s.base.operation).Scan(&existing); err != nil {
		return out, err
	}
	if existing {
		return out, ports.ErrMovieUpgradeObservationReview
	}
	out.OperationID = s.base.operation
	out.OperationTag = "pulsemedia:op:" + s.base.operation.String()
	err = tx.QueryRow(ctx, `SELECT p.task_id,a.client_id,p.evidence->>'client_digest',p.evidence->'proof'->>'info_hash',p.evidence->>'save_path',(p.evidence->'proof'->>'total_bytes')::bigint FROM movie_upgrade_preparation p JOIN movie_upgrade_authority a USING(operation_id) WHERE p.operation_id=$1`, s.base.operation).Scan(&out.TaskID, &out.ClientID, &out.ClientDigest, &out.InfoHash, &out.SavePath, &out.TotalBytes)
	if err != nil {
		return out, err
	}
	if err = verify(); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
