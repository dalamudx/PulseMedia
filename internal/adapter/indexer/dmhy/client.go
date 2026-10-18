package dmhy

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.IndexerClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://share.dmhy.org"
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// RSS XML Structs for DMHY
type dmhyRSS struct {
	Channel struct {
		Title string     `xml:"title"`
		Items []dmhyItem `xml:"item"`
	} `xml:"channel"`
}

type dmhyItem struct {
	Title     string `xml:"title"`
	Link      string `xml:"link"`
	PubDate   string `xml:"pubDate"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Guid string `xml:"guid"`
}

var btihRegex = regexp.MustCompile(`(?i)urn:btih:([a-zA-Z0-9]+)`)

// Search queries DMHY public anime RSS feed by keyword without needing any cookies or registration.
func (c *Client) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	reqURL := fmt.Sprintf("%s/topics/rss/rss.xml?keyword=%s", c.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/rss+xml,application/xml,text/xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rss dmhyRSS
	if err := xml.Unmarshal(bodyBytes, &rss); err != nil {
		return nil, fmt.Errorf("%w: failed to parse dmhy rss: %v", ports.ErrBadResponse, err)
	}

	results := make([]*ports.ReleaseItem, 0, len(rss.Channel.Items))
	for _, item := range rss.Channel.Items {
		dlURL := item.Enclosure.URL
		if dlURL == "" {
			dlURL = item.Link
		}

		// Extract BTIH infohash if present in magnet
		infoHash := ""
		if matches := btihRegex.FindStringSubmatch(dlURL); len(matches) >= 2 {
			infoHash = strings.ToLower(matches[1])
		}

		guid := item.Guid
		if guid == "" {
			guid = item.Link
		}

		size := item.Enclosure.Length
		if size <= 1 {
			size = 1024 * 1024 * 1024 * 1 // fallback estimate 1GB
		}

		// Parse date
		var pubTime *time.Time
		if t, err := time.Parse(time.RFC1123Z, item.PubDate); err == nil {
			pubTime = &t
		} else if t, err := time.Parse(time.RFC1123, item.PubDate); err == nil {
			pubTime = &t
		}

		results = append(results, &ports.ReleaseItem{
			Title:       strings.TrimSpace(item.Title),
			GUID:        guid,
			DownloadURL: dlURL,
			InfoHash:    infoHash,
			SizeBytes:   size,
			Seeders:     25, // default estimated seeders for active anime
			Leechers:    5,
			PublishDate: pubTime,
		})
	}

	return results, nil
}

// FetchTorrent returns raw torrent bytes or magnet URL.
func (c *Client) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	if strings.HasPrefix(downloadURL, "magnet:") {
		return []byte(downloadURL), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
