package httpclient

import (
	"net/url"
	"strings"
)

// AllowsURL keeps requests within the administrator-configured HTTP origin.
// Explicit private and loopback origins remain valid; a provider response cannot
// authorize another host, scheme, port or URL userinfo.
func AllowsURL(baseURL, requestURL string) bool {
	base, err := url.Parse(baseURL)
	if err != nil || !validURL(base) {
		return false
	}
	request, err := url.Parse(requestURL)
	if err != nil || !validURL(request) {
		return false
	}
	return base.Scheme == request.Scheme && strings.EqualFold(base.Hostname(), request.Hostname()) && port(base) == port(request)
}

func validURL(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func port(u *url.URL) string {
	if value := u.Port(); value != "" {
		return value
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
