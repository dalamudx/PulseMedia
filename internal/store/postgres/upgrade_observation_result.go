package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/jackc/pgx/v5"
	"time"
)

func marshalObservationReceipt(v any) (json.RawMessage, error) { return json.Marshal(v) }
func (s *movieUpgradeObservationSession) RecordMovieUpgradeObservation(ctx context.Context, a *ports.Actor, key string, proof ports.MovieUpgradeObservationProof) (out *ports.MovieUpgradeObservationResult, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !upgradeKey(key) || proof == nil {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e := s.base.acquire(work); e != nil {
		return nil, e
	}
	defer func() { <-s.base.gate }()
	return s.recordMovieUpgradeObservationLocked(work, a, key, proof)
}

// Caller holds the original session gate through physical return and writeback.
func (s *movieUpgradeObservationSession) recordMovieUpgradeObservationLocked(work context.Context, a *ports.Actor, key string, proof ports.MovieUpgradeObservationProof) (out *ports.MovieUpgradeObservationResult, err error) {
	if s.base.closed || !s.admitted || len(s.observation) == 0 || len(s.permit) == 0 {
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	tx, e := s.base.conn.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, verify, e := s.load(work, tx, a)
	if e != nil {
		return nil, e
	}
	var binding ports.MovieUpgradeObservationBinding
	binding.ObservationID = s.receipt.ObservationID
	binding.OperationID = s.base.operation
	binding.TaskID = s.receipt.TaskID
	binding.Owner = s.receipt.Owner
	binding.Token = s.receipt.Token
	e = tx.QueryRow(work, `SELECT encode(sha256(convert_to(to_jsonb(o)::text,'UTF8')),'hex'),encode(sha256(convert_to(to_jsonb(p)::text,'UTF8')),'hex') FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit p USING(observation_id) WHERE o.observation_id=$1`, binding.ObservationID).Scan(&binding.ObservationSHA256, &binding.PermitSHA256)
	if e != nil {
		return nil, e
	}
	v, e := s.verifier.VerifyMovieUpgradeObservation(work, proof, binding)
	if e != nil {
		return nil, preparationConflict()
	}
	if v.Binding != binding || v.ReturnedAt.IsZero() {
		return nil, preparationConflict()
	}
	switch v.Outcome {
	case "exact_match", "absent", "ambiguous", "unknown":
	default:
		return nil, preparationConflict()
	}
	returnedAt := v.ReturnedAt.UTC().Truncate(time.Microsecond)
	receipt := &ports.MovieUpgradeObservationResult{ObservationID: binding.ObservationID, OperationID: binding.OperationID, TaskID: binding.TaskID, Outcome: v.Outcome, Disposition: "manual_review"}
	hash, e := observationRequestHash(struct {
		Binding    ports.MovieUpgradeObservationBinding
		Outcome    string
		ReturnedAt time.Time
	}{binding, v.Outcome, returnedAt})
	if e != nil {
		return nil, e
	}
	var row json.RawMessage
	e = tx.QueryRow(work, "SELECT to_jsonb(r) FROM movie_upgrade_observation_result r WHERE observation_id=$1 FOR SHARE", binding.ObservationID).Scan(&row)
	existing := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	sum, e := hex.DecodeString(binding.ObservationSHA256)
	if e != nil {
		return nil, e
	}
	permitSum, e := hex.DecodeString(binding.PermitSHA256)
	if e != nil {
		return nil, e
	}
	if existing {
		var same bool
		e = tx.QueryRow(work, `SELECT request_key=$2 AND outcome=$3 AND returned_at=$4::timestamptz AND disposition='manual_review' AND observation_sha256=$5 AND permit_sha256=$6 FROM movie_upgrade_observation_result WHERE observation_id=$1`, binding.ObservationID, key, v.Outcome, returnedAt, sum, permitSum).Scan(&same)
		if e != nil {
			return nil, e
		}
		if !same {
			return nil, preparationConflict()
		}
	} else {
		var expected json.RawMessage
		e = tx.QueryRow(work, `SELECT jsonb_build_object('observation_id',$1::uuid,'request_key',$2::text,'outcome',$3::text,'returned_at',$4::timestamptz,'recorded_at',clock_timestamp(),'observation_sha256',$5::bytea,'permit_sha256',$6::bytea,'disposition','manual_review')`, binding.ObservationID, key, v.Outcome, returnedAt, sum, permitSum).Scan(&expected)
		if e != nil {
			return nil, e
		}
		if e = tx.QueryRow(work, `INSERT INTO movie_upgrade_observation_result SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_observation_result,$1::jsonb) RETURNING to_jsonb(movie_upgrade_observation_result)`, expected).Scan(&row); e != nil {
			return nil, e
		}
		if !preparationSameJSON(row, expected) {
			return nil, preparationConflict()
		}
	}
	own, e := s.ledger(work, tx, a, sub, key, hash, "upgrade.observation-result", receipt, existing)
	if e != nil {
		return nil, e
	}
	check := func() error { return s.check(work, tx, a, verify, own, row) }
	if e = check(); e != nil {
		return nil, e
	}
	if s.job != nil {
		if existing {
			return nil, preparationConflict()
		}
		if e = s.job.finish(work, tx, s); e != nil {
			return nil, e
		}
	}
	if !existing {
		writer := &UpgradeReservationStore{ids: s.base.store.ids}
		if e = writer.upgradeSuccess(work, tx, a, sub, "upgrade.observation-result:"+key, hash, "upgrade.observation-result", s.base.operation, receipt, own, check); e != nil {
			return nil, e
		}
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
	return receipt, nil
}

func observationRequestHash(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	return completeTVJSONDigest(raw), nil
}
