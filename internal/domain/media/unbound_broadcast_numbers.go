package media

import (
	"regexp"
	"strings"
)

// These fixed-source forms provide episode counterevidence, not an executable
// numbering scheme. A BS11 broadcast tail binds the otherwise bare number;
// ordinary movie years and Blu-ray title numbers do not use this grammar.
var unboundBS11Number = regexp.MustCompile(`(?i)(?:^|[/\\])\[[^\[\]/\\\r\n]{1,64}\][ ._]+([^/\\\r\n]+?)[ ._]+#?[0-9]{1,3}(?:v[0-9]+)?[ ._]+\(BS11[ ._]+(?:[0-9]{3,4}x[0-9]{3,4}|480p|720p|1080p|2160p)(?:[ ._)]|$)`)
var localizedEpisodioInterval = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])episodios?[ ._-]+[0-9]{1,4}[ \t]*(?:-|~)[ \t]*[0-9]{1,4}(?:$|[^a-z0-9])`)

func hasUnboundBroadcastCoverage(name string) bool {
	if localizedEpisodioInterval.MatchString(name) {
		return true
	}
	for _, match := range unboundBS11Number.FindAllStringSubmatch(name, -1) {
		// Hyphen-bound singles keep the existing mapping-required contract.
		if !strings.HasSuffix(strings.TrimSpace(match[1]), "-") {
			return true
		}
	}
	return false
}
