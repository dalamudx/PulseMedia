package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type WantedProjectionStore struct{ pool *pgxpool.Pool }

var _ ports.WantedProjectionRepository = (*WantedProjectionStore)(nil)

func NewWantedProjectionStore(pool *pgxpool.Pool) *WantedProjectionStore {
	return &WantedProjectionStore{pool}
}
func (s *WantedProjectionStore) ProjectWanted(ctx context.Context, id uuid.UUID, policyID string, revision int64) (report *ports.WantedProjection, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrWantedProjectionChanged) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if errors.Is(err, pgx.ErrNoRows) {
				err = ports.ErrNotFound
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	q := db.New(s.pool)
	hint, e := q.GetWantedItemByID(ctx, id)
	if e != nil {
		return nil, e
	}
	before, e := q.GetSubscriptionByID(ctx, hint.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if before.QualityPolicyID != policyID || before.QualityPolicyRevision != revision {
		return nil, ports.ErrWantedProjectionChanged
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q = q.WithTx(tx)
	if _, e = q.LockSearchPolicy(ctx, policyID); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, before.ID)
	if e != nil {
		return nil, e
	}
	if sub.IntentVersion != before.IntentVersion || sub.QualityPolicyID != policyID || sub.QualityPolicyRevision != revision || sub.RootID != before.RootID {
		return nil, ports.ErrWantedProjectionChanged
	}
	if _, e = q.LockAcquisitionRoot(ctx, sub.RootID); e != nil {
		return nil, e
	}
	unit, e := q.LockAcquisitionUnit(ctx, hint.UnitID)
	if e != nil {
		return nil, e
	}
	if unit.MediaID != sub.MediaID {
		return nil, ports.ErrWantedProjectionChanged
	}
	wanted, e := q.LockWantedProjection(ctx, id)
	if e != nil {
		return nil, e
	}
	if wanted.UnitID != hint.UnitID || wanted.SubscriptionID != sub.ID {
		return nil, ports.ErrWantedProjectionChanged
	}
	// A live explicit SEND or bounded preparation lease owns this Wanted observation.
	// automatic projection writes; never mark stale inputs current or change
	// explicit Wanted/policy mutations. The subscription lock serializes this
	// admission with command creation and native preparation.
	installed, e := q.HasMovieUpgradeSendProjectionSchema(ctx)
	if e != nil {
		return nil, e
	}
	if installed {
		deferred, checkErr := q.IsMovieUpgradeSendProjectionDeferred(ctx, id)
		if checkErr != nil {
			return nil, checkErr
		}
		if deferred {
			return nil, ports.ErrWantedProjectionChanged
		}
	}
	actor := &ports.Actor{Type: ports.ActorTypeSystem, ID: "wanted-projection", Role: ports.RoleAdmin}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	snap, e := readSubscriptionStatus(ctx, q, actor, sub.ID, &now.Time)
	if e != nil {
		return nil, e
	}
	result := domain.DeriveProjection(snap)
	for i, value := range result.Items {
		if value.ID == id {
			next := snap.ObservedAt.Add(5 * time.Minute)
			// Reprojection is not a new attempt: keep the persisted retry boundary, do not slide it.
			if value.WaitReason == "no_qualified_candidate" {
				next = snap.ObservedAt
				if wanted.NextEvaluateAt.Valid && wanted.NextEvaluateAt.Time.After(snap.ObservedAt) {
					next = wanted.NextEvaluateAt.Time
				}
			}
			if value.WaitReason == "missing" || value.WaitReason == "file_rejected" {
				next = snap.ObservedAt
			}
			if value.WaitReason == "future_air_date" && snap.Units[i].AirDate != nil {
				next = *snap.Units[i].AirDate
			}
			if value.WaitReason == "search_round_pending" || value.WaitReason == "round_finalization_pending" {
				next = snap.ObservedAt.Add(30 * time.Second)
			}
			var stage pgtype.Text
			var pointer *string
			if value.CurrentFactStage != "" {
				text := value.CurrentFactStage
				stage = pgtype.Text{String: text, Valid: true}
				pointer = &text
			}
			until, e := q.GetWantedProjectionHorizon(ctx, db.GetWantedProjectionHorizonParams{ID: id, ObservedAt: now})
			if e != nil {
				return nil, e
			}
			if until.Valid && until.Time.Before(next) {
				next = until.Time
			}
			e = q.WriteWantedProjection(ctx, db.WriteWantedProjectionParams{ID: id, IntentVersion: sub.IntentVersion, CurrentFactStage: stage, IsSatisfied: value.IsSatisfied, NextEvaluateAt: pgtype.Timestamptz{Time: next, Valid: true}, EvaluatedAt: now, PolicyID: pgtype.Text{String: policyID, Valid: true}, PolicyRevision: pgtype.Int8{Int64: snap.PolicyRevision, Valid: true}, UnitID: pgtype.UUID{Bytes: value.UnitID, Valid: true}, MediaID: pgtype.UUID{Bytes: sub.MediaID, Valid: true}, RootID: pgtype.UUID{Bytes: sub.RootID, Valid: true}, RootGeneration: pgtype.Int8{Int64: snap.RootGeneration, Valid: true}, HealthRevision: pgtype.Int8{Int64: snap.RootHealthRevision, Valid: true}, MediaMetadataVersion: pgtype.Int8{Int64: snap.MediaMetadataVersion, Valid: true}, UnitMetadataVersion: pgtype.Int8{Int64: snap.Units[i].MetadataVersion, Valid: true}, FactsVersion: pgtype.Int8{Int64: value.FactsVersion, Valid: true}, State: pgtype.Text{String: snap.State, Valid: true}, ValidUntil: until, ContractVersion: quality.OwnershipContractVersion})
			if e != nil {
				return nil, e
			}
			if e = tx.Commit(ctx); e != nil {
				return nil, e
			}
			score := quality.AssessOwnership(snap.Rules, snap.Units[i].Files).Score
			return &ports.WantedProjection{WantedItemID: id, UnitID: value.UnitID, IsSatisfied: value.IsSatisfied, FactStage: pointer, NextEvaluateAt: &next, WaitReason: value.WaitReason, Score: score}, nil
		}
	}
	return nil, errors.New("wanted not present in locked projection inputs")
}
