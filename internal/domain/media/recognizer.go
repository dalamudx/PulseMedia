package media

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Strip only certified release metadata at the front of a title. Keep raw
// evidence and quality extraction separate, and retain arbitrary brackets.
var releaseTitleSitePrefixRegex = regexp.MustCompile(`(?i)^\s*(?:1337x\.to[ \t]+-[ \t]+|\(tracker\.xn--p1ai\)[ \t]+|www[ \t]+1?TamilBlasters[ \t]+tel[ \t]+-[ \t]+|\[\s*(?:www\.)?[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.[a-z]{2,24}\s*\]\s*|(?:www[.,]|ww\.)[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.[a-z]{2,24}\s+-\s+)`)
var nonGroupMetadataTailRegex = regexp.MustCompile(`(?i)^(?:vostfr|esubs?|[0-9]+(?:\.[0-9]+)?(?:kb|mb|gb|kib|mib|gib)[\])}]+)$`)
var releaseTitleTechnicalPrefixRegex = regexp.MustCompile(`(?i)^\s*\[(?:dvdrip|bdrip|brrip|webrip|web-dl)(?:[- ](?:ita|eng|french|german))*\]\s*`)
var releaseTitleBracketTailRegex = regexp.MustCompile(`\s*\[[\p{L}\p{N}-]{2,32}\]\s*$`)

func stripReleaseTitleDecorations(s string) string {
	s = stripCertifiedPublisherMovieTitle(s)
	if prefix := releaseTitleSitePrefixRegex.FindStringIndex(s); prefix != nil {
		s = s[prefix[1]:]
	}
	if prefix := releaseTitleTechnicalPrefixRegex.FindStringIndex(s); prefix != nil {
		s = s[prefix[1]:]
		s = releaseTitleBracketTailRegex.ReplaceAllString(s, "")
	}
	return s
}

var (
	coverageGroupTailRegex   = regexp.MustCompile(`(?i)^(?:E[0-9]{1,4}|S[0-9]+(?:E[0-9]{1,4})?)(?:\[[^\]]*\])*$`)
	ultraCompressedTailRegex = regexp.MustCompile(`(?i)\bultra[ ._-]+compressed$`)
	absoluteGroupTailRegex   = regexp.MustCompile(`(?i)^[0-9]{1,4}(?:v[0-9]+)?$`)
	dottedInitialGroupRegex  = regexp.MustCompile(`^[A-Za-z](?:\.[A-Za-z]){1,7}$`)
	tmdbIDRegex              = regexp.MustCompile(`(?i)\{tmdb-(\d+)\}`)
	imdbIDRegex              = regexp.MustCompile(`(?i)\[?(imdb-)?(tt\d{7,10})\]?`)
	yearRegex                = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)

	// Language markers are release suffixes only with a quality boundary; a
	// bare/middle title word such as French remains part of the work name.
	titleLanguageSuffixRegex  = regexp.MustCompile(`(?i)(?:\s+(?:truefrench|french|multi|vostfr|german\s+dl))+$`)
	titleQualityBoundaryRegex = regexp.MustCompile(`(?i)\b(?:2160p|1080p|720p|480p|4k|uhd|fhd|bluray|blu-ray|bdrip|brrip|dvdrip|hdrip|bdremux|remux|web-dl|webrip|hdtv|hevc|x265|x264|h264|av1|xvid|divx)\b`)
	// Common release noise words to strip from title
	noiseRegex = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|fhd|bluray|blu-ray|bdrip|brrip|dvdrip|hdrip|bdremux|remux|web-dl|webrip|hdtv|hevc|x265|x264|h264|av1|hdr|hdr10\+?|dv|dovi|dts-hd|dts|truehd|atmos|aac|ac3|flac|repack|proper|unrated|extended|directors\.cut)\b.*$`)
)

type Recognition struct {
	RawName        string
	Title          string
	Year           int
	Identity       *Identity
	Quality        quality.Profile
	Confidence     float64
	RequiresReview bool
}

// Recognize parses a file name or directory name into structured movie attributes with confidence scoring.
func recognizeFilenameEvidence(filename string) Recognition {
	if view, subtitle := subtitleFileTitleView(filename); subtitle {
		rec := Recognize(view)
		rec.RawName = filename
		rec.Identity = nil
		rec.Confidence = 0
		rec.RequiresReview = true
		return rec
	}
	if hasUnboundBroadcastCoverage(filename) {
		return Recognition{RawName: filename, Quality: extractQuality(filename), RequiresReview: true}
	}
	if hint := unboundTechnicalEpisodeHint(filename); hint != nil {
		profile := extractQuality(filename)
		if hint.publisher != "" {
			profile.Group = hint.publisher
		}
		return Recognition{RawName: filename, Title: cleanTitle(hint.title), Quality: profile, RequiresReview: true}
	}
	if hasParentEpisodeCoverage(filename) {
		return Recognition{RawName: filename, Quality: extractQuality(filename), RequiresReview: true}
	}
	rawBase := filepath.Base(filename)
	base := releaseTitleSitePrefixRegex.ReplaceAllString(rawBase, "")
	ext := filepath.Ext(base)
	// Release titles often have no file extension. Do not discard a trailing
	// quality token or explicit identity as though it were an extension.
	switch strings.ToLower(ext) {
	case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".iso":
	default:
		ext = ""
	}
	nameWithoutExt := strings.TrimSuffix(base, ext)

	rec := Recognition{
		RawName: filename,
	}

	if dates := calendarFilenameSpans(nameWithoutExt); len(dates) > 0 {
		rec.Title = cleanTitle(nameWithoutExt[:dates[0][0]])
		rec.Quality = extractQuality(nameWithoutExt)
		rec.RequiresReview = true
		return rec
	}

	// 1. Look for explicit external IDs ({tmdb-550} or tt0137523)
	if match := tmdbIDRegex.FindStringSubmatch(nameWithoutExt); len(match) > 1 {
		rec.Identity = &Identity{
			Namespace:  "tmdb.movie",
			ExternalID: match[1],
		}
	} else if match := imdbIDRegex.FindStringSubmatch(nameWithoutExt); len(match) > 2 {
		rec.Identity = &Identity{
			Namespace:  "imdb",
			ExternalID: match[2],
		}
	}

	// 2. Extract Year
	var ambiguousYear bool
	rec.Year, ambiguousYear = recognizeYear(nameWithoutExt)

	// 3. Extract Title
	// Normalize separators (. and _ to space)
	normalized := normalizeTitleSeparators(stripReleaseTitleDecorations(nameWithoutExt))

	// Strip explicit identity markers
	normalized = tmdbIDRegex.ReplaceAllString(normalized, "")
	normalized = imdbIDRegex.ReplaceAllString(normalized, "")

	if rec.Year > 0 {
		// A unique work year can precede or follow the title. Preserve the
		// ambiguous-year gate and do not turn numeric series names into dates.
		yearIndex := strings.Index(normalized, strconv.Itoa(rec.Year))
		if yearIndex >= 0 {
			prefix := strings.TrimSpace(normalized[:yearIndex])
			suffixIndex := yearIndex + 4
			// Paired asterisks decorate the unique work year; do not erase
			// title punctuation or an unpaired star elsewhere in the name.
			if yearIndex > 0 && normalized[yearIndex-1] == '*' && suffixIndex < len(normalized) && normalized[suffixIndex] == '*' {
				prefix = strings.TrimSpace(normalized[:yearIndex-1])
				suffixIndex++
			}
			if strings.Trim(prefix, "()[]{}") == "" {
				normalized = strings.TrimLeft(normalized[suffixIndex:], " ._()[]{}")
			} else {
				rec.Title = prefix
			}
		}
	}

	if rec.Title == "" {
		// Cut noise tags
		cleaned := noiseRegex.ReplaceAllString(stripUnsupportedReleaseTitleTail(normalized), "")
		rec.Title = strings.TrimSpace(cleaned)
		if titleQualityBoundaryRegex.MatchString(normalized) {
			rec.Title = titleLanguageSuffixRegex.ReplaceAllString(rec.Title, "")
		}

	}

	// Clean up any stray trailing dashes, brackets, parentheses
	rec.Title = trimOrdinaryPairedTitle(rec.Title, nameWithoutExt)
	if rec.Year == 0 {
		if title, ok := reviewedAnimeTitleView(filename); ok {
			rec.Title = title
		}
	}

	if title, ok := releaseLexicalTitleView(filename, rec.Year); ok {
		rec.Title = title
	}
	if title, ok := beastRemainingTitleView(filename, rec.Year); ok {
		rec.Title = title
	}
	// 4. Extract Quality Attributes
	rec.Quality = extractQuality(nameWithoutExt)

	// 5. Compute Confidence & Review requirement
	if rec.Identity != nil {
		rec.Confidence = 1.0
		rec.RequiresReview = false
	} else if rec.Title != "" && rec.Year > 0 {
		rec.Confidence = 0.9
		rec.RequiresReview = false
	} else if rec.Title != "" {
		rec.Confidence = 0.6
		rec.RequiresReview = true
	} else {
		rec.Title = strings.TrimSpace(nameWithoutExt)
		rec.Confidence = 0.3
		rec.RequiresReview = true
	}

	if ambiguousYear {
		rec.Year = 0
		rec.RequiresReview = true
		rec.Confidence = 0
	}
	if allSeasonsCoverageRegex.MatchString(filename) || partialMovieSuffix(nameWithoutExt) || collectionCoverage(nameWithoutExt) || coffretCollectionCoverageRegex.MatchString(filename) || seasonOnlyCoverage(nameWithoutExt) || publisherEpisodeRangeHint(filename) != nil || unboundOVATechnicalHint.MatchString(filename) {
		rec.RequiresReview = true
		rec.Confidence = 0
		rec.Identity = nil
	}
	return rec
}

// Only suffix markers beyond an explicit work-year boundary denote partial
// content. Without a work year, such a marker stays ambiguous even with an ID.
// Titles such as Sample.1999 and Trailer.Park.1999 remain intact.
func partialMovieSuffix(name string) bool {
	normalized := strings.NewReplacer(".", " ", "_", " ").Replace(name)
	normalized = tmdbIDRegex.ReplaceAllString(normalized, "")
	normalized = imdbIDRegex.ReplaceAllString(normalized, "")
	year, ambiguous := recognizeYear(name)
	if ambiguous {
		return false
	} // already rejected by the independent year guard
	if year == 0 {
		return partialEpisodeSuffixRegex.MatchString(normalized)
	}
	index := strings.Index(normalized, strconv.Itoa(year))
	return index >= 0 && partialEpisodeSuffixRegex.MatchString(normalized[index:])
}

func extractQuality(s string) quality.Profile {
	lower := strings.ToLower(s)
	p := quality.Profile{}

	// Resolution
	p.Resolution = quality.ParseResolution(s)
	if p.Resolution != "" {
		p.ResolutionParserVersion = quality.ResolutionTokenParserVersion
	}

	// Complete Unicode tokens: a title substring is not source or codec evidence.
	tokens := strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' })
	sources := map[quality.Source]bool{}
	codecs := map[quality.Codec]bool{}
	// These complete markers identify unsupported competing source/codec claims.
	// Unsupported is not rank zero and cannot disappear while an allowed claim survives.
	unsupportedSource, unsupportedCodec := false, false

	for i, token := range tokens {
		switch token {
		case "dvd", "dvdrip", "dvd5", "dvd9", "dvdscr", "camrip", "hdcam", "hdts", "telesync", "telecine":
			unsupportedSource = true
		case "xvid", "divx", "vp8":
			unsupportedCodec = true
		case "remux", "bdremux":
			sources[quality.SourceRemux] = true
		case "bluray", "bd", "bdrip", "brrip":
			sources[quality.SourceBluray] = true
		case "blu":
			if i+1 < len(tokens) && tokens[i+1] == "ray" {
				sources[quality.SourceBluray] = true
			}
		case "webdl":
			sources[quality.SourceWEBDL] = true
		case "web":
			if i+1 < len(tokens) && tokens[i+1] == "dl" {
				sources[quality.SourceWEBDL] = true
			}
		case "webrip":
			sources[quality.SourceWEBRip] = true
		case "hdtv":
			sources[quality.SourceHDTV] = true
		case "hevc", "x265", "h265":
			codecs[quality.CodecHEVC] = true
		case "x264", "h264", "avc":
			codecs[quality.CodecH264] = true
		case "av1":
			codecs[quality.CodecAV1] = true
		case "h":
			if i+1 < len(tokens) {
				switch tokens[i+1] {
				case "264":
					codecs[quality.CodecH264] = true
				case "265":
					codecs[quality.CodecHEVC] = true
				}
			}
		}
	}
	// Blu-ray REMUX is one specific source, not conflicting releases.
	if sources[quality.SourceRemux] {
		delete(sources, quality.SourceBluray)
	}
	if len(sources) == 1 && !unsupportedSource {
		for source := range sources {
			p.Source = source
		}
		p.SourceParserVersion = quality.SourceTokenParserVersion
	}
	if len(codecs) == 1 && !unsupportedCodec {
		for codec := range codecs {
			p.Codec = codec
		}
		p.CodecParserVersion = quality.CodecTokenParserVersion
	}

	// HDR facts require complete tokens, not DVD / Adventure substrings.
	has := func(want string) bool {
		for _, token := range tokens {
			if token == want {
				return true
			}
		}
		return false
	}
	dolbyVision := false
	for i, token := range tokens {
		if (token == "dolby" || token == "hdr+dolby" || token == "hdr10+dolby") && i+1 < len(tokens) && tokens[i+1] == "vision" {
			dolbyVision = true
		}
	}
	switch {
	case has("dv") || has("dovi") || dolbyVision:
		p.HDR = quality.HDRDolbyVision
	case has("hdr10+"):
		p.HDR = quality.HDR10Plus
	case has("hdr10") || has("hdr"):
		p.HDR = quality.HDR10
	case has("sdr"):
		p.HDR = quality.HDRSDR
	}

	if p.HDR != "" {
		p.HDRParserVersion = quality.HDRTokenParserVersion
	}
	// External ID decorations are not a release group. Keep the original name
	// as evidence; only use this lexical view for group suffix extraction.
	groupName := quality.WithoutReleaseIdentityTags(s)
	if lastDash := quality.ReleaseGroupDelimiter(groupName); lastDash >= 0 {
		candidate := stripKnownRelayTags(groupName[lastDash+1:])
		if (len(candidate) > 1 || singleLetterTechnicalGroup(groupName, candidate, p)) && !coverageGroupTailRegex.MatchString(candidate) && (!strings.EqualFold(candidate, "Compressed") || !ultraCompressedTailRegex.MatchString(stripKnownRelayTags(groupName))) && (!absoluteEpisodeHintRegex.MatchString(s) || !absoluteGroupTailRegex.MatchString(candidate)) && !nonGroupMetadataTailRegex.MatchString(candidate) && (!strings.ContainsAny(candidate, " ._") || dottedInitialGroupRegex.MatchString(candidate)) {
			p.Group = candidate
		}
	}

	if p.Group == "" && p.Resolution != "" && p.Source != "" && p.Codec != "" {
		p.Group = technicalParenthesizedGroup(groupName)
	}
	if year, ambiguous := recognizeYear(s); !ambiguous {
		if group, ok := beastRemainingRelayGroup(s, year); ok {
			p.Group = group
		}
	}
	return p
}

// Distribution tags are not release-group identity. Only known tags are
// removed from new observations; arbitrary brackets remain raw evidence.
func stripKnownRelayTags(s string) string {
	s = strings.TrimSpace(s)
	for {
		changed := false
		for _, tag := range []string{"[eztv]", "[eztvx.to]", "[ettv]", "[rartv]", "[tgx]", "[etrg]", "[rarbg]", "[publichd]", "(silvertorrent)"} {
			if strings.HasSuffix(strings.ToLower(s), tag) {
				s = strings.TrimSpace(s[:len(s)-len(tag)])
				changed = true
				break
			}
		}
		if !changed {
			return s
		}
	}
}
