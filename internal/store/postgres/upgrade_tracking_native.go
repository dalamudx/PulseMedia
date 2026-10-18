package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type movieUpgradeNativeTrackingRepository struct {
	store  *UpgradePreparationStore
	driver ports.MovieUpgradeNativeTrackingDriver
	issuer *MovieUpgradeTrackingQueryVerifier
}

// The repository and the native driver must share this explicitly injected,
// private issuer. Caller-created interfaces or copied values are not credentials.
type MovieUpgradeTrackingQueryVerifier struct{ _ byte }

func NewMovieUpgradeTrackingQueryVerifier() *MovieUpgradeTrackingQueryVerifier {
	return &MovieUpgradeTrackingQueryVerifier{}
}
func NewMovieUpgradeNativeTrackingRepository(s *UpgradePreparationStore, d ports.MovieUpgradeNativeTrackingDriver, v *MovieUpgradeTrackingQueryVerifier) (ports.MovieUpgradeTrackingRepository, error) {
	if s == nil || d == nil || v == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeNativeTrackingRepository{s, d, v}, nil
}
func (r *movieUpgradeNativeTrackingRepository) TrackMovieUpgrade(ctx context.Context, a *ports.Actor, op, owner uuid.UUID, admissionKey, resultKey string) (*ports.MovieUpgradeTrackingResult, error) {
	return r.trackMovieUpgrade(ctx, a, op, owner, admissionKey, resultKey, nil)
}
func (r *movieUpgradeNativeTrackingRepository) trackMovieUpgrade(ctx context.Context, a *ports.Actor, op, owner uuid.UUID, admissionKey, resultKey string, job *movieUpgradeTrackingJob) (out *ports.MovieUpgradeTrackingResult, err error) {
	defer func() {
		if errors.Is(err, ports.ErrMovieUpgradeTrackingReview) {
			return
		}
		err = upgradeObservationError(ctx, err)
	}()
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if op == uuid.Nil || owner == uuid.Nil || !upgradeKey(admissionKey) || !upgradeKey(resultKey) {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if out, err = r.recover(work, a, op, admissionKey, resultKey); err != nil || out != nil {
		return out, err
	}
	opened, err := r.store.OpenMovieUpgradeSendSession(work, op)
	if err != nil {
		return nil, err
	}
	base := opened.(*movieUpgradeSendSession)
	defer func() {
		if e := base.Close(context.WithoutCancel(ctx)); err == nil && e != nil {
			err = e
		}
	}()
	if err = base.acquire(work); err != nil {
		return nil, err
	}
	defer func() { <-base.gate }()
	if _, err = base.conn.Exec(work, `SELECT set_config('pulsemedia.upgrade_tracking_nonce',$1,false)`, base.nonce.String()); err != nil {
		return nil, err
	}
	s := &movieUpgradeTrackingSession{base: base, admissionLedger: ownedUpgradeRows{}, job: job}
	tx, err := base.conn.Begin(work)
	if err != nil {
		return nil, err
	}
	_, _, verify, err := s.load(work, tx, a)
	if err == nil {
		err = verify()
	}
	if err == nil {
		err = tx.Commit(work)
	} else {
		upgradeRollback(work, tx)
	}
	if err != nil {
		return nil, err
	}
	attempt, err := r.driver.OpenMovieUpgradeNativeTracking(ctx, s.descriptor)
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
	admission, done := context.WithTimeout(ctx, 15*time.Second)
	err = s.admit(admission, a, owner, admissionKey)
	done()
	if err != nil {
		return nil, err
	}
	var lease struct {
		Until time.Time `json:"lease_until"`
	}
	if json.Unmarshal(s.observation, &lease) != nil || lease.Until.IsZero() {
		return nil, preparationConflict()
	}
	queryCtx, stopQuery := context.WithDeadline(ctx, lease.Until)
	defer stopQuery()
	query := &movieUpgradeTrackingQueryProof{issuer: r.issuer, session: s, actor: a, descriptor: s.descriptor}
	proof, err := attempt.QueryMovieUpgradeTracking(queryCtx, query)
	if err != nil {
		return nil, err
	}
	result, finish := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finish()
	return s.record(result, a, resultKey, attempt, proof)
}

type movieUpgradeTrackingQueryProof struct {
	issuer     *MovieUpgradeTrackingQueryVerifier
	session    *movieUpgradeTrackingSession
	actor      *ports.Actor
	descriptor ports.MovieUpgradeTrackingDescriptor
	consumed   atomic.Bool
}

func (v *MovieUpgradeTrackingQueryVerifier) ConsumeMovieUpgradeTrackingQuery(ctx context.Context, claim ports.MovieUpgradeTrackingQueryProof, expected ports.MovieUpgradeTrackingDescriptor) (ports.MovieUpgradeObservationBinding, error) {
	var zero ports.MovieUpgradeObservationBinding
	if e := ctx.Err(); e != nil {
		return zero, e
	}
	p, ok := claim.(*movieUpgradeTrackingQueryProof)
	if !ok || p == nil || p.issuer != v || p.session == nil || !sameTrackingDescriptor(p.descriptor, expected) || !p.consumed.CompareAndSwap(false, true) {
		return zero, preparationConflict()
	}
	s := p.session
	if s.base.closed || len(s.observation) == 0 || len(s.permit) == 0 {
		return zero, ports.ErrMovieUpgradeTrackingReview
	}
	work, done := context.WithTimeout(ctx, 15*time.Second)
	defer done()
	tx, e := s.base.conn.Begin(work)
	if e != nil {
		return zero, e
	}
	defer upgradeRollback(work, tx)
	actor, _, verify, e := s.load(work, tx, p.actor)
	if e != nil {
		return zero, e
	}
	if !sameTrackingDescriptor(s.descriptor, expected) {
		return zero, preparationConflict()
	}
	if e = s.check(work, tx, actor, verify, ownedUpgradeRows{}, nil); e != nil {
		return zero, e
	}
	if e = tx.Commit(work); e != nil {
		return zero, e
	}
	return s.binding, nil
}
func sameTrackingDescriptor(a, b ports.MovieUpgradeTrackingDescriptor) bool {
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	return e == nil && preparationSameJSON(x, y)
}
