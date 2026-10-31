package tmdb

import "strings"

// Missing origin facts remain unknown. Only provider ISO codes may guide classification.
func validClassificationOrigin(language string, countries []string) bool {
	if language != "" && (len(language) != 2 || strings.Trim(language, "abcdefghijklmnopqrstuvwxyz") != "") {
		return false
	}
	if len(countries) > 64 {
		return false
	}
	seen := make(map[string]bool, len(countries))
	for _, country := range countries {
		if len(country) != 2 || strings.Trim(country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" || seen[country] {
			return false
		}
		seen[country] = true
	}
	return true
}
