package indexer

import "strings"

// ValidSiteCheckinSchedule validates syntax without filesystem or clock access.
// The repository additionally checks the name against PostgreSQL's timezone registry.
func ValidSiteCheckinSchedule(zone, dailyAt string) bool {
	if len(zone) == 0 || len(zone) > 96 || zone == "Local" || zone == "localtime" || zone == "Factory" || zone == "posixrules" || strings.HasPrefix(zone, "posix/") || strings.HasPrefix(zone, "right/") {
		return false
	}
	parts := strings.Split(zone, "/")
	for _, part := range parts {
		if len(part) == 0 {
			return false
		}
		letter := part[0] >= 'A' && part[0] <= 'Z' || part[0] >= 'a' && part[0] <= 'z'
		if !letter {
			return false
		}
		for _, c := range []byte(part) {
			allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '+'
			if !allowed {
				return false
			}
		}
	}
	return len(dailyAt) == 5 && dailyAt[2] == ':' && dailyAt[0] >= '0' && dailyAt[0] <= '2' && dailyAt[1] >= '0' && dailyAt[1] <= '9' && (dailyAt[0] != '2' || dailyAt[1] <= '3') && dailyAt[3] >= '0' && dailyAt[3] <= '5' && dailyAt[4] >= '0' && dailyAt[4] <= '9'
}
