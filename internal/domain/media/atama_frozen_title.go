package media

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Frozen MIT Atama literal evidence certifies these publishers and complete
// layouts, never work identities, source metadata-only scopes, or unit authority.
var atamaFrozenMovieForms = []*regexp.Regexp{
	regexp.MustCompile(`^\[project-gxs\] (.+?) v[1-9][0-9]* \[10bit BD 1080p\]$`),
	regexp.MustCompile(`^\[SallySubs\] (.+?) \[BD 1080p FLAC\]$`),
	regexp.MustCompile(`^\[Arukoru\] (.+?) \([\p{L}][\p{L} .'-]+\) \[1080p x265 10bit BD Dual Audio AAC\]$`),
	regexp.MustCompile(`^\[Cleo\] (.+?) \[Dual Audio 10bit (?:BD1080p|DVD480p)\]\[HEVC-x265\]$`),
}
var atamaFrozenSpecial = regexp.MustCompile(`^\[DeadFish\] (.+?) - Special \[BD\]\[1080p\]\[AAC\]\.mp4$`)
var atamaFrozenVolume = regexp.MustCompile(`^(.+?) Volume [1-9][0-9]{0,2} \[BD\]\[x264,720p,AAC\]\[10Bit\]\[DarkHollow\]$`)
var atamaFrozenCRC = regexp.MustCompile(`^\[Watashi\]_[^\[\]\r\n]+_[0-9]{1,3}_\[576p\]\[[0-9A-Fa-f]{8}\]\.mkv$`)
var atamaFrozenWebRange = regexp.MustCompile(`^\[inid4c\] (.+?) WEB [0-9]{1,3}-[0-9]{1,3} \(1080p\)$`)
var atamaFrozenRevisionEpisode = regexp.MustCompile(`^\[inid4c\] (.+?) [0-9]{1,3} v[1-9][0-9]* \(1080p\)$`)
var atamaFrozenCompeting = regexp.MustCompile(`^\[AnimeRG\] .+ Season [0-9]{1,2} Part [0-9]{1,2} \([0-9]{1,3}-[0-9]{1,3}\) \((?:19|20)[0-9]{2}\) \(Complete\) \[1080p\] \[JRR\] \[[^\[\]]+ [0-9]{1,3}-[0-9]{1,3}\]$`)
var atamaFrozenAbsolute = regexp.MustCompile(`^\[Commie\] (.+_[0-9]{4}) - [0-9]{1,3} \[[0-9A-Fa-f]{8}\]\.mkv$`)
var atamaFrozenSlashTitle = regexp.MustCompile(`^[.\p{L}\p{N}_-]+//[\p{L}\p{N}_-]+$`)

func atamaFrozenSafeTitle(title string, slash bool) bool {
	if title == "" || strings.ContainsAny(title, "[]{}\\\r\n|") {
		return false
	}
	if strings.Contains(title, "/") && (!slash || !atamaFrozenSlashTitle.MatchString(title)) {
		return false
	}
	return !ambiguousEpisodeRegex.MatchString(title) && !competingCoverageRegex.MatchString(title) && !extraAbsoluteCoverageRegex.MatchString(title) && !partialEpisodeSuffixRegex.MatchString(title) && !yearRegex.MatchString(title) && len(calendarFilenameSpans(title)) == 0 && !collectionCoverage(title) && !localizedTVHintRegex.MatchString(title)
}

// Returns only lexical display data. mode movie is a yearless review fallback;
// hold is a filename-supported refusal, not external Batch/unknown metadata.
func atamaFrozenLexical(name string) (title, group, mode string) {
	if strings.ContainsAny(name, "\r\n\\") {
		return
	}
	for _, form := range atamaFrozenMovieForms {
		if m := form.FindStringSubmatch(name); m != nil {
			title = m[1]
			if strings.HasPrefix(name, "[Cleo]") && strings.Contains(title, " | ") {
				aliases := strings.Split(title, " | ")
				if len(aliases) != 2 || !atamaFrozenSafeTitle(aliases[1], false) {
					return "", "", ""
				}
				title = aliases[0]
			}
			if !atamaFrozenSafeTitle(title, strings.HasPrefix(name, "[Cleo]")) {
				return "", "", ""
			}
			return title, "", "movie"
		}
	}
	if m := atamaFrozenSpecial.FindStringSubmatch(name); m != nil && atamaFrozenSafeTitle(m[1], false) {
		return m[1], "DeadFish", "hold"
	}
	if m := atamaFrozenVolume.FindStringSubmatch(name); m != nil && atamaFrozenSafeTitle(m[1], false) {
		return m[1], "", "hold"
	}
	if atamaFrozenCRC.MatchString(name) && filepath.Base(name) == name {
		return "", "", "hold"
	}
	for _, form := range []*regexp.Regexp{atamaFrozenWebRange, atamaFrozenRevisionEpisode} {
		if m := form.FindStringSubmatch(name); m != nil && atamaFrozenSafeTitle(m[1], false) {
			return m[1], "inid4c", "hold"
		}
	}
	if atamaFrozenCompeting.MatchString(name) && filepath.Base(name) == name {
		return "", "", "hold"
	}
	return "", "", ""
}

// A canonical token cannot hide a second unbound scope or a slash/alias
// fragment in these complete certified layouts. Ordinary single SxxExx
// releases retain the original parser behavior.
func atamaFrozenMixed(name string) bool {
	if !standardEpRegex.MatchString(name) && !altEpRegex.MatchString(name) {
		return false
	}
	if strings.HasPrefix(name, "[Commie] ") && animeRegex1.MatchString(name) {
		return true
	}
	for _, form := range []*regexp.Regexp{atamaFrozenSpecial, atamaFrozenVolume, atamaFrozenCRC, atamaFrozenWebRange, atamaFrozenRevisionEpisode, atamaFrozenAbsolute} {
		if form.MatchString(name) {
			return true
		}
	}
	if strings.HasPrefix(name, "[Cleo] ") && (strings.Contains(name, "/") || strings.Contains(name, " | ")) {
		if atamaFrozenMovieForms[3].MatchString(name) {
			return true
		}
		// Retain complete parent lexical-title evidence when the final segment
		// looks like a canonical episode; filepath.Base alone loses that evidence.
		for i := 0; i < len(name); i++ {
			if name[i] == '/' && atamaFrozenMovieForms[3].MatchString(name[:i]) {
				return true
			}
		}
	}
	return false
}

func RecognizeRelease(filename string) EpisodeRecognition {
	res := recognizeReleaseEvidence(filename)
	if res.Reason == "non_video_file" {
		return res
	}
	if atamaFrozenMixed(filename) {
		return EpisodeRecognition{RawName: filename, MediaType: TypeTV, Quality: extractQuality(filename), RequiresReview: true, Reason: "conflicting_episode_coverage", ParserVersion: EpisodeParserVersion}
	}
	title, group, mode := atamaFrozenLexical(filename)
	if mode != "" {
		res.Title, res.Year, res.Season, res.AbsoluteEpisode = title, 0, 0, 0
		res.Episodes, res.UnitKeys = nil, nil
		res.IsAnime, res.RequiresReview = false, true
		// Use complete original bytes, including slash-containing lexical titles.
		res.Quality = extractQuality(filename)
		res.Quality.Group = group
		res.MediaType, res.Confidence, res.Reason = TypeTV, 0, "episode_coverage_unresolved"
		if mode == "movie" {
			res.MediaType, res.Confidence, res.Reason = TypeMovie, .6, "title_only_without_year"
		}
	}
	if res.Reason == "absolute_mapping_required" && res.AbsoluteEpisode > 0 {
		if m := atamaFrozenAbsolute.FindStringSubmatch(filename); m != nil && !strings.ContainsAny(m[1], "/\\[]{}") {
			res.Title = m[1]
		}
	}
	return archiveScopeRefusalRelease(filename, virenRepairRelease(filename, nextBRepairRelease(filename, rlsNextRelease(filename, chillRepairRelease(filename, scoutRepairRelease(filename, rlsRepairRelease(filename, res)))))))
}

func Recognize(filename string) Recognition {
	res := recognizeFilenameEvidence(filename)
	if _, nonVideo := subtitleFileTitleView(filename); nonVideo {
		return res
	}
	if atamaFrozenMixed(filename) {
		return Recognition{RawName: filename, Quality: extractQuality(filename), RequiresReview: true}
	}
	if title, group, mode := atamaFrozenLexical(filename); mode != "" {
		res.Title, res.Year, res.Identity, res.RequiresReview = title, 0, nil, true
		res.Quality = extractQuality(filename)
		res.Quality.Group = group
		res.Confidence = 0
		if mode == "movie" {
			res.Confidence = .6
		}
	}
	return archiveScopeRefusalMovie(filename, virenRepairMovie(filename, nextBRepairMovie(filename, rlsNextMovie(filename, chillRepairFilename(filename, scoutRepairFilename(filename, rlsRepairMovie(filename, res)))))))
}
