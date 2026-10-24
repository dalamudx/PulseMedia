package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StartRound is a controlled internal/manual candidate round; automatic runtime
// must use StartScheduledRound so only this round's network evidence is consumed.
type SearchRoundStore struct {
	pool           *pgxpool.Pool
	idgen          ports.IDGen
	queries        *db.Queries
	routingRules   []endpoint.DownloadRouteRule
	routingEnabled bool
}

func NewSearchRoundStore(pool *pgxpool.Pool, ids ports.IDGen) *SearchRoundStore {
	return &SearchRoundStore{pool: pool, idgen: ids, queries: Queries(pool)}
}
func NewSearchRoundStoreWithRouting(pool *pgxpool.Pool, ids ports.IDGen, rules []endpoint.DownloadRouteRule) *SearchRoundStore {
	return &SearchRoundStore{pool: pool, idgen: ids, queries: Queries(pool), routingEnabled: true, routingRules: append([]endpoint.DownloadRouteRule(nil), rules...)}
}
func (c *SearchRoundStore) StartRound(ctx context.Context, spec ports.SearchRoundStart) (*ports.SearchRound, error) {
	wantedID, window, scheduled := spec.WantedID, spec.Window, spec.Scheduled
	wanted, err := c.queries.GetWantedItemByID(ctx, wantedID)
	if err != nil {
		return nil, err
	}
	hint, err := c.queries.GetSubscriptionByID(ctx, wanted.SubscriptionID)
	if err != nil {
		return nil, err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := c.queries.WithTx(tx)
	if _, err = q.LockSearchPolicy(ctx, hint.QualityPolicyID); err != nil {
		return nil, err
	}
	sub, err := q.LockSearchSubscription(ctx, hint.ID)
	if err != nil {
		return nil, err
	}
	if sub.State != "active" || sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	root, err := q.LockAcquisitionRoot(ctx, sub.RootID)
	if err != nil {
		return nil, err
	}
	if _, err = q.LockAcquisitionUnit(ctx, wanted.UnitID); err != nil {
		return nil, err
	}
	wanted, err = q.LockSearchWanted(ctx, wantedID)
	if err != nil {
		return nil, err
	}
	if wanted.IsSkipped {
		return nil, ports.ErrRejected
	}
	unit, err := q.GetSearchUnit(ctx, wanted.UnitID)
	if err != nil {
		return nil, err
	}
	if unit.Retired || unit.UnitKey != "movie" || unit.MediaID != sub.MediaID {
		return nil, ports.ErrRejected
	}
	media, err := q.GetMediaByID(ctx, sub.MediaID)
	if err != nil {
		return nil, err
	}
	if media.MediaType != "movie" {
		return nil, ports.ErrRejected
	}
	if root.Health != "online" || !root.BaselineCompleted {
		return nil, ports.ErrAcquisitionRootNotOnline
	}
	snap := ports.RoundSnapshot{PolicyID: sub.QualityPolicyID, UnitMetadataVersion: unit.MetadataVersion, MediaMetadataVersion: media.MetadataVersion, RootID: root.ID, RootGeneration: root.Generation, RootPath: root.Path, UnitID: unit.ID, MediaID: media.ID, Title: media.Title, Year: media.Year.Int32, Bindings: sub.IndexerIds, Indexers: map[string]int64{}, Identities: map[string]string{}, Scheduled: scheduled}
	if sub.DownloaderID.Valid {
		id := uuid.UUID(sub.DownloaderID.Bytes)
		snap.DownloaderID = &id
	}
	identities, err := q.ListSearchMediaIdentities(ctx, media.ID)
	if err != nil {
		return nil, err
	}
	for _, identity := range identities {
		snap.Identities[identity.Namespace] = identity.ExternalID
	}
	indexers, err := q.ListIndexers(ctx)
	if err != nil {
		return nil, err
	}
	var bound []uuid.UUID
	if len(sub.IndexerIds) > 0 {
		if err = json.Unmarshal(sub.IndexerIds, &bound); err != nil {
			return nil, err
		}
	}
	for _, idx := range indexers {
		if idx.Health != "online" {
			continue
		}
		allowed := len(bound) == 0
		for _, id := range bound {
			if id == idx.ID {
				allowed = true
			}
		}
		if allowed {
			snap.Indexers[idx.ID.String()] = idx.ConfigVersion
		}
	}
	var sharedCandidate uuid.UUID
	if spec.Shared {
		lane, e := q.LockActiveAcquisitionLane(ctx, unit.ID)
		if e != nil || lane.LegacyConflict || lane.SourceStamp == "" {
			return nil, ports.ErrUnavailable
		}
		source, e := q.GetCandidateByID(ctx, lane.CandidateID)
		if e != nil {
			return nil, e
		}
		if len(bound) == 0 || snap.Indexers[source.IndexerID.String()] != source.SourceConfigVersion {
			return nil, ports.ErrUnavailable
		}
		revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision})
		if e != nil {
			return nil, e
		}
		if spec.QualifyShared == nil || !spec.QualifyShared(ports.SearchSharedSource{Candidate: searchCandidateDTO(source), Rules: revision.Rules, Title: media.Title, Year: media.Year.Int32, Identities: snap.Identities}) {
			return nil, ports.ErrUnavailable
		}
		if task, e := q.GetDownloadTaskByOpID(ctx, lane.OperationID); e == nil && (task.State == "unknown" || task.State == "submitting" || task.State == "failed" || task.OrganizationState == "completed" || task.OrganizationState == "review_required") {
			return nil, ports.ErrUnavailable
		} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		snap.Indexers = map[string]int64{source.IndexerID.String(): source.SourceConfigVersion}
		snap.SharedSource = true
		sharedCandidate = source.ID
	}
	if scheduled && len(bound) == 0 {
		return nil, ports.ErrRejected
	}
	observedRSS := time.Unix(0, 0).UTC()
	if scheduled {
		// Freeze receipt visibility under the same source lock as its timestamp.
		// Otherwise an uncommitted ACK can have a time before our consume watermark.
		keys := make([]string, 0, len(snap.Indexers))
		for key := range snap.Indexers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			row, e := q.LockSearchIndexer(ctx, uuid.MustParse(key))
			if e != nil {
				return nil, e
			}
			if row.Health != "online" {
				delete(snap.Indexers, key)
			} else {
				snap.Indexers[key] = row.ConfigVersion
				if row.RssEnabled && row.RssLastSuccessAt.Valid && row.RssLastSuccessAt.Time.After(observedRSS) {
					observedRSS = row.RssLastSuccessAt.Time
				}
			}
		}
		if len(snap.Indexers) == 0 {
			return nil, ports.ErrUnavailable
		}
		if snap.DownloaderID == nil && c.routingEnabled {
			active := make([]string, 0, len(snap.Indexers))
			for _, key := range keys {
				if _, ok := snap.Indexers[key]; ok {
					active = append(active, key)
				}
			}
			routes, e := freezeDownloadRoutes(ctx, q, root.Path, "movie", active, c.routingRules)
			if e != nil {
				return nil, e
			}
			snap.RoutedDownloaders = routes
		} else {
			selected := snap.DownloaderID
			if selected == nil {
				clients, e := q.ListDownloadClients(ctx)
				if e != nil {
					return nil, e
				}
				if len(clients) == 0 {
					return nil, ports.ErrUnavailable
				}
				id := clients[0].ID
				selected = &id
			}
			config, e := q.GetSearchDownloaderSnapshot(ctx, *selected)
			if e != nil {
				return nil, e
			}
			snap.SelectedDownloaderID = selected
			snap.DownloaderConfigDigest = config.ConfigDigest
		}
	} else {
		snap.SelectedDownloaderID = snap.DownloaderID
	}

	if window <= 0 {
		window = 30 * time.Second
	}
	if window > time.Minute {
		window = time.Minute
	}
	var now time.Time
	if !scheduled {
		if spec.ManualNow == nil {
			return nil, ports.ErrBadResponse
		}
		now = spec.ManualNow()
	}
	if scheduled {
		dbNow, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return nil, e
		}
		now = dbNow.Time
	}
	round, err := q.CreateSearchRound(ctx, db.CreateSearchRoundParams{ID: c.idgen.NewUUID(), SubscriptionID: sub.ID, WantedItemID: wanted.ID, SnapshotIntentVersion: sub.IntentVersion, SnapshotPolicyRevision: sub.QualityPolicyRevision, Deadline: pgtype.Timestamptz{Time: now.Add(window), Valid: true}})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(snap)
	round, err = q.FreezeSearchRound(ctx, db.FreezeSearchRoundParams{ID: round.ID, DecisionSnapshot: b})
	if err != nil {
		return nil, err
	}
	if scheduled {
		if e := q.ConsumeWantedRSSObservation(ctx, db.ConsumeWantedRSSObservationParams{ID: wanted.ID, Column2: pgtype.Timestamptz{Time: observedRSS, Valid: true}}); e != nil {
			return nil, e
		}
		keys := make([]string, 0, len(snap.Indexers))
		for key := range snap.Indexers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if !snap.SharedSource {
			for _, key := range keys {
				feed, e := q.GetLatestRSSQuery(ctx, db.GetLatestRSSQueryParams{ID: uuid.MustParse(key), ConfigVersion: snap.Indexers[key]})
				if e == nil {
					if e = q.LinkRoundRSSQuery(ctx, db.LinkRoundRSSQueryParams{RoundID: round.ID, QueryID: feed.ID}); e != nil {
						return nil, e
					}
				} else if !errors.Is(e, pgx.ErrNoRows) {
					return nil, e
				}
			}
			for _, key := range keys {
				idx, _ := uuid.Parse(key)
				if err = q.AddRoundEndpoint(ctx, db.AddRoundEndpointParams{RoundID: round.ID, IndexerID: idx}); err != nil {
					return nil, err
				}
				payload, _ := json.Marshal(map[string]any{"round_id": round.ID, "indexer_id": idx})
				if _, err = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: c.idgen.NewUUID(), Kind: "search_endpoint", DedupeKey: pgtype.Text{String: round.ID.String() + ":" + key, Valid: true}, Payload: payload, MaxAttempts: 6, Priority: 5, NotBefore: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
					return nil, err
				}
			}
		}
	}
	if snap.SharedSource {
		raw, _ := json.Marshal([]uuid.UUID{sharedCandidate})
		if _, err = q.AddCandidateToRound(ctx, db.AddCandidateToRoundParams{ID: round.ID, Column2: raw}); err != nil {
			return nil, err
		}
		if err = q.QueueRoundFinalization(ctx, db.QueueRoundFinalizationParams{ID: c.idgen.NewUUID(), ID_2: round.ID}); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	result := searchRoundDTO(round)
	return &result, nil
}
func (c *SearchRoundStore) AddCandidate(ctx context.Context, roundID, candidateID uuid.UUID) error {
	round, e := c.queries.GetSearchRoundByID(ctx, roundID)
	if e != nil {
		return e
	}
	var snap ports.RoundSnapshot
	if e = json.Unmarshal(round.DecisionSnapshot, &snap); e != nil {
		return e
	}
	if round.State != "running" || snap.Scheduled {
		return ports.ErrRejected
	}
	b, _ := json.Marshal([]uuid.UUID{candidateID})
	_, err := c.queries.AddCandidateToRound(ctx, db.AddCandidateToRoundParams{ID: roundID, Column2: b})
	return err
}
