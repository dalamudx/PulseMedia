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

var _ ports.PersonCreditsProvider = (*Client)(nil)

// GetPersonCredits reads TMDB's complete combined credits for one exact person.
// The caller must obtain canonical details and execution authorization separately.
func (c *Client) GetPersonCredits(ctx context.Context, id int64, role string) (ports.PersonCredits, error) {
	var zero ports.PersonCredits
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if c == nil || c.httpClient == nil || c.budget == nil || id <= 0 || (role != "actor" && role != "director") {
		return zero, ports.ErrRejected
	}
	if c.apiKey == "" {
		return zero, ports.ErrAuthRequired
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, _, err := c.doGet(bounded, fmt.Sprintf("%s/person/%d/combined_credits?language=zh-CN", c.baseURL, id), "")
	if errors.Is(ctx.Err(), context.Canceled) {
		return zero, ctx.Err()
	}
	if errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return zero, ports.ErrTimeout
	}
	if errors.Is(err, errNotModified) {
		return zero, ports.ErrBadResponse
	}
	if err != nil {
		return zero, err
	}
	return decodePersonCredits(raw, id, role)
}

func decodePersonCredits(raw []byte, id int64, role string) (ports.PersonCredits, error) {
	var zero ports.PersonCredits
	fields, err := personCreditObject(raw)
	if err != nil {
		return zero, err
	}
	var actualID int64
	if json.Unmarshal(fields["id"], &actualID) != nil || actualID != id || id <= 0 {
		return zero, ports.ErrBadResponse
	}
	for _, key := range []string{"page", "total_pages", "next", "links"} {
		if _, exists := fields[key]; exists {
			return zero, ports.ErrBadResponse
		}
	}
	result := ports.PersonCredits{PersonID: id, Role: role, Works: []ports.PersonWork{}}
	seen := map[string]ports.PersonWork{}
	total := 0
	for _, group := range []string{"cast", "crew"} {
		var rows []json.RawMessage
		if json.Unmarshal(fields[group], &rows) != nil || rows == nil {
			return zero, ports.ErrBadResponse
		}
		total += len(rows)
		if total > 10000 {
			return zero, ports.ErrBadResponse
		}
		for _, row := range rows {
			_, err := personCreditObject(row)
			if err != nil {
				return zero, err
			}
			var work struct {
				ID            int64  `json:"id"`
				MediaType     string `json:"media_type"`
				Title         string `json:"title"`
				OriginalTitle string `json:"original_title"`
				Name          string `json:"name"`
				OriginalName  string `json:"original_name"`
				ReleaseDate   string `json:"release_date"`
				FirstAirDate  string `json:"first_air_date"`
				Job           string `json:"job"`
				Department    string `json:"department"`
			}
			if json.Unmarshal(row, &work) != nil || work.ID <= 0 || (work.MediaType != "movie" && work.MediaType != "tv") {
				return zero, ports.ErrBadResponse
			}
			selected := role == "actor" && group == "cast"
			if group == "crew" {
				if strings.TrimSpace(work.Job) == "" || strings.TrimSpace(work.Department) == "" {
					return zero, ports.ErrBadResponse
				}
				if work.Job == "Director" && work.Department != "Directing" {
					return zero, ports.ErrBadResponse
				}
				selected = role == "director" && work.Job == "Director" && work.Department == "Directing"
			}
			if !selected {
				continue
			}
			item := ports.PersonWork{TMDBID: work.ID, MediaType: work.MediaType, Title: work.Title, OriginalTitle: work.OriginalTitle, ReleaseDate: work.ReleaseDate}
			if work.MediaType == "tv" {
				item.Title, item.OriginalTitle, item.ReleaseDate = work.Name, work.OriginalName, work.FirstAirDate
			}
			if strings.TrimSpace(item.Title) == "" || len(item.Title) > 1024 || len(item.OriginalTitle) > 1024 {
				return zero, ports.ErrBadResponse
			}
			if _, valid := catalogDateYear(item.ReleaseDate); !valid {
				return zero, ports.ErrBadResponse
			}
			key := fmt.Sprintf("%s:%d", item.MediaType, item.TMDBID)
			if previous, exists := seen[key]; exists {
				if previous != item {
					return zero, ports.ErrBadResponse
				}
				continue
			}
			seen[key] = item
			result.Works = append(result.Works, item)
		}
	}
	return result, nil
}

func personCreditObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > 5<<20 || !utf8.Valid(raw) {
		return nil, ports.ErrBadResponse
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ports.ErrBadResponse
	}
	fields := map[string]json.RawMessage{}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || key != strings.ToLower(key) || seen[key] {
			return nil, ports.ErrBadResponse
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ports.ErrBadResponse
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, ports.ErrBadResponse
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ports.ErrBadResponse
	}
	return fields, nil
}
