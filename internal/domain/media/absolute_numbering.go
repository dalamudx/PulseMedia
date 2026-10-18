package media

import (
	"fmt"
	"github.com/google/uuid"
)

// ResolveAbsoluteNumbering explains coverage only. It never raises the parser's
// identity/quality confidence or authorizes acquisition/file operations.
func ResolveAbsoluteNumbering(parsed EpisodeRecognition, mode string, rows []NumberingRow) ([]NumberingRow, string) {
	if parsed.ParserVersion != EpisodeParserVersion {
		return nil, "parser_version_changed"
	}
	if parsed.MediaType != TypeTV || parsed.Reason != "absolute_mapping_required" || parsed.AbsoluteEpisode < 1 || len(parsed.UnitKeys) > 0 {
		return nil, "absolute_evidence_unresolved"
	}
	if mode != "absolute" && mode != "manual" {
		return nil, "absolute_mapping_required"
	}
	if len(rows) == 0 || len(rows) > 5000 {
		return nil, "incomplete_mapping"
	}
	numbers := map[int]bool{}
	units := map[uuid.UUID]bool{}
	var chosen *NumberingRow
	for _, row := range rows {
		var season, episode int
		_, err := fmt.Sscanf(row.UnitKey, "S%dE%d", &season, &episode)
		if row.SourceNumber < 1 || row.SourceNumber > 9999 || numbers[row.SourceNumber] || row.UnitID == uuid.Nil || units[row.UnitID] || row.UnitVersion < 1 || err != nil || season < 0 || episode < 1 || row.UnitKey != FormatEpisodeUnitKey(season, episode) {
			return nil, "ambiguous_mapping"
		}
		numbers[row.SourceNumber] = true
		units[row.UnitID] = true
		if row.SourceNumber == parsed.AbsoluteEpisode {
			copy := row
			chosen = &copy
		}
	}
	if chosen == nil {
		return nil, "absolute_number_unmapped"
	}
	return []NumberingRow{*chosen}, "mapped_coverage_only"
}
