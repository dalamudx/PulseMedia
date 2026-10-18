package media

import (
	"regexp"
	"strings"
)

// Whole preserved release layouts, not a rule to strip arbitrary brackets.
// Numbers are display delimiters only: callers keep raw scope and authority.
var virenAnimeLexical = []struct {
	re    *regexp.Regexp
	group string
}{
	{regexp.MustCompile(`^\[Lazyleido-Mini\] (.+?) - [0-9]{2} \(S[0-9]{2}E[0-9]{2}\) - \(WEB 1080p AV1 10-bit AAC 2\.0\) \[[A-F0-9]{8}\]$`), "Lazyleido-Mini"},
	{regexp.MustCompile(`^\[Anipakku\][ _]+(.+?)[ _]+-[ _]+Season[ _]+[0-9]{1,2}[ _]+[0-9]{1,3}\.mkv$`), "Anipakku"},
	{regexp.MustCompile(`^\[Erai-raws\] (.+?) Season [0-9]{1,2} - [0-9]{1,3} \[(?:480p|720p|1080p|2160p) CR WEBRip HEVC AAC\]\[MultiSub\]\[[A-F0-9]{8}\]$`), "Erai-raws"},
	{regexp.MustCompile(`^Erai-raws-(.+?)_Season_[0-9]{1,2}-[0-9]{1,3}-(?:480p|720p|1080p|2160p)_CR_WEB-DL_AVC_AAC-MultiSub-[A-F0-9]{8}\.mkv$`), "Erai-raws"},
	{regexp.MustCompile(`^\[Erai-raws\] (.+?) [0-9]{1,2}(?:st|nd|rd|th) Season - [0-9]{1,3} \[(?:480p|720p|1080p|2160p)\]\.mkv$`), "Erai-raws"},
	{regexp.MustCompile(`^\[inid4c\] (.+?) [0-9]{2} \(BD (?:720p|1080p) FLAC\) \[[A-F0-9]{8}\]\.mkv$`), "inid4c"},
	{regexp.MustCompile(`^\[ParanDark\]_(.+?)_[0-9]{3}_v[0-9]+_\(R2DVD_576p_x264_AC3\)\.mkv$`), "ParanDark"},
	{regexp.MustCompile(`^\[a-s\]_(.+?)_-_[0-9]{3}_-_[^/\\]+__rs2_\[1080p_bd-rip\]\[[A-F0-9]{8}\]\.mkv$`), "a-s"},
	{regexp.MustCompile(`^\[Kaerizaki-Fansub\] (.+?) [0-9]{1,4} VOSTFR FHD \(1920x1080\)\.mp4$`), "Kaerizaki-Fansub"},
	{regexp.MustCompile(`^([^/\\\[\]{}]+?) S[0-9]{1,2} OVA - [0-9]{1,3}\.mkv$`), ""},
}
var virenLostYears = regexp.MustCompile(`^\[LostYears\] ([^/\\\[\]{}]+) \(WEB (?:480p|720p|1080p|2160p) x264 AAC\) \[Dual-Audio\]$`)

func virenAnimeTitle(raw string) (string, string) {
	if strings.ContainsAny(raw, "/\\\r\n") {
		return "", ""
	}
	for _, p := range virenAnimeLexical {
		if m := p.re.FindStringSubmatch(raw); m != nil && !strings.ContainsAny(m[1], "[]{}") {
			return normalizeTitleSeparators(m[1]), p.group
		}
	}
	if m := virenLostYears.FindStringSubmatch(raw); m != nil {
		return m[1], "LostYears"
	}
	return "", ""
}
