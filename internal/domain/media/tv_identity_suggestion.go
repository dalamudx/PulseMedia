package media

import "strings"

const TVIdentitySuggestionVersion = "tv-identity-suggestion-v2"

// A suggestion is review evidence only, never provider identity or execution permission.
func SuggestTVIdentity(raw, title, original string, year int, seriesID int64) (string, string) {
	if UnsafeCandidateTitle(raw) || strings.ContainsAny(raw, "/\\") {
		return "hold", "unsafe_title"
	}
	if HasConflictingTVIdentity(raw, seriesID) {
		return "reject", "media_identity_conflict"
	}
	if len(imdbIDRegex.FindAllStringSubmatch(raw, -1)) > 0 {
		return "hold", "external_identity_unverified"
	}
	parsed := RecognizeRelease(raw)
	if parsed.MediaType != TypeTV || parsed.RequiresReview || len(parsed.UnitKeys) != 1 || parsed.AbsoluteEpisode != 0 {
		return "hold", "identity_coverage_unresolved"
	}
	if parsed.Year > 0 && year > 0 && parsed.Year != year {
		return "reject", "media_year_conflict"
	}
	if seriesID < 1 || year < 1 || parsed.Year < 1 {
		return "hold", "media_identity_unproven"
	}
	// The date must occur in the work prefix, not only in an episode subtitle.
	// RecognizeRelease now keeps this date separate from its cleaned title.
	index := standardEpRegex.FindStringIndex(raw)
	if alternate := altEpRegex.FindStringIndex(raw); alternate != nil && (index == nil || alternate[0] < index[0]) {
		index = alternate
	}
	if index == nil {
		return "hold", "media_identity_unproven"
	}
	prefixYear, ambiguous := recognizeYear(raw[:index[0]])
	if ambiguous || prefixYear != year {
		return "hold", "media_identity_unproven"
	}
	if normalizeMovieTitle(parsed.Title) == normalizeMovieTitle(title) || (original != "" && normalizeMovieTitle(parsed.Title) == normalizeMovieTitle(original)) {
		return "suggested", "exact_title_year_review"
	}
	return "hold", "media_title_unproven"
}
