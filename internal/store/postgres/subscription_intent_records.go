package postgres

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

func intentInt4(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}
func intentInt8(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
func intentText(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}
func intentBool(v pgtype.Bool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}
func intentTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	timestamp := v.Time.UTC()
	return &timestamp
}
func intentUUID(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}
func subscriptionIntentRecord(v db.Subscription) (ports.SubscriptionRecord, error) {
	out := ports.SubscriptionRecord{ID: v.ID, UserID: v.UserID, MediaID: v.MediaID, RootID: v.RootID, QualityPolicyID: v.QualityPolicyID, QualityPolicyRevision: v.QualityPolicyRevision, DownloaderID: intentUUID(v.DownloaderID), State: v.State, IntentVersion: v.IntentVersion, ScopeType: v.ScopeType, CreatedAt: v.CreatedAt.Time.UTC(), UpdatedAt: v.UpdatedAt.Time.UTC(), IncludeSpecials: v.IncludeSpecials, EpisodeMappingRevision: intentInt8(v.EpisodeMappingRevision), UpgradeMode: v.UpgradeMode}
	if e := json.Unmarshal(v.IndexerIds, &out.IndexerIDs); e != nil {
		return out, e
	}
	if e := json.Unmarshal(v.Seasons, &out.Seasons); e != nil {
		return out, e
	}
	if e := json.Unmarshal(v.SelectedUnitIds, &out.SelectedUnitIDs); e != nil {
		return out, e
	}
	return out, nil
}
func subscriptionIntentWanted(v db.WantedItem) ports.SubscriptionWanted {
	return ports.SubscriptionWanted{
		ID: v.ID, SubscriptionID: v.SubscriptionID, UnitID: v.UnitID, IntentVersion: v.IntentVersion,
		IsSkipped: v.IsSkipped, IsSatisfied: v.IsSatisfied, UserSkipped: v.UserSkipped, ScopeExcluded: v.ScopeExcluded,
		CurrentFactStage: intentText(v.CurrentFactStage), LastEvaluatedAt: intentTime(v.LastEvaluatedAt), NextEvaluateAt: intentTime(v.NextEvaluateAt), RssSeenAt: intentTime(v.RssSeenAt), CreatedAt: v.CreatedAt.Time.UTC(), UpdatedAt: v.UpdatedAt.Time.UTC(),
		EvaluatedIntentVersion: intentInt8(v.EvaluatedIntentVersion), EvaluatedPolicyID: intentText(v.EvaluatedPolicyID), EvaluatedPolicyRevision: intentInt8(v.EvaluatedPolicyRevision), EvaluatedRootID: intentUUID(v.EvaluatedRootID), EvaluatedUnitID: intentUUID(v.EvaluatedUnitID), EvaluatedMediaID: intentUUID(v.EvaluatedMediaID), EvaluatedRootGeneration: intentInt8(v.EvaluatedRootGeneration), EvaluatedHealthRevision: intentInt8(v.EvaluatedHealthRevision), EvaluatedMediaMetadataVersion: intentInt8(v.EvaluatedMediaMetadataVersion), EvaluatedUnitMetadataVersion: intentInt8(v.EvaluatedUnitMetadataVersion), EvaluatedFactsVersion: intentInt8(v.EvaluatedFactsVersion), EvaluatedState: intentText(v.EvaluatedState), EvaluatedSkipped: intentBool(v.EvaluatedSkipped), ProjectionValidUntil: intentTime(v.ProjectionValidUntil), EvaluatedContractVersion: intentText(v.EvaluatedContractVersion), EvaluatedWriteToken: intentUUID(v.EvaluatedWriteToken),
	}
}
