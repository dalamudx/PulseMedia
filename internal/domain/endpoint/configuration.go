package endpoint

import (
	"net/url"
	"strings"
)

// DisplayURL is a response projection only; it never rewrites persisted configuration.
func DisplayURL(raw string) string {
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

// URLHasMask also rejects invalid escaping, which cannot be a complete replacement.
func URLHasMask(raw string) bool {
	decoded, err := url.QueryUnescape(raw)
	return err != nil || strings.Contains(decoded, "***")
}
