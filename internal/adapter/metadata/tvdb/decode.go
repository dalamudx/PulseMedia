package tvdb

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func text(value string, max int) bool {
	if len(value) > max {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) && ch != '\n' && ch != '\r' && ch != '\t' {
			return false
		}
	}
	return true
}
func decodeSeries(data []byte, id int64) (*ports.TVDBSeriesDetails, error) {
	var raw struct {
		ID               int64  `json:"id"`
		Name             string `json:"name"`
		Year             string `json:"year"`
		Overview         string `json:"overview"`
		OriginalLanguage string `json:"originalLanguage"`
		Seasons          *[]struct {
			ID     int64  `json:"id"`
			Number *int   `json:"number"`
			Name   string `json:"name"`
			Type   struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"type"`
		} `json:"seasons"`
		RemoteIDs []ports.TVDBRemoteIdentity `json:"remoteIds"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.ID != id || strings.TrimSpace(raw.Name) == "" || !text(raw.Name, 1024) || !text(raw.Overview, 65536) || !text(raw.OriginalLanguage, 16) {
		return nil, ports.ErrBadResponse
	}
	year := 0
	if raw.Year != "" {
		n, e := strconv.Atoi(raw.Year)
		if e != nil || n < 1 || n > 9999 || len(raw.Year) != 4 {
			return nil, ports.ErrBadResponse
		}
		year = n
	}
	result := &ports.TVDBSeriesDetails{TVDBID: id, Title: raw.Name, Year: year, Overview: raw.Overview, OriginalLanguage: raw.OriginalLanguage, RemoteIDs: raw.RemoteIDs}
	remoteTypes := map[int64]bool{}
	for _, identity := range raw.RemoteIDs {
		if identity.Type <= 0 || remoteTypes[identity.Type] || strings.TrimSpace(identity.ID) == "" || !text(identity.ID, 256) || !text(identity.SourceName, 256) {
			return nil, ports.ErrBadResponse
		}
		remoteTypes[identity.Type] = true
	}
	if raw.Seasons != nil {
		if len(*raw.Seasons) > 1000 {
			return nil, ports.ErrBadResponse
		}
		result.Seasons = []ports.TVDBSeasonDetails{}
		ids, numbers := map[int64]bool{}, map[string]bool{}
		for _, season := range *raw.Seasons {
			if season.ID <= 0 || ids[season.ID] || season.Number == nil || *season.Number < 0 || *season.Number > 1000 || season.Type.ID <= 0 || season.Type.Type == "" || !text(season.Type.Type, 64) || !text(season.Name, 1024) {
				return nil, ports.ErrBadResponse
			}
			key := strconv.FormatInt(season.Type.ID, 10) + ":" + strconv.Itoa(*season.Number)
			if numbers[key] {
				return nil, ports.ErrBadResponse
			}
			ids[season.ID], numbers[key] = true, true
			result.Seasons = append(result.Seasons, ports.TVDBSeasonDetails{TVDBID: season.ID, SeasonNumber: *season.Number, Name: season.Name, TypeID: season.Type.ID, Numbering: season.Type.Type})
		}
	}
	return result, nil
}

func decodePage(data *envelope, id int64, season, page int, path string) ([]ports.TVDBEpisode, bool, *int, error) {
	var raw struct {
		Series struct {
			ID int64 `json:"id"`
		} `json:"series"`
		Episodes *[]struct {
			ID       int64  `json:"id"`
			SeriesID int64  `json:"seriesId"`
			Number   *int   `json:"number"`
			Season   *int   `json:"seasonNumber"`
			Name     string `json:"name"`
			Aired    string `json:"aired"`
			Absolute *int   `json:"absoluteNumber"`
		} `json:"episodes"`
	}
	if json.Unmarshal(data.Data, &raw) != nil || raw.Series.ID != id || raw.Episodes == nil || len(*raw.Episodes) > episodeLimit {
		return nil, false, nil, ports.ErrBadResponse
	}
	var links map[string]json.RawMessage
	if json.Unmarshal(data.Links, &links) != nil || links == nil || links["next"] == nil {
		return nil, false, nil, ports.ErrBadResponse
	}
	var next *string
	if json.Unmarshal(links["next"], &next) != nil {
		return nil, false, nil, ports.ErrBadResponse
	}
	more := next != nil && *next != ""
	if more && !pageLink(*next, path, season, page+1) {
		return nil, false, nil, ports.ErrBadResponse
	}
	if self, exists := links["self"]; exists {
		var value *string
		if json.Unmarshal(self, &value) != nil || (value != nil && !pageLink(*value, path, season, page)) {
			return nil, false, nil, ports.ErrBadResponse
		}
	}
	var total *int
	if value, exists := links["total_items"]; exists {
		if json.Unmarshal(value, &total) != nil || (total != nil && (*total < 0 || *total > episodeLimit)) {
			return nil, false, nil, ports.ErrBadResponse
		}
	}
	result := make([]ports.TVDBEpisode, 0, len(*raw.Episodes))
	for _, ep := range *raw.Episodes {
		if ep.ID <= 0 || ep.SeriesID != id || ep.Number == nil || *ep.Number <= 0 || *ep.Number > 10000 || ep.Season == nil || *ep.Season != season || !text(ep.Name, 1024) || (ep.Absolute != nil && (*ep.Absolute < 0 || *ep.Absolute > 100000)) {
			return nil, false, nil, ports.ErrBadResponse
		}
		value := ports.TVDBEpisode{TVDBID: ep.ID, SeasonNumber: season, EpisodeNumber: *ep.Number, Title: ep.Name}
		if ep.Absolute != nil && *ep.Absolute > 0 {
			value.AbsoluteNumber = ep.Absolute
		}
		if ep.Aired != "" {
			date, e := time.Parse("2006-01-02", ep.Aired)
			if e != nil || date.Year() < 1 || date.Format("2006-01-02") != ep.Aired {
				return nil, false, nil, ports.ErrBadResponse
			}
			value.AirDate = &date
		}
		result = append(result, value)
	}
	return result, more, total, nil
}
func pageLink(raw, path string, season, page int) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || u.Path != path || u.ForceQuery {
		return false
	}
	if u.IsAbs() {
		if u.Scheme != "https" || u.Host != "api4.thetvdb.com" {
			return false
		}
	} else if u.Host != "" || !strings.HasPrefix(raw, "/") {
		return false
	}
	query, e := url.ParseQuery(u.RawQuery)
	return e == nil && len(query) == 2 && len(query["page"]) == 1 && len(query["season"]) == 1 && query.Get("page") == strconv.Itoa(page) && query.Get("season") == strconv.Itoa(season)
}
