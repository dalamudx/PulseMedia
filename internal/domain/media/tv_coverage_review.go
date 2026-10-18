package media

import (
	"sort"
	"strings"
)

// ReviewWholeStandardSeason compares explicit title coverage with the entire current
// metadata proof. This establishes no knowledge of torrent files and no execution right.
func ReviewWholeStandardSeason(title string, parsed EpisodeRecognition, mode string, season int, proof, canonical []NumberingUnit, selected map[string]bool) ([]NumberingRow, string) {
	if mode != "standard" || season < 1 || season > 999 || len(proof) < 2 {
		return nil, "complete_normal_season_required"
	}
	for _, token := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
		switch token {
		case "complete", "pack", "ova", "oad", "special", "specials":
			return nil, "uncertain_pack_or_special_coverage"
		}
	}
	rows, reason := ResolveStandardCoverage(parsed, canonical)
	if len(rows) < 2 || len(rows) != len(proof) {
		return nil, "whole_explicit_coverage_required:" + reason
	}
	byKey := map[string]NumberingUnit{}
	provider := map[int64]bool{}
	prefix := FormatEpisodeUnitKey(season, 1)
	prefix = prefix[:strings.Index(prefix, "E")+1]
	active := 0
	for _, u := range canonical {
		if !u.Retired && (u.Season == season || strings.HasPrefix(u.Key, prefix)) {
			active++
		}
	}
	if active != len(proof) {
		return nil, "proof_set_changed"
	}
	for _, u := range proof {
		if u.Season != season || u.Retired || u.Version < 1 || u.EpisodeID < 1 || u.Key != FormatEpisodeUnitKey(season, u.Episode) || u.Episode < 1 || byKey[u.Key].EpisodeID != 0 || provider[u.EpisodeID] {
			return nil, "invalid_proof"
		}
		byKey[u.Key] = u
		provider[u.EpisodeID] = true
	}
	for _, row := range rows {
		u, ok := byKey[row.UnitKey]
		if !ok || !selected[row.UnitKey] || u.ID != row.UnitID || u.Version != row.UnitVersion || u.EpisodeID != row.EpisodeID {
			return nil, "whole_selected_proof_required"
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitKey < rows[j].UnitKey })
	return rows, "reviewed_not_executable"
}
