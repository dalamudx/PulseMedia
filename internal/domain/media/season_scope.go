package media

import "regexp"

// A season declaration is incomplete coverage, never one canonical movie.
// Complete standard episodes do not match the standalone season token.
var seasonScopeRegex = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s[0-9]{1,3}\b|seasons?[ ._-]+[0-9]{1,3}\b)`)

func seasonOnlyCoverage(name string) bool { return seasonScopeRegex.MatchString(name) }
