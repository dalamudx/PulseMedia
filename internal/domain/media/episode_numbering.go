package media

import (
	"github.com/google/uuid"
	"sort"
)

type NumberingUnit struct {
	ID              uuid.UUID
	EpisodeID       int64
	Season, Episode int
	Key             string
	Version         int64
	Retired         bool
}
type NumberingEpisode struct {
	SourceNumber    int
	EpisodeID       int64
	Season, Episode int
}
type NumberingRow struct {
	SourceNumber int       `json:"source_number"`
	EpisodeID    int64     `json:"episode_id"`
	UnitID       uuid.UUID `json:"unit_id"`
	UnitKey      string    `json:"unit_key"`
	UnitVersion  int64     `json:"unit_version"`
}

// Join by immutable provider identity, never by array position, title or cumulative counts.
func BuildEpisodeNumbering(source []NumberingEpisode, units []NumberingUnit) ([]NumberingRow, string) {
	if len(source) == 0 || len(source) > 5000 {
		return nil, "incomplete_source"
	}
	byID := map[int64]NumberingUnit{}
	for _, u := range units {
		if u.EpisodeID <= 0 {
			continue
		}
		if _, ok := byID[u.EpisodeID]; ok {
			return nil, "conflicting_episode_identity"
		}
		byID[u.EpisodeID] = u
	}
	rows := []NumberingRow{}
	numbers := map[int]bool{}
	ids := map[int64]bool{}
	canonicalIDs := map[uuid.UUID]bool{}
	canonicalKeys := map[string]bool{}
	for _, ep := range source {
		if ep.SourceNumber < 1 || ep.SourceNumber > 9999 || numbers[ep.SourceNumber] || ep.EpisodeID < 1 || ids[ep.EpisodeID] {
			return nil, "conflicting_source_numbering"
		}
		numbers[ep.SourceNumber] = true
		ids[ep.EpisodeID] = true
		u, ok := byID[ep.EpisodeID]
		if !ok {
			return nil, "canonical_identity_unknown"
		}
		if u.Retired {
			return nil, "canonical_unit_retired"
		}
		if u.ID == uuid.Nil || canonicalIDs[u.ID] || canonicalKeys[u.Key] || u.Version < 1 || u.Season < 0 || u.Episode < 1 || u.Season != ep.Season || u.Episode != ep.Episode || u.Key != FormatEpisodeUnitKey(u.Season, u.Episode) {
			return nil, "canonical_numbering_conflict"
		}
		canonicalIDs[u.ID] = true
		canonicalKeys[u.Key] = true
		rows = append(rows, NumberingRow{ep.SourceNumber, ep.EpisodeID, u.ID, u.Key, u.Version})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SourceNumber < rows[j].SourceNumber })
	return rows, ""
}
