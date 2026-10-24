package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strconv"
)

type TVRefreshStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewTVRefreshStore(pool *pgxpool.Pool, ids ports.IDGen) *TVRefreshStore {
	return &TVRefreshStore{pool, ids}
}
func readTVRefresh(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (ports.TVRefreshContext, error) {
	var out ports.TVRefreshContext
	if a == nil {
		return out, ports.ErrUnauthorized
	}
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return out, e
	}
	if !a.IsAdmin() {
		return out, ports.ErrForbidden
	}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if e != nil {
		return out, ports.ErrUnavailable
	}
	m, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil || m.MediaType != "tv" {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return out, ports.ErrUnavailable
	}
	var tmdb int64
	for _, identity := range identities {
		if identity.Namespace == "tmdb.tv" {
			tmdb, e = strconv.ParseInt(identity.ExternalID, 10, 64)
			if e != nil {
				return out, ports.ErrInvalidSubscriptionBindings
			}
		}
	}
	if tmdb <= 0 {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	return ports.TVRefreshContext{SubscriptionID: id, MediaID: m.ID, TMDBID: tmdb, MediaVersion: m.MetadataVersion, IntentVersion: sub.IntentVersion}, nil
}
func cachedTVReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID, key, hash string) (*ports.TVRefreshReceipt, error) {
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var out ports.TVRefreshReceipt
	if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &out) != nil || out.SubscriptionID != id {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	return &out, nil
}
func (s *TVRefreshStore) ReadTVRefreshContext(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.TVRefreshCommand, key, hash string) (ports.TVRefreshContext, *ports.TVRefreshReceipt, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return ports.TVRefreshContext{}, nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return ports.TVRefreshContext{}, nil, e
	}
	input, e := readTVRefresh(ctx, q, a, id)
	if e != nil {
		return input, nil, e
	}
	receipt, e := cachedTVReceipt(ctx, q, a, id, key, hash)
	if e != nil || receipt != nil {
		return input, receipt, e
	}
	if input.IntentVersion != c.ExpectedVersion {
		return input, nil, ports.ErrSubscriptionBindingsConflict
	}
	return input, nil, nil
}
func (s *TVRefreshStore) ApplyTVRefresh(ctx context.Context, a *ports.Actor, hint ports.TVRefreshContext, c ports.TVRefreshCommand, seasons []ports.TVSeasonMetadata, key, hash string) (*ports.TVRefreshReceipt, error) {
	return s.applyTVRefresh(ctx, a, hint, c, seasons, nil, key, hash)
}
func (s *TVRefreshStore) ApplyTVRefreshWithSupplements(ctx context.Context, a *ports.Actor, hint ports.TVRefreshContext, c ports.TVRefreshCommand, seasons []ports.TVSeasonMetadata, supplements []ports.TVDBSeasonSupplement, key, hash string) (*ports.TVRefreshReceipt, error) {
	return s.applyTVRefresh(ctx, a, hint, c, seasons, supplements, key, hash)
}
func (s *TVRefreshStore) applyTVRefresh(ctx context.Context, a *ports.Actor, hint ports.TVRefreshContext, c ports.TVRefreshCommand, seasons []ports.TVSeasonMetadata, supplements []ports.TVDBSeasonSupplement, key, hash string) (out *ports.TVRefreshReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrSubscriptionBindingsConflict) && !errors.Is(err, ports.ErrInvalidSubscriptionBindings) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "tv.metadata.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	subs, e := q.ListTVMediaSubscriptions(ctx, hint.MediaID)
	if e != nil {
		return nil, e
	}
	policies := map[string]bool{}
	roots := map[uuid.UUID]bool{}
	for _, sub := range subs {
		policies[sub.QualityPolicyID] = true
		roots[sub.RootID] = true
	}
	policyKeys := []string{}
	for id := range policies {
		policyKeys = append(policyKeys, id)
	}
	sort.Strings(policyKeys)
	for _, id := range policyKeys {
		if _, e = q.LockSearchPolicy(ctx, id); e != nil {
			return nil, e
		}
	}
	for i := range subs {
		locked, e := q.LockSearchSubscription(ctx, subs[i].ID)
		if e != nil {
			return nil, e
		}
		if locked.QualityPolicyID != subs[i].QualityPolicyID || locked.RootID != subs[i].RootID {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		subs[i] = locked
	}
	rootKeys := []uuid.UUID{}
	for id := range roots {
		rootKeys = append(rootKeys, id)
	}
	sort.Slice(rootKeys, func(i, j int) bool { return rootKeys[i].String() < rootKeys[j].String() })
	for _, id := range rootKeys {
		if _, e = q.LockAcquisitionRoot(ctx, id); e != nil {
			return nil, e
		}
	}
	if _, e = q.LockScopeMediaUnits(ctx, hint.MediaID); e != nil {
		return nil, e
	}
	m, e := q.LockSubmissionMedia(ctx, hint.MediaID)
	if e != nil {
		return nil, e
	}
	currentSubs, e := q.ListTVMediaSubscriptions(ctx, hint.MediaID)
	if e != nil {
		return nil, e
	}
	if len(currentSubs) != len(subs) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	for i := range currentSubs {
		if currentSubs[i].ID != subs[i].ID || currentSubs[i].IntentVersion != subs[i].IntentVersion {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
	}
	input, e := readTVRefresh(ctx, q, a, hint.SubscriptionID)
	if e != nil {
		return nil, e
	}
	receipt, e := cachedTVReceipt(ctx, q, a, hint.SubscriptionID, key, hash)
	if e != nil || receipt != nil {
		return receipt, e
	}
	if input.MediaID != hint.MediaID || m.MetadataVersion != hint.MediaVersion || input.TMDBID != hint.TMDBID || input.IntentVersion != c.ExpectedVersion {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	proofs, existingUnits, e := prepareTVDBRefresh(ctx, q, hint, seasons, supplements)
	if e != nil {
		return nil, e
	}
	count := 0
	if len(seasons) != len(c.Seasons) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	seenSeasons := map[int]bool{}
	expectedSeasons := map[int]bool{}
	for _, n := range c.Seasons {
		expectedSeasons[n] = true
	}
	seenIDs := map[int64]bool{}
	for _, season := range seasons {
		if !expectedSeasons[season.SeasonNumber] || seenSeasons[season.SeasonNumber] {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		seenSeasons[season.SeasonNumber] = true
		numbers := []int32{}
		seen := map[int]bool{}
		proof, supplemented := proofs[season.SeasonNumber]
		if (!season.Complete && !supplemented) || (season.Complete && supplemented) || len(season.Episodes) == 0 {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		for _, episode := range season.Episodes {
			if episode.TMDBEpisodeID < 0 || (episode.TMDBEpisodeID > 0 && seenIDs[episode.TMDBEpisodeID]) {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
			if episode.TMDBEpisodeID > 0 {
				seenIDs[episode.TMDBEpisodeID] = true
			}
			if episode.EpisodeNumber < 1 || seen[episode.EpisodeNumber] {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
			seen[episode.EpisodeNumber] = true
			numbers = append(numbers, int32(episode.EpisodeNumber))
			date := pgtype.Date{}
			if episode.AirDate != nil {
				date = pgtype.Date{Time: *episode.AirDate, Valid: true}
			}
			absolute := pgtype.Int4{}
			if episode.AbsoluteNumber != nil {
				if *episode.AbsoluteNumber < 1 {
					return nil, ports.ErrInvalidSubscriptionBindings
				}
				absolute = pgtype.Int4{Int32: int32(*episode.AbsoluteNumber), Valid: true}
			}
			unitID := s.ids.NewUUID()
			unitKey := media.FormatEpisodeUnitKey(season.SeasonNumber, episode.EpisodeNumber)
			if existing, ok := existingUnits[unitKey]; supplemented && ok {
				if existing.Retired || !existing.TmdbEpisodeID.Valid || existing.TmdbEpisodeID.Int64 != episode.TMDBEpisodeID || !existing.TmdbSeriesID.Valid || existing.TmdbSeriesID.Int64 != hint.TMDBID || !existing.SeasonNumber.Valid || int(existing.SeasonNumber.Int32) != season.SeasonNumber || !existing.EpisodeNumber.Valid || int(existing.EpisodeNumber.Int32) != episode.EpisodeNumber {
					return nil, ports.ErrSubscriptionBindingsConflict
				}
				unitID = existing.ID
			} else {
				u, applyErr := q.ApplyTVEpisodeMetadata(ctx, db.ApplyTVEpisodeMetadataParams{ID: unitID, MediaID: hint.MediaID, UnitKey: unitKey, SeasonNumber: pgtype.Int4{Int32: int32(season.SeasonNumber), Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(episode.EpisodeNumber), Valid: true}, AbsoluteNumber: absolute, AirDate: date})
				if applyErr != nil {
					return nil, applyErr
				}
				unitID = u.ID
			}
			if episode.TMDBEpisodeID > 0 {
				if _, e = q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: unitID, ExternalID: episode.TMDBEpisodeID, TmdbSeriesID: hint.TMDBID}); e != nil {
					return nil, e
				}
				identity, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unitID)
				if errors.Is(e, pgx.ErrNoRows) {
					return nil, ports.ErrSubscriptionBindingsConflict
				}
				if e != nil {
					return nil, e
				}
				if identity.ExternalID != episode.TMDBEpisodeID || identity.TmdbSeriesID != hint.TMDBID {
					return nil, ports.ErrSubscriptionBindingsConflict
				}
			}
			if supplemented {
				if e = persistTVDBEpisodeEvidence(ctx, q, hint.MediaID, unitID, proof, episode); e != nil {
					return nil, e
				}
			}
			count++
		}
		if !supplemented {
			if e = q.RetireMissingTVSeasonUnits(ctx, db.RetireMissingTVSeasonUnitsParams{MediaID: hint.MediaID, SeasonNumber: pgtype.Int4{Int32: int32(season.SeasonNumber), Valid: true}, Column3: numbers}); e != nil {
				return nil, e
			}
		}
	}
	m, e = q.AdvanceTVMediaMetadataVersion(ctx, hint.MediaID)
	if e != nil {
		return nil, e
	}
	units, e := q.LockScopeMediaUnits(ctx, hint.MediaID)
	if e != nil {
		return nil, e
	}
	if e = persistCompleteSeasonMetadata(ctx, q, input, m.MetadataVersion, seasons); e != nil {
		return nil, e
	}
	for _, sub := range subs {
		var scope domain.Scope
		scope.Type, scope.IncludeSpecials = sub.ScopeType, sub.IncludeSpecials
		if json.Unmarshal(sub.Seasons, &scope.Seasons) != nil || json.Unmarshal(sub.SelectedUnitIds, &scope.Selected) != nil {
			return nil, ports.ErrUnavailable
		}
		for _, u := range units {
			included := domain.ScopeContains(scope, domain.ScopeUnit{ID: u.ID, Season: int(u.SeasonNumber.Int32), TV: true, Retired: u.Retired})
			if _, e = q.ApplyScopeWanted(ctx, db.ApplyScopeWantedParams{ID: s.ids.NewUUID(), SubscriptionID: sub.ID, UnitID: u.ID, IntentVersion: sub.IntentVersion, IsSkipped: !included}); e != nil {
				return nil, e
			}
		}
	}
	out = &ports.TVRefreshReceipt{SubscriptionID: hint.SubscriptionID, MediaID: hint.MediaID, MediaVersion: m.MetadataVersion, ObservedEpisodes: count}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "tv.metadata_refreshed", TargetType: "media", TargetID: m.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "media", AggregateID: m.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "media", AggregateID: m.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "media", AggregateID: m.ID.String(), EventType: "tv.metadata_refreshed", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: m.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
