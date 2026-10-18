package media

import (
	"regexp"
	"strings"
)

// Only publishers independently asserted by fixed standard-episode fixtures
// are recognized here. Unknown leading brackets remain ordinary raw evidence.
var standardEpisodePublisherPrefix = regexp.MustCompile(`(?i)^\[(DKB|EMBER|ToonsHub)\][ ._-]+`)

func standardEpisodeTitleView(prefix string) (string, string) {
	match := standardEpisodePublisherPrefix.FindStringSubmatchIndex(prefix)
	if match == nil {
		return prefix, ""
	}
	return prefix[match[1]:], prefix[match[2]:match[3]]
}

// A basename cannot overwrite explicit episode evidence in an alias or parent.
// A plain season directory is not an episode declaration.
func hasParentEpisodeCoverage(raw string) bool {
	last := strings.LastIndexAny(raw, `/\`)
	if last <= 0 {
		return false
	}
	parent := raw[:last]
	return standardEpRegex.MatchString(parent) || altEpRegex.MatchString(parent)
}
