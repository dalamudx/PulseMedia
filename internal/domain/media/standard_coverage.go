package media

import (
	"github.com/google/uuid"
	"math"
)

// ResolveStandardCoverage explains the complete explicit numbered set only.
// It never expands an unknown season pack, proves media identity, or grants execution.
func ResolveStandardCoverage(parsed EpisodeRecognition, units []NumberingUnit) ([]NumberingRow, string) {
	if parsed.ParserVersion != EpisodeParserVersion {
		return nil, "parser_version_changed"
	}
	if parsed.MediaType != TypeTV || parsed.RequiresReview || parsed.AbsoluteEpisode != 0 || len(parsed.UnitKeys) == 0 || len(parsed.UnitKeys) > 1000 || math.IsNaN(parsed.Confidence) || parsed.Confidence < 0.85 || parsed.Confidence > 1 {
		return nil, "standard_coverage_unresolved"
	}
	if len(parsed.UnitKeys) == 1 {
		return ResolveStandardSingleEpisode(parsed, units)
	}
	keys := map[string]bool{}
	ids := map[uuid.UUID]bool{}
	providers := map[int64]bool{}
	rows := make([]NumberingRow, 0, len(parsed.UnitKeys))
	for _, key := range parsed.UnitKeys {
		if keys[key] {
			return nil, "duplicate_episode_coverage"
		}
		keys[key] = true
		one := parsed
		one.UnitKeys = []string{key}
		resolved, reason := ResolveStandardSingleEpisode(one, units)
		if len(resolved) != 1 {
			return nil, reason
		}
		row := resolved[0]
		if ids[row.UnitID] || providers[row.EpisodeID] {
			return nil, "ambiguous_canonical_unit"
		}
		ids[row.UnitID] = true
		providers[row.EpisodeID] = true
		rows = append(rows, row)
	}
	return rows, "standard_multi_coverage_only"
}
