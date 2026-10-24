package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sort"
)

type automaticTVInitialBinding func(context.Context, *db.Queries, db.Subscription, db.LibraryRoot, map[uuid.UUID]ports.TVEpisodeQuery) (db.Subscription, error)

// Called only for a new round, after owner/policy/subscription/root/unit/Wanted/
// indexer locks. The same transaction owns binding, refreshed intents and round.
// Existing execution graphs continue to require sub.downloader_id == client_id.
func (s *AutomaticTVRoundStore) bindInitialTVDownloader(ctx context.Context, q *db.Queries, sub db.Subscription, root db.LibraryRoot, sources map[uuid.UUID]ports.TVEpisodeQuery) (db.Subscription, error) {
	if sub.DownloaderID.Valid {
		return sub, nil
	}
	keys := make([]string, 0, len(sources))
	for id := range sources {
		keys = append(keys, id.String())
	}
	sort.Strings(keys)
	routes, err := freezeDownloadRoutes(ctx, q, root.Path, "tv", keys, s.routingRules)
	if err != nil {
		return sub, err
	}
	selected := uuid.Nil
	for _, key := range keys {
		route := routes[key]
		if route.DownloaderID == uuid.Nil || (selected != uuid.Nil && selected != route.DownloaderID) {
			return sub, ports.ErrUnavailable
		}
		selected = route.DownloaderID
	}
	if selected == uuid.Nil {
		return sub, ports.ErrUnavailable
	}
	beforeVersion := sub.IntentVersion
	sub, err = q.ConfigureSubscriptionBindings(ctx, db.ConfigureSubscriptionBindingsParams{ID: sub.ID, IndexerIds: sub.IndexerIds, DownloaderID: pgtype.UUID{Bytes: selected, Valid: true}, PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision})
	if err != nil {
		return sub, err
	}
	if err = q.RefreshBindingWantedIntents(ctx, db.RefreshBindingWantedIntentsParams{SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion}); err != nil {
		return sub, err
	}
	safe, err := json.Marshal(map[string]any{"owner_id": sub.UserID, "from_intent_version": beforeVersion, "intent_version": sub.IntentVersion, "downloader_id": selected, "policy_version": endpoint.DownloadRoutePolicyVersion, "sources": routes})
	if err != nil {
		return sub, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: sub.UserID.String(), Action: "subscription.downloader_routed", TargetType: "subscription", TargetID: sub.ID.String(), Payload: safe}); err != nil {
		return sub, err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: sub.ID.String()}); err != nil {
		return sub, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: sub.ID.String()})
	if err != nil {
		return sub, err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "subscription", AggregateID: sub.ID.String(), EventType: "subscription.downloader_routed", Payload: safe, Seq: int64(seq)}); err != nil {
		return sub, err
	}
	return sub, nil
}
