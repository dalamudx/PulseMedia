package media

import "github.com/google/uuid"

// ResolveApprovedAbsoluteSingle preserves the complete approved mapping and checks its canonical provider identities.
// It explains one target only; publication freshness and execution authority remain separate transaction checks.
func ResolveApprovedAbsoluteSingle(parsed EpisodeRecognition, mode string, rows []NumberingRow, canonical []NumberingUnit) ([]NumberingRow, string) {
	resolved, reason := ResolveAbsoluteNumbering(parsed, mode, rows)
	if len(resolved) != 1 {
		return nil, reason
	}
	units := map[uuid.UUID]NumberingUnit{}
	identities := map[int64]bool{}
	for _, u := range canonical {
		if u.ID == uuid.Nil || u.Version < 1 || units[u.ID].ID != uuid.Nil {
			return nil, "canonical_numbering_conflict"
		}
		units[u.ID] = u
		if u.EpisodeID > 0 {
			if identities[u.EpisodeID] {
				return nil, "canonical_numbering_conflict"
			}
			identities[u.EpisodeID] = true
		}
	}
	for _, row := range rows {
		u, ok := units[row.UnitID]
		if !ok || u.Retired || u.Version != row.UnitVersion || u.Key != row.UnitKey || u.Key != FormatEpisodeUnitKey(u.Season, u.Episode) {
			return nil, "canonical_units_changed"
		}
		if row.EpisodeID <= 0 || u.EpisodeID <= 0 || row.EpisodeID != u.EpisodeID {
			return nil, "canonical_identity_unknown"
		}
	}
	return resolved, "mapped_coverage_only"
}
