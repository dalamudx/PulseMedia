package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (c *Client) GetTVTranslation(ctx context.Context, id int64, language string) (*ports.TVTranslation, error) {
	if id <= 0 || language != "zh-CN" {
		return nil, ports.ErrRejected
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fields, err := c.tvdbEvidenceRead(bounded, fmt.Sprintf("%s/tv/%d/translations", c.baseURL, id))
	if err != nil {
		return nil, err
	}
	actual, ok := tvdbEvidenceInteger(fields["id"])
	if !ok || actual != id {
		return nil, ports.ErrBadResponse
	}
	var translations []json.RawMessage
	if json.Unmarshal(fields["translations"], &translations) != nil || translations == nil || len(translations) > 512 {
		return nil, ports.ErrBadResponse
	}
	result := &ports.TVTranslation{TMDBID: id, Language: language}
	seen := map[string]bool{}
	for _, raw := range translations {
		row, e := tvdbEvidenceObject(raw)
		if e != nil {
			return nil, e
		}
		var iso, country *string
		if json.Unmarshal(row["iso_639_1"], &iso) != nil || iso == nil || len(*iso) != 2 || strings.Trim(*iso, "abcdefghijklmnopqrstuvwxyz") != "" || json.Unmarshal(row["iso_3166_1"], &country) != nil || country == nil || (len(*country) != 0 && len(*country) != 2) || strings.Trim(*country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return nil, ports.ErrBadResponse
		}
		key := *iso + "-" + *country
		if seen[key] {
			return nil, ports.ErrBadResponse
		}
		seen[key] = true
		data, e := tvdbEvidenceObject(row["data"])
		if e != nil {
			return nil, e
		}
		var title, overview *string
		if json.Unmarshal(data["name"], &title) != nil || title == nil || len(*title) > 1024 || strings.ContainsRune(*title, 0) || json.Unmarshal(data["overview"], &overview) != nil || overview == nil || len(*overview) > 65536 || strings.ContainsRune(*overview, 0) {
			return nil, ports.ErrBadResponse
		}
		if key == language {
			result.Title, result.Overview = *title, *overview
		}
	}
	if err := bounded.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
