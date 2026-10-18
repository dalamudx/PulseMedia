package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type SubscriptionStatusStore struct{ pool *pgxpool.Pool }

var _ ports.SubscriptionStatusRepository = (*SubscriptionStatusStore)(nil)

func NewSubscriptionStatusStore(pool *pgxpool.Pool) *SubscriptionStatusStore {
	return &SubscriptionStatusStore{pool}
}
func (s *SubscriptionStatusStore) ReadSubscriptionStatus(ctx context.Context, a *ports.Actor, id uuid.UUID) (snap domain.Snapshot, err error) {
	if a == nil {
		return snap, ports.ErrUnauthorized
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return snap, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	snap, e = readSubscriptionStatus(ctx, q, a, id, nil)
	if e != nil {
		return snap, e
	}
	if e = tx.Commit(ctx); e != nil {
		return snap, e
	}
	return snap, nil
}

// This helper never begins/commits or exposes its sqlc transaction to usecases.
func readSubscriptionStatus(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID, evaluationTime *time.Time) (snap domain.Snapshot, err error) {
	sub, e := q.GetSubscriptionByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return snap, ports.ErrNotFound
	}
	if e != nil {
		return snap, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return snap, ports.ErrForbidden
	}
	root, e := q.GetLibraryRootByID(ctx, sub.RootID)
	if e != nil {
		return snap, e
	}
	media, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil {
		return snap, e
	}
	observed, e := q.GetSubscriptionStatusTime(ctx)
	if e != nil {
		return snap, e
	}
	if evaluationTime != nil {
		observed.Time = *evaluationTime
	}
	snap = domain.Snapshot{ID: id, State: sub.State, MediaType: media.MediaType, IntentVersion: sub.IntentVersion, PolicyRevision: sub.QualityPolicyRevision, RootGeneration: root.Generation, RootHealthRevision: root.HealthRevision, MediaMetadataVersion: media.MetadataVersion, ObservedAt: observed.Time, RootHealth: root.Health, BaselineCompleted: root.BaselineCompleted}
	rules, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision})
	if e == nil {
		if json.Unmarshal(rules.Rules, &snap.Rules) != nil {
			return snap, ports.ErrUnavailable
		}
		snap.PolicyAvailable = true
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return snap, e
	}
	var ids []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &ids) != nil {
		return snap, ports.ErrUnavailable
	}
	snap.BoundIndexers = len(ids)
	var soon *time.Time
	for _, idxID := range ids {
		idx, e := q.GetIndexerByID(ctx, idxID)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return snap, e
		}
		if idx.Health != "online" {
			continue
		}
		snap.AvailableIndexers++
		next := idx.NextQueryAt.Time
		if idx.RateLimitUntil.Valid && idx.RateLimitUntil.Time.After(next) {
			next = idx.RateLimitUntil.Time
		}
		if !next.After(snap.ObservedAt) {
			snap.EndpointsReady = true
		} else if soon == nil || next.Before(*soon) {
			value := next
			soon = &value
		}
	}
	if !snap.EndpointsReady {
		snap.EndpointRetryAt = soon
	}
	snap.DownloaderAvailable, e = q.HasSubscriptionStatusDownloader(ctx, sub.DownloaderID)
	if e != nil {
		return snap, e
	}
	units, e := q.ListSubscriptionStatusUnits(ctx, id)
	if e != nil {
		return snap, e
	}
	positions := map[uuid.UUID]int{}
	for _, u := range units {
		value := domain.UnitInput{ID: u.ID, UnitID: u.UnitID, Key: u.UnitKey, Skipped: u.IsSkipped, Retired: u.Retired, FactsVersion: u.FactsVersion, MetadataVersion: u.MetadataVersion}
		if u.AirDate.Valid {
			air := u.AirDate.Time
			value.AirDate = &air
		}
		positions[u.ID] = len(snap.Units)
		snap.Units = append(snap.Units, value)
	}
	files, e := q.ListSubscriptionStatusFiles(ctx, db.ListSubscriptionStatusFilesParams{SubscriptionID: id, ObservedAt: observed})
	if e != nil {
		return snap, e
	}
	for _, f := range files {
		pos, ok := positions[f.WantedID]
		if !ok {
			return snap, ports.ErrUnavailable
		}
		var profile quality.Profile
		if len(f.QualityProfile) > 0 {
			if json.Unmarshal(f.QualityProfile, &profile) != nil {
				profile = quality.Profile{}
			}
		}
		snap.Units[pos].Files = append(snap.Units[pos].Files, domain.FileEvidence{Title: f.RelPath, Profile: profile, Verified: f.Stage.Valid})
	}
	reservations, e := q.ListSubscriptionStatusReservations(ctx, id)
	if e != nil {
		return snap, e
	}
	// Existing standalone status/projection callers may use a pre-reuse schema.
	// Only the installed extension contributes local reservations; query errors still fail.
	reuseInstalled, e := q.HasSubscriptionStatusLocalReuseSchema(ctx)
	if e != nil {
		return snap, e
	}
	if reuseInstalled {
		localReservations, err := q.ListSubscriptionStatusLocalReuse(ctx, id)
		if err != nil {
			return snap, err
		}
		for _, local := range localReservations {
			reservations = append(reservations, db.ListSubscriptionStatusReservationsRow(local))
		}
	}
	for _, r := range reservations {
		pos := positions[r.WantedID]
		snap.Units[pos].Reserved = true
		if r.TaskState == "global_available" || r.TaskState == "global_unknown" {
			snap.Units[pos].SharedAcquisition = true
		}
		snap.Units[pos].ReservationUnknown = snap.Units[pos].ReservationUnknown || r.TaskState == "unknown" || r.TaskState == "submitting" || r.TaskState == "failed" || r.TaskState == "canceled" || r.TaskState == "global_unknown"
	}
	facts, e := q.ListSubscriptionStatusTaskFacts(ctx, db.ListSubscriptionStatusTaskFactsParams{SubscriptionID: id, ObservedAt: observed})
	if e != nil {
		return snap, e
	}
	for _, f := range facts {
		pos := positions[f.WantedID]
		if snap.Units[pos].FactStage == "" || f.Stage == "downloaded" {
			snap.Units[pos].FactStage = f.Stage
		}
	}
	rounds, e := q.ListSubscriptionStatusRounds(ctx, id)
	if e != nil {
		return snap, e
	}
	for _, r := range rounds {
		pos, ok := positions[r.WantedItemID]
		if !ok {
			continue
		}
		snap.Units[pos].Round = &domain.RoundEvidence{State: r.State, Reason: r.AbortReason.String, IntentVersion: r.SnapshotIntentVersion, PolicyRevision: r.SnapshotPolicyRevision, Deadline: r.Deadline.Time}
	}
	return snap, nil
}
