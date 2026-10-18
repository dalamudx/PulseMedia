package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type CandidateExplanationStore struct{ pool *pgxpool.Pool }

func NewCandidateExplanationStore(pool *pgxpool.Pool) *CandidateExplanationStore {
	return &CandidateExplanationStore{pool}
}

var _ ports.CandidateExplanationRepository = (*CandidateExplanationStore)(nil)

func (s *CandidateExplanationStore) ReadCandidateExplanation(ctx context.Context, a *ports.Actor, id, wantedID uuid.UUID) (out ports.CandidateExplanationSnapshot, err error) {
	if a == nil {
		return out, ports.ErrUnauthorized
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrUnauthorized) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	snap, e := readSubscriptionStatus(ctx, q, a, id, nil)
	if e != nil {
		return out, e
	}
	report := subscription.DeriveStatus(snap)
	var unitStatus *subscription.UnitStatus
	for i := range report.Items {
		if report.Items[i].ID == wantedID {
			unitStatus = &report.Items[i]
		}
	}
	if unitStatus == nil {
		return out, ports.ErrNotFound
	}
	out = ports.CandidateExplanationSnapshot{SubscriptionID: id, WantedID: wantedID, IntentVersion: snap.IntentVersion, PolicyRevision: snap.PolicyRevision, ObservedAt: snap.ObservedAt, Status: report, UnitStatus: *unitStatus, Items: []ports.CandidateExplanationInput{}}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return out, e
	}
	target, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil {
		return out, e
	}
	identities, e := q.ListSearchMediaIdentities(ctx, target.ID)
	if e != nil {
		return out, e
	}
	matching := map[string]string{}
	for _, identity := range identities {
		matching[identity.Namespace] = identity.ExternalID
	}
	round, e := q.GetLatestCandidateRound(ctx, db.GetLatestCandidateRoundParams{SubscriptionID: id, WantedItemID: wantedID})
	if errors.Is(e, pgx.ErrNoRows) {
		if e = tx.Commit(ctx); e != nil {
			return out, e
		}
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.RoundID = round.ID
	out.RoundState = round.State
	out.RoundReason = round.AbortReason.String
	rows, e := q.ListExplainRoundCandidates(ctx, round.ID)
	if e != nil {
		return out, e
	}
	blockedRows, e := q.ListSubscriptionBlockedCandidates(ctx, id)
	if e != nil {
		return out, e
	}
	blockedIDs := map[uuid.UUID]bool{}
	combined := make([]db.ReleaseCandidate, 0, len(blockedRows)+len(rows))
	for _, candidate := range blockedRows {
		blockedIDs[candidate.ID] = true
		combined = append(combined, candidate)
	}
	for _, candidate := range rows {
		if !blockedIDs[candidate.ID] {
			combined = append(combined, candidate)
		}
	}
	rows = combined
	if len(rows) > 100 {
		out.Limited = true
		rows = rows[:100]
	}
	for _, row := range rows {
		indexer, e := q.GetIndexerByID(ctx, row.IndexerID)
		if e != nil {
			return out, e
		}
		timely, e := q.HasCandidateRoundTimelyEvidence(ctx, db.HasCandidateRoundTimelyEvidenceParams{ID: round.ID, Column2: row.ID.String()})
		if e != nil {
			return out, e
		}
		unit, e := q.GetSearchUnit(ctx, unitStatus.UnitID)
		if e != nil {
			return out, e
		}
		rule := media.CandidateRuleInput{MediaID: target.ID.String(), UnitID: unit.ID.String(), MediaVersion: target.MetadataVersion, UnitVersion: unit.MetadataVersion, RawProfile: string(row.QualityProfile), Scope: sub.ScopeType + ":" + string(sub.Seasons), CandidateID: row.ID.String(), PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, Title: row.Title, TargetTitle: target.Title, Year: int(target.Year.Int32), Identities: matching, Profile: media.Recognize(row.Title).Quality, Rules: snap.Rules, SizeBytes: row.SizeBytes, HasReference: len(row.LinkEncrypted) > 0, SourceVersion: row.SourceConfigVersion}
		out.Items = append(out.Items, ports.CandidateExplanationInput{ID: row.ID, IndexerID: row.IndexerID, Rule: rule, Blocked: blockedIDs[row.ID], SourceCurrent: timely && row.SourceConfigVersion > 0 && row.SourceConfigVersion == indexer.ConfigVersion && indexer.Health == "online" && round.SnapshotIntentVersion == sub.IntentVersion && round.SnapshotPolicyRevision == sub.QualityPolicyRevision, ObservedAt: time.Time{}})
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	return out, nil
}
func (s *CandidateExplanationStore) ReadCandidateRuleCache(ctx context.Context, key string) (*media.CandidateRuleDecision, error) {
	raw, e := db.New(s.pool).GetCandidateRuleCache(ctx, key)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var out media.CandidateRuleDecision
	if json.Unmarshal(raw, &out) != nil || (out.Decision != "accept" && out.Decision != "reject" && out.Decision != "hold") || media.UnsafeCandidateTitle(out.Reason) {
		return nil, ports.ErrUnavailable
	}
	return &out, nil
}
func (s *CandidateExplanationStore) WriteCandidateRuleCache(ctx context.Context, key string, value media.CandidateRuleDecision) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return ports.ErrUnavailable
	}
	if e = db.New(s.pool).PutCandidateRuleCache(ctx, db.PutCandidateRuleCacheParams{InputDigest: key, Decision: raw}); e != nil {
		return ports.ErrUnavailable
	}
	return nil
}
