package media

import (
	"regexp"
	"strings"
)

// Certified multilingual publisher layouts provide a display view only.
// Do not feed the chosen literal spelling back into matching or numbering.
var virenBracketTitles = []*regexp.Regexp{
	regexp.MustCompile(`^【[0-9]{1,2}月/悠哈璃羽字幕社】\[UHA-WINGS\]\[[^\[\]]+\]\[([A-Za-z][^\[\]/]+)\]\[[0-9]{2}\]\[1080p AVC_AAC\]\[简繁外挂\]\[sc_tc\]$`),
	regexp.MustCompile(`^\[GM-Team\]\[国漫\]\[[^\[\]]+\]\[([A-Za-z][^\[\]/]+)\]\[[0-9]{2}-[0-9]{2}\]\[AVC\]\[GB\]\[1080P\]$`),
	regexp.MustCompile(`^\[NC-Raws\] [^/\[\]]+ / ([^/\[\]]+) - [0-9]{2} \(B-Global 1920x1080 HEVC AAC MKV\)$`),
}
var virenMonthlyTitle = regexp.MustCompile(`^【喵萌奶茶屋】★[0-9]{2}月新番★\[([^\[\]]+)\]\[[0-9]{2}\]\[(?:720p|1080p)\]\[(?:简体|繁體)\](?:\[招募翻译\])?$`)
var virenRomanAlias = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 !'.,×-]*$`)

func virenBracketTitle(raw string) string {
	if strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}
	for _, re := range virenBracketTitles {
		if m := re.FindStringSubmatch(raw); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	if m := virenMonthlyTitle.FindStringSubmatch(raw); m != nil {
		title := ""
		for _, alias := range strings.Split(m[1], "/") {
			if virenRomanAlias.MatchString(alias) {
				if title != "" {
					return ""
				}
				title = alias
			}
		}
		return title
	}
	return ""
}
