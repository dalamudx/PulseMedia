package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// persistCompleteSeasonMetadata runs inside the same transaction as the complete refresh.
// Historical rows and range/count summaries are never used to mint completeness evidence.
func persistCompleteSeasonMetadata(ctx context.Context, q *db.Queries, input ports.TVRefreshContext, version int64, seasons []ports.TVSeasonMetadata) error {
	units, err := q.ListTMDBEpisodeUnits(ctx, input.MediaID)
	if err != nil {
		return err
	}
	for _, season := range seasons {
		if !season.Complete || season.SeasonNumber < 1 || season.SeasonNumber > 999 || len(season.Episodes) < 1 || len(season.Episodes) > 1000 {
			continue
		}
		expected := make(map[int]int64, len(season.Episodes))
		valid := true
		for _, episode := range season.Episodes {
			if episode.TMDBEpisodeID < 1 || episode.EpisodeNumber < 1 || episode.EpisodeNumber > 9999 || expected[episode.EpisodeNumber] != 0 {
				valid = false
			}
			expected[episode.EpisodeNumber] = episode.TMDBEpisodeID
		}
		frozen := make([]ports.SeasonMetadataUnit, 0, len(expected))
		seen := make(map[int]bool, len(expected))
		for _, unit := range units {
			if unit.Retired {
				continue
			}
			if !unit.SeasonNumber.Valid || int(unit.SeasonNumber.Int32) != season.SeasonNumber {
				if strings.HasPrefix(unit.UnitKey, fmt.Sprintf("S%02dE", season.SeasonNumber)) {
					valid = false
				}
				continue
			}
			number := int(unit.EpisodeNumber.Int32)
			if !unit.EpisodeNumber.Valid || expected[number] == 0 || !unit.TmdbSeriesID.Valid || unit.TmdbSeriesID.Int64 != input.TMDBID || !unit.TmdbEpisodeID.Valid || unit.TmdbEpisodeID.Int64 != expected[number] || seen[number] || unit.ID == uuid.Nil || unit.MetadataVersion < 1 || unit.UnitKey != media.FormatEpisodeUnitKey(season.SeasonNumber, number) {
				valid = false
			}
			seen[number] = true
			frozen = append(frozen, ports.SeasonMetadataUnit{UnitID: unit.ID, UnitVersion: unit.MetadataVersion, TMDBEpisodeID: unit.TmdbEpisodeID.Int64, EpisodeNumber: number})
		}
		if !valid || len(frozen) != len(expected) {
			continue
		}
		sort.Slice(frozen, func(i, j int) bool { return frozen[i].EpisodeNumber < frozen[j].EpisodeNumber })
		raw, err := json.Marshal(frozen)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		if err = q.CreateTVSeasonMetadataSnapshot(ctx, db.CreateTVSeasonMetadataSnapshotParams{
			MediaID: input.MediaID, MediaVersion: version, SeasonNumber: int32(season.SeasonNumber), SourceTmdbID: input.TMDBID,
			Units: raw, ContentHash: hex.EncodeToString(digest[:]),
		}); err != nil {
			return err
		}
	}
	return nil
}

func readCompleteSeasonMetadata(ctx context.Context, q *db.Queries, source *ports.EpisodeSourceContext) error {
	rows, err := q.ListTVSeasonMetadataSnapshots(ctx, db.ListTVSeasonMetadataSnapshotsParams{MediaID: source.MediaID, MediaVersion: source.MediaVersion})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if proof := currentCompleteSeasonMetadata(row, source); proof != nil {
			source.CompleteSeasons = append(source.CompleteSeasons, *proof)
		}
	}
	return nil
}

func currentCompleteSeasonMetadata(row db.TvSeasonMetadataSnapshot, source *ports.EpisodeSourceContext) *ports.CompleteSeasonMetadata {
	season := int(row.SeasonNumber)
	if row.MediaID != source.MediaID || row.MediaVersion != source.MediaVersion || row.SourceTmdbID != source.TMDBID || season < 1 || season > 999 {
		return nil
	}
	var frozen []ports.SeasonMetadataUnit
	if json.Unmarshal(row.Units, &frozen) != nil || len(frozen) < 1 || len(frozen) > 1000 {
		return nil
	}
	// JSONB normalizes object serialization; hash the typed canonical representation.
	raw, err := json.Marshal(frozen)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != row.ContentHash {
		return nil
	}
	active := make(map[uuid.UUID]ports.EpisodeSourceUnit, len(frozen))
	for _, unit := range source.Units {
		if !unit.Retired && unit.Season != season && strings.HasPrefix(unit.Key, fmt.Sprintf("S%02dE", season)) {
			return nil
		}
		if !unit.Retired && unit.Season == season {
			if _, duplicate := active[unit.ID]; duplicate {
				return nil
			}
			active[unit.ID] = unit
		}
	}
	if len(active) != len(frozen) {
		return nil
	}
	previous := 0
	for _, unit := range frozen {
		current, exists := active[unit.UnitID]
		if !exists || unit.UnitID == uuid.Nil || unit.UnitVersion < 1 || unit.TMDBEpisodeID < 1 || current.EpisodeID != unit.TMDBEpisodeID || unit.EpisodeNumber <= previous || unit.EpisodeNumber > 9999 || current.Version != unit.UnitVersion || current.Episode != unit.EpisodeNumber || current.Key != media.FormatEpisodeUnitKey(season, unit.EpisodeNumber) {
			return nil
		}
		previous = unit.EpisodeNumber
	}
	return &ports.CompleteSeasonMetadata{Season: season, SourceTMDBID: row.SourceTmdbID, MediaVersion: row.MediaVersion, ContentHash: row.ContentHash, Units: frozen}
}
