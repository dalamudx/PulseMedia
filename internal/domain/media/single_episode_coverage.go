package media

import "github.com/google/uuid"

// ResolveStandardSingleEpisode matches a clear canonical key, not identity, title,
// quality or permission. Absolute/multi-unit evidence must use its own contract.
func ResolveStandardSingleEpisode(parsed EpisodeRecognition, units []NumberingUnit) ([]NumberingRow, string) {
	if parsed.ParserVersion != EpisodeParserVersion {
		return nil, "parser_version_changed"
	}
	if parsed.MediaType != TypeTV || parsed.RequiresReview || len(parsed.UnitKeys) != 1 || parsed.AbsoluteEpisode != 0 {
		return nil, "single_episode_coverage_unresolved"
	}
	var selected *NumberingUnit
	for i := range units {
		u := units[i]
		if u.Key != parsed.UnitKeys[0] {
			continue
		}
		if selected != nil {
			return nil, "ambiguous_canonical_unit"
		}
		selected = &u
	}
	if selected == nil {
		return nil, "canonical_unit_unknown"
	}
	u := *selected
	if u.Retired {
		return nil, "canonical_unit_retired"
	}
	if u.Version < 1 || u.EpisodeID <= 0 || u.ID == uuid.Nil || u.Season < 0 || u.Episode < 1 || u.Key != FormatEpisodeUnitKey(u.Season, u.Episode) {
		return nil, "canonical_identity_unknown"
	}
	return []NumberingRow{{SourceNumber: u.Episode, EpisodeID: u.EpisodeID, UnitID: u.ID, UnitKey: u.Key, UnitVersion: u.Version}}, "standard_coverage_only"
}
