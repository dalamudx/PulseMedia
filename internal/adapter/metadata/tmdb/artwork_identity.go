package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.TVArtworkIdentityResolver = (*Client)(nil)

// ResolveTVArtworkIdentity reads one explicit TV cross-reference without caching
// or changing canonical media identity, frozen metadata, or acquisition rights.
func (c *Client) ResolveTVArtworkIdentity(ctx context.Context, id int64) (ports.TVArtworkIdentity, error) {
	var zero ports.TVArtworkIdentity
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c == nil || c.httpClient == nil || id <= 0 {
		return zero, ports.ErrRejected
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, _, err := c.doGet(bounded, fmt.Sprintf("%s/tv/%d/external_ids", c.baseURL, id), "")
	if errors.Is(err, errNotModified) {
		return zero, ports.ErrBadResponse
	}
	if err != nil {
		return zero, err
	}
	if err := bounded.Err(); err != nil {
		return zero, err
	}
	return decodeTVArtworkIdentity(raw, id)
}

func decodeTVArtworkIdentity(raw []byte, id int64) (ports.TVArtworkIdentity, error) {
	var zero ports.TVArtworkIdentity
	if !utf8.Valid(raw) {
		return zero, ports.ErrBadResponse
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return zero, ports.ErrBadResponse
	}
	fields := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return zero, ports.ErrBadResponse
		}
		key, ok := token.(string)
		if !ok || seen[strings.ToLower(key)] {
			return zero, ports.ErrBadResponse
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return zero, ports.ErrBadResponse
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return zero, ports.ErrBadResponse
	}
	if _, err = d.Token(); err != io.EOF {
		return zero, ports.ErrBadResponse
	}
	var tmdbID, tvdbID int64
	if json.Unmarshal(fields["id"], &tmdbID) != nil || tmdbID != id {
		return zero, ports.ErrBadResponse
	}
	tvdb, exists := fields["tvdb_id"]
	if !exists {
		return zero, ports.ErrBadResponse
	}
	if string(tvdb) == "null" {
		return zero, ports.ErrNotFound
	}
	if json.Unmarshal(tvdb, &tvdbID) != nil || tvdbID <= 0 {
		return zero, ports.ErrBadResponse
	}
	return ports.TVArtworkIdentity{TMDBID: tmdbID, TVDBID: tvdbID}, nil
}
