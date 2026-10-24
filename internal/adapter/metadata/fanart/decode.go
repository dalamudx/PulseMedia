package fanart

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func decodeArtworks(data []byte, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
	var zero ports.MediaArtworks
	if !utf8.Valid(data) || !uniqueJSON(data) {
		return zero, ports.ErrBadResponse
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return zero, ports.ErrBadResponse
	}
	identityField := "tmdb_id"
	fields := []string{"hdmovielogo", "hdmovieclearart", "moviebanner", "moviedisc"}
	if id.MediaType == "tv" {
		identityField = "thetvdb_id"
		fields = []string{"hdtvlogo", "hdclearart", "tvbanner", ""}
	}
	var actual string
	if json.Unmarshal(raw[identityField], &actual) != nil || actual != strconv.FormatInt(id.ID, 10) {
		return zero, ports.ErrBadResponse
	}
	result := ports.MediaArtworks{Identity: id}
	targets := []*string{&result.ClearLogo, &result.ClearArt, &result.Banner, &result.DiscArt}
	prefix, _ := identityPath(id)
	for i, field := range fields {
		if field == "" {
			continue
		}
		data, exists := raw[field]
		if !exists {
			continue
		}
		var rows []struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			Lang string `json:"lang"`
		}
		if json.Unmarshal(data, &rows) != nil || rows == nil || len(rows) > 512 {
			return zero, ports.ErrBadResponse
		}
		best := -1
		seen := map[string]bool{}
		for _, row := range rows {
			n, err := strconv.ParseInt(row.ID, 10, 64)
			if err != nil || n <= 0 || strconv.FormatInt(n, 10) != row.ID || seen[row.ID] || len(row.Lang) > 32 {
				return zero, ports.ErrBadResponse
			}
			seen[row.ID] = true
			u, err := url.Parse(row.URL)
			if err != nil || u.Scheme != "https" || u.Host != "assets.fanart.tv" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || !validImagePath(u.Path) || !strings.HasPrefix(u.Path, "/fanart/"+prefix+"/") {
				return zero, ports.ErrBadResponse
			}
			ext := path.Ext(u.Path)
			if (i != 2 && ext != ".png") || (i == 2 && ext != ".jpg" && ext != ".jpeg") {
				return zero, ports.ErrBadResponse
			}
			score := 0
			language := strings.ToLower(row.Lang)
			if language == "zh" || language == "cn" || strings.HasPrefix(language, "zh-") {
				score = 1
			}
			if score > best {
				*targets[i] = u.Path
				best = score
			}
		}
	}
	return result, nil
}

func validImagePath(p string) bool {
	if len(p) > 1024 || strings.ContainsAny(p, "\\%?#\x00\r\n") || path.Clean(p) != p {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "fanart" || (parts[2] != "movies" && parts[2] != "tv") || parts[4] == "" || parts[5] == "" {
		return false
	}
	n, e := strconv.ParseInt(parts[3], 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != parts[3] {
		return false
	}
	for _, s := range parts[4:] {
		for _, r := range s {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
				continue
			}
			return false
		}
	}
	switch path.Ext(p) {
	case ".png", ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

// Reject duplicate keys (including case aliases), trailing values and excessive nesting.
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !jsonValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
func jsonValue(d *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	t, e := d.Token()
	if e != nil {
		return false
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return false
			}
			s, ok := key.(string)
			if !ok {
				return false
			}
			s = strings.ToLower(s)
			if seen[s] {
				return false
			}
			seen[s] = true
			if !jsonValue(d, depth+1) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !jsonValue(d, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	end, e := d.Token()
	return e == nil && (delimiter == '{' && end == json.Delim('}') || delimiter == '[' && end == json.Delim(']'))
}
