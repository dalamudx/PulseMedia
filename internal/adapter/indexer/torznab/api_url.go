package torznab

import (
	"net/url"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// apiURL supports both a configured source root and a complete /api endpoint.
// Configured query parameters survive adding protocol fields. Userinfo was
// detached by NewClient; get attaches it only after the original origin check.
func (c *Client) apiURL(params url.Values) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", ports.ErrBadResponse
	}
	path := strings.TrimRight(u.Path, "/")
	rawPath := strings.TrimRight(u.RawPath, "/")
	if !strings.HasSuffix(path, "/api") {
		path += "/api"
		if rawPath != "" {
			rawPath += "/api"
		}
	}
	u.Path, u.RawPath = path, rawPath
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ports.ErrBadResponse
	}
	for name, values := range params {
		if name == "apikey" && c.apiKey == "" && q.Has(name) {
			continue
		}
		q.Del(name)
		for _, value := range values {
			q.Add(name, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
