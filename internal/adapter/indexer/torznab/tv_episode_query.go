package torznab

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.TVEpisodePageClient = (*Client)(nil)

// SearchTVEpisodePage issues exactly one native TV request. The caller separately
// checks capabilities and reserves the budget and physical permit for each call.
func (c *Client) SearchTVEpisodePage(ctx context.Context, query ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
	if query.SchemaVersion != "tv-episode-query-v1" || query.Mode != "tvsearch" || query.SeriesNamespace != "tmdb.tv" || query.SeriesID <= 0 ||
		query.Keyword == "" || len(query.Keyword) > 512 || !utf8.ValidString(query.Keyword) || strings.TrimSpace(query.Keyword) != query.Keyword || strings.ContainsFunc(query.Keyword, unicode.IsControl) ||
		query.Season < 1 || query.Season > 999 || query.Episode < 1 || query.Episode > 9999 || query.Categories == nil || len(query.Categories) > 100 ||
		query.Offset < 0 || query.Offset > 150 || query.Offset%50 != 0 || query.Limit != 50 {
		return nil, ports.ErrBadResponse
	}
	categories := append([]int32(nil), query.Categories...)
	slices.Sort(categories)
	catValues := make([]string, len(categories))
	for i, category := range categories {
		if category <= 0 || (i > 0 && category == categories[i-1]) {
			return nil, ports.ErrBadResponse
		}
		catValues[i] = strconv.FormatInt(int64(category), 10)
	}
	params := url.Values{
		"t": {"tvsearch"}, "q": {query.Keyword}, "season": {strconv.FormatInt(int64(query.Season), 10)},
		"ep": {strconv.FormatInt(int64(query.Episode), 10)}, "cat": {strings.Join(catValues, ",")},
		"offset": {strconv.FormatInt(int64(query.Offset), 10)}, "limit": {strconv.FormatInt(int64(query.Limit), 10)}, "apikey": {c.apiKey},
	}
	requestURL, err := c.apiURL(params)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, requestURL, 5<<20)
	if err != nil {
		return nil, err
	}
	if err := apiError(body); err != nil {
		return nil, err
	}
	items, proof, err := searchPageItems(body, query.Offset, query.Limit)
	if err != nil {
		return nil, err
	}
	proof.Items = releaseItems(items)
	if err := ctx.Err(); err != nil {
		return nil, transportError(ctx, err)
	}
	return proof, nil
}

// releaseItems preserves the common RSS conversion for movie and TV pages.
func releaseItems(items []rssItem) []*ports.ReleaseItem {
	releases := make([]*ports.ReleaseItem, 0, len(items))
	for _, item := range items {
		rel := &ports.ReleaseItem{
			Title: strings.TrimSpace(item.Title),
			GUID:  strings.TrimSpace(item.GUID),
		}

		// Download URL preference: enclosure url > link
		if item.Enclosure != nil && item.Enclosure.URL != "" {
			rel.DownloadURL = item.Enclosure.URL
			if item.Enclosure.Length > 0 {
				rel.SizeBytes = item.Enclosure.Length
			}
		} else if item.Link != "" {
			rel.DownloadURL = item.Link
		}

		if rel.SizeBytes == 0 && item.Size > 0 {
			rel.SizeBytes = item.Size
		}

		if item.PubDate != "" {
			if t, err := time.Parse(time.RFC1123Z, item.PubDate); err == nil {
				rel.PublishDate = &t
			} else if t, err := time.Parse(time.RFC1123, item.PubDate); err == nil {
				rel.PublishDate = &t
			}
		}

		// Parse Torznab attributes
		for _, attr := range item.Attrs {
			switch strings.ToLower(attr.Name) {
			case "infohash":
				rel.InfoHash = strings.ToLower(strings.TrimSpace(attr.Value))
			case "seeders":
				if s, err := strconv.Atoi(attr.Value); err == nil {
					rel.Seeders = int32(s)
				}
			case "peers", "leechers":
				if l, err := strconv.Atoi(attr.Value); err == nil {
					rel.Leechers = int32(l)
				}
			case "size":
				if sz, err := strconv.ParseInt(attr.Value, 10, 64); err == nil && sz > 0 {
					rel.SizeBytes = sz
				}
			case "category":
				if cat, err := strconv.Atoi(attr.Value); err == nil {
					rel.Categories = append(rel.Categories, int32(cat))
				}
			}
		}

		releases = append(releases, rel)
	}

	return releases
}
