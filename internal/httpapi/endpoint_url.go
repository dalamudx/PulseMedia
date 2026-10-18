package httpapi

import (
	"net/url"
	"strings"
)

// endpointDisplayURL is a response projection only. Stored configuration and
// request inputs are never rewritten by this helper, including legacy URLs.
func endpointDisplayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	redacted := u.User != nil || u.Fragment != ""
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		// A malformed query must not make an unparsed secret visible.
		u.RawQuery = ""
		u.ForceQuery = false
		return u.String()
	}
	for key := range query {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
		sensitive := normalized == "url" || normalized == "link" || strings.HasSuffix(normalized, "url") || strings.HasSuffix(normalized, "link")
		for _, marker := range []string{"password", "passwd", "token", "secret", "apikey", "passkey", "cookie", "auth", "credential", "dsn", "download", "magnet"} {
			sensitive = sensitive || strings.Contains(normalized, marker)
		}
		if sensitive {
			redacted = true
			query[key] = []string{"***"}
		}
	}
	if !redacted {
		return raw
	}
	u.RawQuery = query.Encode()
	return u.String()
}
