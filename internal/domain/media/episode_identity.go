package media

import "strconv"

// HasConflictingTVIdentity only rejects contradictory scoped TMDB markers.
// Matching numbers do not prove the TV work identity or grant execution rights.
func HasConflictingTVIdentity(raw string, seriesID int64) bool {
	for _, m := range tmdbIDRegex.FindAllStringSubmatch(raw, -1) {
		id, e := strconv.ParseInt(m[1], 10, 64)
		if e != nil || seriesID < 1 || id != seriesID {
			return true
		}
	}
	return false
}
