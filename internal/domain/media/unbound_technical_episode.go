package media

import (
	"regexp"
	"strings"
)

// A reject-only lexical hint. Neither field establishes identity, numbering,
// actual file coverage, or permission to acquire a unit. The caller retains the
// full input and decides whether other evidence makes the scope conflicting.
type technicalEpisodeScopeHint struct {
	title, publisher string
}

const technicalEpisodePrefix = `\[[^\[\]\r\n]{1,64}\]`
const technicalEpisodeTitle = `([^\[\]()\r\n]+?)`
const technicalEpisodeEnd = `(?:\.mkv)?(?:$|[ \t]+[^/\\\r\n]*(?:\bS[0-9]{1,4}E[0-9]{1,4}\b|\b[0-9]{1,2}x[0-9]{1,4}\b|\{tmdb-[0-9]+\}|\btt[0-9]+\b|\b(?:19|20)[0-9]{2}\b|-[ \t]*[0-9]{1,4}\b)[^/\\\r\n]*$)`

var unboundTechnicalEpisodeForms = []*regexp.Regexp{
	// Bare numbers need the entire observed technical/CRC tail. In particular,
	// a resolution, a version, or an arbitrary bracket alone is insufficient.
	regexp.MustCompile(`(?i)^` + technicalEpisodePrefix + `_` + technicalEpisodeTitle + `_[0-9]{1,3}_\((?:1920x1080|1280x720)_Blu-ray_FLAC\)_\[[0-9a-f]{8}\]` + technicalEpisodeEnd),
	regexp.MustCompile(`(?i)^` + technicalEpisodePrefix + ` +` + technicalEpisodeTitle + ` +[0-9]{1,3}(?:v[0-9]+)? +\[(?:480|720|1080|2160)p-Hi10P\] +\[[0-9a-f]{8}\]` + technicalEpisodeEnd),
	// Letter parts are unresolved content, never an absolute episode number.
	regexp.MustCompile(`(?i)^` + technicalEpisodePrefix + ` +` + technicalEpisodeTitle + ` +- +[0-9]{1,3}[AB] +\[(?:480|720|1080|2160)p\]` + technicalEpisodeEnd),
	regexp.MustCompile(`(?i)^` + technicalEpisodePrefix + `_` + technicalEpisodePrefix + `_` + technicalEpisodeTitle + `_Ep[0-9]{1,3}_\([0-9a-f]{8}\)` + technicalEpisodeEnd),
	regexp.MustCompile(`(?i)^` + technicalEpisodePrefix + ` +` + technicalEpisodeTitle + ` +Episode +[0-9]{1,3} +\(VP8\)` + technicalEpisodeEnd),
}

// Only this verified publisher and relay have title-preserving volume forms.
// NyaaTorrents is distribution evidence and must not become a release group.
var technicalPublisherVolume = regexp.MustCompile(`(?i)^\[(Chihiro-Amaterasu)\]_` + technicalEpisodeTitle + `_-_Vol\.[0-9]{1,3}_\[Blu-ray_(?:480|720|1080|2160)p_Hi10P_FLAC\]` + technicalEpisodeEnd)
var technicalRelayVolume = regexp.MustCompile(`(?i)^\[NyaaTorrents\] +` + technicalEpisodeTitle + ` +VOL +[0-9]{1,3} +\[DVD-ISO\]` + technicalEpisodeEnd)

// This complete alias/subtitle/audio/source/codec layout is independently
// observed. Do not replace it with arbitrary metadata-bracket stripping.
var technicalAliasRange = regexp.MustCompile(`(?i)^` + technicalEpisodeTitle + `_[0-9]{1,3}-[0-9]{1,3}_\[[\p{Han}\p{Hiragana}\p{Katakana}ー]+\]_\[[a-z][a-z _-]*\]_\[moo-shi_eng_softsub\]\[jap_audio\]\[DVD-Rip\]\[H264\]` + technicalEpisodeEnd)
var technicalTitleWorkYear = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?:19|20)[0-9]{2}(?:$|[^\p{L}\p{N}])`)

func unboundTechnicalEpisodeHint(name string) *technicalEpisodeScopeHint {
	// Every slash segment is evidence, including aliases and parent directories.
	// Merely containing SxxExx, 1x02, or a season directory is not this hint.
	for _, segment := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		segment = strings.TrimSpace(segment)
		for _, form := range unboundTechnicalEpisodeForms {
			if match := form.FindStringSubmatch(segment); match != nil {
				// A title ending in a separator belongs to the already supported
				// hyphen-bound absolute grammar, including numeric series titles.
				if strings.HasSuffix(strings.TrimSpace(match[1]), "-") {
					continue
				}
				return &technicalEpisodeScopeHint{}
			}
		}
		if match := technicalPublisherVolume.FindStringSubmatch(segment); match != nil && !technicalTitleWorkYear.MatchString(match[2]) {
			return &technicalEpisodeScopeHint{title: technicalHintTitle(match[2]), publisher: match[1]}
		}
		if match := technicalRelayVolume.FindStringSubmatch(segment); match != nil && !technicalTitleWorkYear.MatchString(match[1]) {
			return &technicalEpisodeScopeHint{title: technicalHintTitle(match[1])}
		}
		if match := technicalAliasRange.FindStringSubmatch(segment); match != nil && !technicalTitleWorkYear.MatchString(match[1]) {
			return &technicalEpisodeScopeHint{title: technicalHintTitle(match[1])}
		}
	}
	return nil
}

func technicalHintTitle(title string) string {
	// Only the verified structural separator is normalized. Keep punctuation
	// and all title words; no quality, year, or arbitrary bracket cleanup.
	return strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
}
