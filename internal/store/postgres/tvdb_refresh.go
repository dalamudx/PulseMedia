package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	tvdbconfig "github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ ports.TVSupplementRefreshRepository = (*TVRefreshStore)(nil)

func prepareTVDBRefresh(ctx context.Context, q *db.Queries, hint ports.TVRefreshContext, seasons []ports.TVSeasonMetadata, supplements []ports.TVDBSeasonSupplement) (map[int]ports.TVDBSeasonSupplement, map[string]db.ListTMDBEpisodeUnitsRow, error) {
	proofs := map[int]ports.TVDBSeasonSupplement{}
	existing := map[string]db.ListTMDBEpisodeUnitsRow{}
	if len(supplements) == 0 {
		return proofs, existing, nil
	}
	if len(supplements) > len(seasons) || len(supplements) > 5 {
		return nil, nil, ports.ErrInvalidSubscriptionBindings
	}
	seenSourceIDs := map[int64]bool{}
	for _, proof := range supplements {
		season := proof.Metadata.SeasonNumber
		if _, duplicate := proofs[season]; duplicate || season < 0 || season > 999 || proof.TMDBSeriesID != hint.TMDBID || proof.TVDBSeriesID <= 0 || proof.CredentialVersion <= 0 || proof.FallbackVersion <= 0 || proof.Metadata.Complete || len(proof.Added) < 1 || len(proof.Added) > 64 || len(proof.UnmatchedTVDBIDs) > 64 {
			return nil, nil, ports.ErrInvalidSubscriptionBindings
		}
		if proof.TVDBSeriesID != supplements[0].TVDBSeriesID || proof.CredentialVersion != supplements[0].CredentialVersion || proof.FallbackVersion != supplements[0].FallbackVersion {
			return nil, nil, ports.ErrSubscriptionBindingsConflict
		}
		found := false
		for _, observation := range seasons {
			if observation.SeasonNumber == season {
				if found || !sameTVDBSeason(observation, proof.Metadata) {
					return nil, nil, ports.ErrInvalidSubscriptionBindings
				}
				found = true
			}
		}
		if !found {
			return nil, nil, ports.ErrInvalidSubscriptionBindings
		}
		canonical := map[int64]ports.TVEpisodeMetadata{}
		numbers := map[int]bool{}
		for _, ep := range proof.Metadata.Episodes {
			if ep.TMDBEpisodeID <= 0 || ep.EpisodeNumber < 1 || ep.EpisodeNumber > 9999 || numbers[ep.EpisodeNumber] {
				return nil, nil, ports.ErrInvalidSubscriptionBindings
			}
			if _, exists := canonical[ep.TMDBEpisodeID]; exists {
				return nil, nil, ports.ErrInvalidSubscriptionBindings
			}
			canonical[ep.TMDBEpisodeID], numbers[ep.EpisodeNumber] = ep, true
		}
		seenCanonical := map[int64]bool{}
		for _, entry := range proof.Added {
			if entry.TMDBSeriesID != hint.TMDBID || entry.TVDBEpisodeID <= 0 || seenSourceIDs[entry.TVDBEpisodeID] || entry.SeasonNumber != season || seenCanonical[entry.Episode.TMDBEpisodeID] || entry.Episode.AbsoluteNumber != nil {
				return nil, nil, ports.ErrInvalidSubscriptionBindings
			}
			ep, exists := canonical[entry.Episode.TMDBEpisodeID]
			if !exists || !sameTVDBEpisode(ep, entry.Episode) {
				return nil, nil, ports.ErrInvalidSubscriptionBindings
			}
			seenSourceIDs[entry.TVDBEpisodeID], seenCanonical[entry.Episode.TMDBEpisodeID] = true, true
		}
		for _, id := range proof.UnmatchedTVDBIDs {
			if id <= 0 || seenSourceIDs[id] {
				return nil, nil, ports.ErrInvalidSubscriptionBindings
			}
			seenSourceIDs[id] = true
		}
		proofs[season] = proof
	}
	// The two versioned rows stay locked until the entire refresh ledger commits.
	credential, err := q.GetSettingForUpdate(ctx, tvdbconfig.CredentialsSetting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	if err != nil {
		return nil, nil, err
	}
	fallback, err := q.GetSettingForUpdate(ctx, tvdbconfig.FallbackSetting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	if err != nil {
		return nil, nil, err
	}
	var cipher map[string]string
	var enabled map[string]bool
	if credential.Version != supplements[0].CredentialVersion || fallback.Version != supplements[0].FallbackVersion || json.Unmarshal(credential.Value, &cipher) != nil || len(cipher) != 1 || cipher["ciphertext"] == "" || json.Unmarshal(fallback.Value, &enabled) != nil || len(enabled) != 1 || !enabled["enabled"] {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	proof := supplements[0]
	if _, err = q.BindTVDBSeriesIdentity(ctx, db.BindTVDBSeriesIdentityParams{MediaID: hint.MediaID, TmdbSeriesID: hint.TMDBID, TvdbSeriesID: proof.TVDBSeriesID}); err != nil {
		return nil, nil, err
	}
	binding, err := q.GetTVDBSeriesIdentity(ctx, hint.MediaID)
	if err != nil {
		return nil, nil, err
	}
	if binding.TmdbSeriesID != hint.TMDBID || binding.TvdbSeriesID != proof.TVDBSeriesID {
		return nil, nil, ports.ErrSubscriptionBindingsConflict
	}
	units, err := q.ListTMDBEpisodeUnits(ctx, hint.MediaID)
	if err != nil {
		return nil, nil, err
	}
	for _, unit := range units {
		existing[unit.UnitKey] = unit
	}
	return proofs, existing, nil
}
func persistTVDBEpisodeEvidence(ctx context.Context, q *db.Queries, mediaID, unitID uuid.UUID, proof ports.TVDBSeasonSupplement, ep ports.TVEpisodeMetadata) error {
	for _, entry := range proof.Added {
		if entry.Episode.TMDBEpisodeID != ep.TMDBEpisodeID {
			continue
		}
		if _, err := q.BindTVDBEpisodeEvidence(ctx, db.BindTVDBEpisodeEvidenceParams{UnitID: unitID, MediaID: mediaID, TmdbSeriesID: proof.TMDBSeriesID, TvdbSeriesID: proof.TVDBSeriesID, TmdbEpisodeID: ep.TMDBEpisodeID, TvdbEpisodeID: entry.TVDBEpisodeID, SeasonNumber: int32(proof.Metadata.SeasonNumber), EpisodeNumber: int32(ep.EpisodeNumber), CredentialVersion: proof.CredentialVersion, FallbackVersion: proof.FallbackVersion}); err != nil {
			return err
		}
		stored, err := q.GetTVDBEpisodeEvidence(ctx, unitID)
		if err != nil {
			return err
		}
		if stored.MediaID != mediaID || stored.TmdbSeriesID != proof.TMDBSeriesID || stored.TvdbSeriesID != proof.TVDBSeriesID || stored.TmdbEpisodeID != ep.TMDBEpisodeID || stored.TvdbEpisodeID != entry.TVDBEpisodeID || int(stored.SeasonNumber) != proof.Metadata.SeasonNumber || int(stored.EpisodeNumber) != ep.EpisodeNumber {
			return ports.ErrSubscriptionBindingsConflict
		}
	}
	return nil
}
func sameTVDBSeason(a, b ports.TVSeasonMetadata) bool {
	if a.SeasonNumber != b.SeasonNumber || a.Complete != b.Complete || len(a.Episodes) != len(b.Episodes) {
		return false
	}
	for i, ep := range a.Episodes {
		if !sameTVDBEpisode(ep, b.Episodes[i]) {
			return false
		}
	}
	return true
}
func sameTVDBEpisode(a, b ports.TVEpisodeMetadata) bool {
	if a.TMDBEpisodeID != b.TMDBEpisodeID || a.EpisodeNumber != b.EpisodeNumber || (a.AirDate == nil) != (b.AirDate == nil) || (a.AbsoluteNumber == nil) != (b.AbsoluteNumber == nil) {
		return false
	}
	if a.AirDate != nil && (a.AirDate.Year() < 1 || a.AirDate.Year() > 9999 || a.AirDate.Format(time.DateOnly) != b.AirDate.Format(time.DateOnly)) {
		return false
	}
	return a.AbsoluteNumber == nil || *a.AbsoluteNumber == *b.AbsoluteNumber
}
