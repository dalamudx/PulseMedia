package media

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const AutomaticTVIdentityRuleVersion = "automatic-tv-identity-v1"

// AutomaticTVIdentityDecision describes identity evidence only, never execution permission.
type AutomaticTVIdentityDecision struct {
	State         string
	Reason        string
	RuleVersion   string
	ParserVersion string
}

// DecideAutomaticTVIdentity requires title, original and year from trusted catalog
// metadata for seriesID. Callers must independently check current real aired Wanted
// coverage, quality, source/hash/rule versions, lifecycle, lease, subscription and root facts.
// Historical manual reviews cannot supply or upgrade this evidence.
func DecideAutomaticTVIdentity(raw, title, original string, year int, seriesID int64) AutomaticTVIdentityDecision {
	decision := func(state, reason string) AutomaticTVIdentityDecision {
		return AutomaticTVIdentityDecision{State: state, Reason: reason, RuleVersion: AutomaticTVIdentityRuleVersion, ParserVersion: EpisodeParserVersion}
	}
	if !automaticTVIdentitySafeText(raw) {
		return decision("hold", "unsafe_title")
	}
	if HasConflictingTVIdentity(raw, seriesID) {
		return decision("reject", "media_identity_conflict")
	}
	if imdbIDRegex.MatchString(raw) {
		return decision("hold", "external_identity_unverified")
	}
	if seriesID < 1 || year < 1 || year > 9999 {
		return decision("hold", "media_identity_unproven")
	}
	// Do not let punctuation-only or unsafe catalog aliases normalize to evidence.
	titleKey, originalKey := "", ""
	if automaticTVIdentitySafeText(title) {
		titleKey = normalizeMovieTitle(title)
	}
	if automaticTVIdentitySafeText(original) {
		originalKey = normalizeMovieTitle(original)
	}
	if titleKey == "" && originalKey == "" {
		return decision("hold", "media_identity_unproven")
	}
	rawYear, ambiguous := recognizeYear(raw)
	if ambiguous {
		return decision("hold", "ambiguous_media_year")
	}
	if rawYear > 0 && rawYear != year {
		return decision("reject", "media_year_conflict")
	}
	parsed := RecognizeRelease(raw)
	if parsed.Year > 0 && parsed.Year != year {
		return decision("reject", "media_year_conflict")
	}
	if parsed.ParserVersion != EpisodeParserVersion || parsed.MediaType != TypeTV || parsed.RequiresReview ||
		len(parsed.UnitKeys) != 1 || len(parsed.Episodes) != 1 || parsed.AbsoluteEpisode != 0 || parsed.IsAnime ||
		parsed.Season < 1 || parsed.Season > 99 || parsed.Episodes[0] < 1 ||
		parsed.UnitKeys[0] != FormatEpisodeUnitKey(parsed.Season, parsed.Episodes[0]) ||
		(parsed.Reason != "standard_season_episode" && parsed.Reason != "alt_season_episode") {
		return decision("hold", "identity_coverage_unresolved")
	}
	standard := standardEpRegex.FindAllStringIndex(raw, -1)
	alternate := altEpRegex.FindAllStringIndex(raw, -1)
	if len(standard)+len(alternate) != 1 {
		return decision("hold", "identity_coverage_unresolved")
	}
	index := 0
	if len(standard) == 1 {
		index = standard[0][0]
	} else {
		index = alternate[0][0]
	}
	prefix := raw[:index]
	prefixYear, ambiguous := recognizeYear(prefix)
	if ambiguous || prefixYear != year || parsed.Year != year {
		return decision("hold", "media_identity_unproven")
	}
	parsedKey := normalizeMovieTitle(parsed.Title)
	if parsedKey == "" || (parsedKey != titleKey && parsedKey != originalKey) {
		return decision("hold", "media_title_unproven")
	}
	// A lexical cleaner must not hide extra work-prefix text behind technical tags.
	prefix = tmdbIDRegex.ReplaceAllString(prefix, "")
	prefix, _ = standardEpisodeTitleView(prefix)
	prefix = yearRegex.ReplaceAllString(prefix, " ")
	if normalizeMovieTitle(prefix) != parsedKey {
		return decision("hold", "media_title_unproven")
	}
	return decision("eligible", "strict_title_year")
}

func automaticTVIdentitySafeText(s string) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 512 || UnsafeCandidateTitle(s) || strings.ContainsAny(s, "/\\\u2044\u2215\u29f8\uff0f\uff3c\u29f5") {
		return false
	}
	for _, r := range s {
		// Invisible/combining text and symbols must not disappear during title normalization.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsMark(r) || unicode.IsSymbol(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
