package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"sync/atomic"
	"time"
)

// An explicitly assembled repository/driver pair shares this private identity.
type MovieUpgradeNativeQueryVerifier struct{ _ byte }

func NewMovieUpgradeNativeQueryVerifier() *MovieUpgradeNativeQueryVerifier {
	return &MovieUpgradeNativeQueryVerifier{}
}

type movieUpgradeNativeQueryProof struct {
	issuer     *MovieUpgradeNativeQueryVerifier
	session    *movieUpgradeObservationSession
	actor      *ports.Actor
	descriptor ports.MovieUpgradeNativeObservationDescriptor
	consumed   atomic.Bool
}

func (v *MovieUpgradeNativeQueryVerifier) ConsumeMovieUpgradeQuery(ctx context.Context, claim ports.MovieUpgradeNativeQueryProof, expected ports.MovieUpgradeNativeObservationDescriptor) (ports.MovieUpgradeObservationBinding, error) {
	var zero ports.MovieUpgradeObservationBinding
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, ok := claim.(*movieUpgradeNativeQueryProof)
	if !ok || p == nil || p.issuer != v || p.session == nil || p.descriptor != expected || !p.consumed.CompareAndSwap(false, true) {
		return zero, preparationConflict()
	}
	s := p.session
	if s.base.closed || !s.admitted || len(s.observation) == 0 || len(s.permit) == 0 {
		return zero, ports.ErrMovieUpgradeObservationReview
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.base.conn.Begin(work)
	if err != nil {
		return zero, err
	}
	defer upgradeRollback(work, tx)
	actor, _, verify, err := s.load(work, tx, p.actor)
	if err != nil {
		return zero, err
	}
	var actual ports.MovieUpgradeNativeObservationDescriptor
	actual.OperationID = expected.OperationID
	binding := ports.MovieUpgradeObservationBinding{ObservationID: s.receipt.ObservationID, OperationID: s.receipt.OperationID, TaskID: s.receipt.TaskID, Owner: s.receipt.Owner, Token: s.receipt.Token}
	err = tx.QueryRow(work, `SELECT o.task_id,o.client_id,p.evidence->>'client_digest',o.infohash,o.task_tag,o.save_path,o.total_bytes,encode(sha256(convert_to(to_jsonb(o)::text,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(q)::text,'UTF8')),'hex') FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit q USING(observation_id) JOIN movie_upgrade_preparation p ON p.operation_id=o.operation_id WHERE o.observation_id=$1`, s.receipt.ObservationID).Scan(&actual.TaskID, &actual.ClientID, &actual.ClientDigest, &actual.InfoHash, &actual.OperationTag, &actual.SavePath, &actual.TotalBytes, &binding.ObservationSHA256, &binding.PermitSHA256)
	if err != nil {
		return zero, err
	}
	if actual != expected {
		return zero, preparationConflict()
	}
	if err = s.check(work, tx, actor, verify, ownedUpgradeRows{}, nil); err != nil {
		return zero, err
	}
	if err = tx.Commit(work); err != nil {
		return zero, err
	}
	return binding, nil
}
