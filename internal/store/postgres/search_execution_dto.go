package postgres

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

func searchTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}
func searchUUID(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}
func searchText(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
func searchPGTime(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *v, Valid: true}
}
func searchPGText(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}
func searchIndexerDTO(v db.Indexer) ports.SearchIndexer {
	return ports.SearchIndexer{ID: v.ID, Name: v.Name, Url: v.Url, ApiKeyEncrypted: v.ApiKeyEncrypted, Categories: v.Categories, ConfigVersion: v.ConfigVersion, Health: v.Health, RateLimitUntil: searchTime(v.RateLimitUntil), CreatedAt: v.CreatedAt.Time, UpdatedAt: v.UpdatedAt.Time, NextQueryAt: searchTime(v.NextQueryAt), RssEnabled: v.RssEnabled, RssKind: v.RssKind, RssUrlEncrypted: v.RssUrlEncrypted, RssIntervalSeconds: v.RssIntervalSeconds, RssNextPollAt: searchTime(v.RssNextPollAt), RssLastQueryID: searchUUID(v.RssLastQueryID), RssLastSuccessAt: searchTime(v.RssLastSuccessAt), HealthCheckedAt: searchTime(v.HealthCheckedAt), HealthObservedAt: searchTime(v.HealthObservedAt), HealthQueryID: searchUUID(v.HealthQueryID), HealthNextCheckAt: searchTime(v.HealthNextCheckAt), HealthFailures: v.HealthFailures, HealthReason: v.HealthReason}
}
func searchCandidateDTO(v db.ReleaseCandidate) ports.SearchCandidate {
	return ports.SearchCandidate{ID: v.ID, IndexerID: v.IndexerID, Title: v.Title, Guid: v.Guid, LinkEncrypted: v.LinkEncrypted, InfoHash: searchText(v.InfoHash), SizeBytes: v.SizeBytes, Seeders: v.Seeders, Leechers: v.Leechers, PublishDate: searchTime(v.PublishDate), QualityProfile: v.QualityProfile, MatchedMediaID: searchUUID(v.MatchedMediaID), MatchedUnitID: searchUUID(v.MatchedUnitID), CreatedAt: v.CreatedAt.Time, SourceConfigVersion: v.SourceConfigVersion}
}
func searchRoundDTO(v db.SearchRound) ports.SearchRound {
	return ports.SearchRound{ID: v.ID, SubscriptionID: v.SubscriptionID, WantedItemID: v.WantedItemID, SnapshotIntentVersion: v.SnapshotIntentVersion, SnapshotPolicyRevision: v.SnapshotPolicyRevision, State: v.State, Deadline: v.Deadline.Time, CandidateIds: v.CandidateIds, SelectedCandidateID: searchUUID(v.SelectedCandidateID), AbortReason: searchText(v.AbortReason), CreatedAt: v.CreatedAt.Time, FinishedAt: searchTime(v.FinishedAt), DecisionSnapshot: v.DecisionSnapshot, OperationID: searchUUID(v.OperationID)}
}
