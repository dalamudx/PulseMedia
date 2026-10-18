package media

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var ErrMovieUpgradeFilesReview = errors.New("movie upgrade file evidence requires manual review")

const MovieUpgradeFilesVersion = "movie-upgrade.files.v1"

// MovieUpgradeTarget is a server-frozen identity, never a request DTO or authority.
// Aliases must already have been resolved to this same media by trusted metadata.
type MovieUpgradeTarget struct {
	MediaID       uuid.UUID
	UnitID        uuid.UUID
	MediaVersion  int64
	UnitVersion   int64
	MediaType     Type
	UnitKey       string
	OriginalTitle string
	Year          int
	Identities    []Identity
	Aliases       []string
	ParserVersion string
}

type MovieUpgradeUnitFile struct {
	Path         string
	Size         int64
	MediaID      uuid.UUID
	UnitID       uuid.UUID
	MediaVersion int64
	UnitVersion  int64
}

var movieUpgradeUnsafe = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(?:tv|ova|oav|specials?|sample|clip|trailer|part[ ._-]*[0-9]*|cd[ ._-]*[0-9]+|disc[ ._-]*[0-9]+|half|collection|complete|pack|series|season[ ._-]*[0-9]*|episodes?[ ._-]*[0-9]+|vol[ ._-]*[0-9]+)(?:$|[^\p{L}\p{N}])|\.part[0-9]+\.rar|\.(?:rar|zip|7z|r[0-9]{2})$`)

func movieUpgradeComponent(s string) bool {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) || !norm.NFC.IsNormalString(s) || s == "." || s == ".." || strings.TrimSpace(s) != s || strings.HasSuffix(s, ".") || strings.ContainsAny(s, "/\\:<>\"|?*") {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return false
	}
	return true
}

func movieUpgradeTitles(t MovieUpgradeTarget) ([]string, bool) {
	if t.MediaID == uuid.Nil || t.UnitID == uuid.Nil || t.MediaVersion < 1 || t.UnitVersion < 1 || t.MediaType != TypeMovie || t.UnitKey != FormatMovieUnitKey() || t.Year < 1900 || t.Year > 2099 || t.ParserVersion != EpisodeParserVersion || len(t.Aliases) > 16 || len(t.Identities) == 0 || len(t.Identities) > 8 {
		return nil, false
	}
	ids := map[string]bool{}
	for _, i := range t.Identities {
		if ids[i.Namespace] {
			return nil, false
		}
		ids[i.Namespace] = true
		switch i.Namespace {
		case "tmdb.movie":
			if !regexpMovieUpgradeTMDB.MatchString(i.ExternalID) {
				return nil, false
			}
		case "imdb":
			if !regexpMovieUpgradeIMDB.MatchString(i.ExternalID) {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	titles := append([]string{t.OriginalTitle}, t.Aliases...)
	seen := map[string]bool{}
	for _, s := range titles {
		n := normalizeMovieTitle(s)
		if !movieUpgradeComponent(s) || n == "" || seen[n] {
			return nil, false
		}
		seen[n] = true
	}
	return titles, true
}

var regexpMovieUpgradeTMDB = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
var regexpMovieUpgradeIMDB = regexp.MustCompile(`^tt[0-9]{7,10}$`)

// Only strip an exact server-bound title prefix for the lexical marker filter.
// The original name still goes through identity, year and complete coverage checks.
func movieUpgradeTitleSpelling(s string) string {
	return strings.NewReplacer(".", " ", "_", " ").Replace(s)
}

func movieUpgradeUnsafeOutsideTitle(s string, titles []string) bool {
	if !movieUpgradeUnsafe.MatchString(s) {
		return false
	}
	name := movieUpgradeTitleSpelling(s)
	for _, title := range titles {
		literal := movieUpgradeTitleSpelling(title)
		if len(name) >= len(literal) && strings.EqualFold(name[:len(literal)], literal) && (len(name) == len(literal) || name[len(literal)] == ' ') {
			return movieUpgradeUnsafe.MatchString(strings.TrimSpace(name[len(literal):]))
		}
	}
	return true
}

func movieUpgradeNameMatches(s string, t MovieUpgradeTarget, titles []string, bare bool) bool {
	if movieUpgradeUnsafeOutsideTitle(s, titles) || ambiguousEpisodeRegex.MatchString(s) || competingCoverageRegex.MatchString(s) || localizedTVHintRegex.MatchString(s) || frenchSeasonNumberHintRegex.MatchString(s) || len(calendarFilenameSpans(s)) > 0 {
		return false
	}
	for _, title := range titles {
		// Bare parents are literal titles only. Never strip unrecognized decorations.
		if bare && (strings.EqualFold(s, title) || strings.EqualFold(movieUpgradeTitleSpelling(s), movieUpgradeTitleSpelling(title))) {
			return movieUpgradeBareDeclarationMatches(s, t)
		}
	}
	if !movieUpgradeSupportedLayout(s) {
		return false
	}
	r := RecognizeRelease(s)
	if r.ParserVersion != EpisodeParserVersion || r.MediaType != TypeMovie || r.RequiresReview || r.Year != t.Year || len(r.UnitKeys) != 1 || r.UnitKeys[0] != FormatMovieUnitKey() || r.AbsoluteEpisode != 0 || len(r.Episodes) != 0 {
		return false
	}
	rec := Recognize(s)
	if rec.RequiresReview || rec.Year != t.Year {
		return false
	}
	ids := map[string]string{}
	for _, i := range t.Identities {
		ids[i.Namespace] = i.ExternalID
	}
	for _, title := range titles {
		// Even matching external IDs cannot substitute for the frozen title/year.
		if normalizeMovieTitle(rec.Title) == normalizeMovieTitle(title) && MatchesAutomaticMovie(s, title, t.Year, ids) {
			return true
		}
	}
	return false
}

// A literal metadata alias may omit the year, but cannot override contrary
// declarations in the complete parent or attachment name.
func movieUpgradeBareDeclarationMatches(s string, t MovieUpgradeTarget) bool {
	if collectionCoverage(s) || partialMovieSuffix(s) {
		return false
	}
	year, ambiguous := recognizeYear(s)
	if ambiguous || (year != 0 && year != t.Year) {
		return false
	}
	ids := map[string]string{}
	for _, identity := range t.Identities {
		ids[identity.Namespace] = identity.ExternalID
	}
	for _, match := range tmdbIDRegex.FindAllStringSubmatch(s, -1) {
		if ids["tmdb.movie"] != match[1] {
			return false
		}
	}
	for _, match := range imdbIDRegex.FindAllStringSubmatch(s, -1) {
		if !strings.EqualFold(ids["imdb"], match[2]) {
			return false
		}
	}
	coverage := RecognizeRelease(s)
	if coverage.MediaType != TypeMovie || coverage.Season != 0 || coverage.AbsoluteEpisode != 0 || len(coverage.Episodes) != 0 {
		return false
	}
	return coverage.Reason == "title_only_without_year" || (!coverage.RequiresReview && len(coverage.UnitKeys) == 1 && coverage.UnitKeys[0] == FormatMovieUnitKey())
}

// ValidateMovieUpgradeTorrentFiles checks the entire ordinary-file inventory.
// Symlink/type checks belong to the raw metainfo inspector; file claims alone
// cannot establish ordinary-file provenance or execution permission.
// Unsupported layouts return review; no missing/unknown coverage is inferred.
func ValidateMovieUpgradeTorrentFiles(files []TVTorrentFile, t MovieUpgradeTarget) (*MovieUpgradeUnitFile, error) {
	fail := ErrMovieUpgradeFilesReview
	titles, ok := movieUpgradeTitles(t)
	if !ok || len(files) < 1 || len(files) > 4096 {
		return nil, fail
	}
	paths := map[string]bool{}
	directories := map[string]string{}
	var video *MovieUpgradeUnitFile
	for _, f := range files {
		if len(f.Path) > 4096 || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Size < 0 {
			return nil, fail
		}
		parts := strings.Split(f.Path, "/")
		if len(parts) > 32 {
			return nil, fail
		}
		ext := strings.ToLower(path.Ext(f.Path))
		attachment := false
		switch ext {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".nfo", ".txt", ".jpg", ".jpeg", ".png":
			attachment = true
		case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".ts", ".m2ts":
		default:
			return nil, fail
		}
		for n, c := range parts {
			if !movieUpgradeComponent(c) || movieUpgradeUnsafeOutsideTitle(c, titles) {
				return nil, fail
			}
			if n < len(parts)-1 {
				prefix := strings.Join(parts[:n+1], "/")
				key := cases.Fold().String(prefix)
				if previous, exists := directories[key]; exists && previous != prefix {
					return nil, fail
				}
				directories[key] = prefix
				if attachment && (strings.EqualFold(c, "Subs") || strings.EqualFold(c, "Subtitles")) && n == len(parts)-2 && n > 0 {
					continue
				}
				if !movieUpgradeNameMatches(c, t, titles, true) {
					return nil, fail
				}
			}
		}
		folded := cases.Fold().String(f.Path)
		if paths[folded] {
			return nil, fail
		}
		paths[folded] = true
		base := path.Base(f.Path)
		if attachment {
			stem := strings.TrimSuffix(base, path.Ext(base))
			switch strings.ToLower(stem) {
			case "readme", "poster", "cover", "folder", "en", "eng", "zh", "zho", "chs", "cht":
			default:
				if !movieUpgradeNameMatches(stem, t, titles, true) {
					return nil, fail
				}
			}
			continue
		}
		if video != nil || f.Size < 1 || !movieUpgradeNameMatches(base, t, titles, false) {
			return nil, fail
		}
		video = &MovieUpgradeUnitFile{f.Path, f.Size, t.MediaID, t.UnitID, t.MediaVersion, t.UnitVersion}
	}
	for p := range paths {
		for parent := strings.LastIndexByte(p, '/'); parent >= 0; parent = strings.LastIndexByte(p[:parent], '/') {
			if paths[p[:parent]] {
				return nil, fail
			}
		}
	}
	if video == nil {
		return nil, fail
	}
	return video, nil
}

// Only explicitly supported title/year/technical suffix layouts are accepted.
// The shared recognizer's title extraction alone can ignore arbitrary tail words.
// Unknown release groups, disc layouts and unexplained suffixes remain review.
var movieUpgradeTail = regexp.MustCompile(`(?i)^(?:[ ._()\[\]{}-]*(?:2160p|1080p|720p|480p|blu-ray|bluray|web-dl|webrip|remux|x264|x265|h264|hevc|hdr10|sdr|aac|dts))*[ ._()\[\]{}-]*$`)

func movieUpgradeSupportedLayout(s string) bool {
	switch strings.ToLower(path.Ext(s)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".ts", ".m2ts":
		s = strings.TrimSuffix(s, path.Ext(s))
	}
	years := yearRegex.FindAllStringIndex(s, -1)
	if len(years) != 1 {
		return false
	}
	tail := s[years[0][1]:]
	tail = tmdbIDRegex.ReplaceAllString(tail, "")
	tail = imdbIDRegex.ReplaceAllString(tail, "")
	return movieUpgradeTail.MatchString(tail)
}
