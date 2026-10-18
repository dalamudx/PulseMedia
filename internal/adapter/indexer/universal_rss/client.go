package universal_rss

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.IndexerClient = (*Client)(nil)

// PublicSiteRule defines the declarative search template for a known public BT / anime tracker.
type PublicSiteRule struct {
	Domain    string `json:"domain"`
	Name      string `json:"name"`
	SearchURL string `json:"search_url"` // Template using {query}
}

// Built-in declarative presets for popular public anime & BT trackers
var DefaultPublicRules = map[string]PublicSiteRule{
	"mikanani.me": {
		Domain:    "mikanani.me",
		Name:      "蜜柑计划 (Mikan)",
		SearchURL: "https://mikanani.me/RSS/Search?searchstr={query}",
	},
	"bangumi.moe": {
		Domain:    "bangumi.moe",
		Name:      "萌番组 (Bangumi.moe)",
		SearchURL: "https://bangumi.moe/rss/tags/{query}",
	},
	"dmhy.org": {
		Domain:    "dmhy.org",
		Name:      "动漫花园 (DMHY)",
		SearchURL: "https://share.dmhy.org/topics/rss/rss.xml?keyword={query}",
	},
	"nyaa.si": {
		Domain:    "nyaa.si",
		Name:      "Nyaa",
		SearchURL: "https://nyaa.si/?page=rss&q={query}",
	},
	"acg.rip": {
		Domain:    "acg.rip",
		Name:      "ACG.RIP",
		SearchURL: "https://acg.rip/page/1.xml?term={query}",
	},
	"tokyotosho.info": {
		Domain:    "tokyotosho.info",
		Name:      "Tokyo Toshokan",
		SearchURL: "https://www.tokyotosho.info/rss.php?terms={query}",
	},
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	feedOnly   bool
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		httpClient: &clone,
		baseURL:    strings.TrimSpace(baseURL),
	}
}

// NewFeedClient reads the exact configured feed URL; no domain/template rewriting.
func NewFeedClient(feedURL string, httpClient *http.Client) *Client {
	c := NewClient(feedURL, httpClient)
	c.feedOnly = true
	return c
}

// XML Data Structures compatible with RSS 2.0 and custom enclosures
type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Enclosure   struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	TorrentCustom struct {
		Link          string `xml:"link"`
		ContentLength int64  `xml:"contentLength"`
		PubDate       string `xml:"pubDate"`
	} `xml:"torrent"`
	Guid string `xml:"guid"`
}

var btihRegex = regexp.MustCompile(`(?i)urn:btih:([a-zA-Z0-9]+)`)

// BuildSearchURL resolves the target search URL using either a template placeholder, domain rule, or default fallback.
func (c *Client) BuildSearchURL(query string) string {
	escapedQuery := url.QueryEscape(query)

	// 1. If user entered a custom template with {query} or {keyword}
	if strings.Contains(c.baseURL, "{query}") {
		return strings.ReplaceAll(c.baseURL, "{query}", escapedQuery)
	}
	if strings.Contains(c.baseURL, "{keyword}") {
		return strings.ReplaceAll(c.baseURL, "{keyword}", escapedQuery)
	}

	// 2. Extract domain and check built-in declarative rules
	parsed, err := url.Parse(c.baseURL)
	if err == nil && parsed.Host != "" {
		host := strings.ToLower(parsed.Host)
		host = strings.TrimPrefix(host, "www.")
		host = strings.TrimPrefix(host, "share.")

		for ruleDomain, rule := range DefaultPublicRules {
			if strings.Contains(host, ruleDomain) || strings.Contains(ruleDomain, host) {
				return strings.ReplaceAll(rule.SearchURL, "{query}", escapedQuery)
			}
		}
	}

	// 3. Fallback standard endpoint
	cleanBase := strings.TrimRight(c.baseURL, "/")
	return fmt.Sprintf("%s/rss?q=%s", cleanBase, escapedQuery)
}

// Search queries any public BT tracker via universal RSS without needing authentication.
func (c *Client) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	reqURL := c.BuildSearchURL(query)
	if c.feedOnly {
		reqURL = c.baseURL
	}
	if err := ctx.Err(); err != nil {
		return nil, requestError(ctx, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/rss+xml,application/xml,text/xml,*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		delay := time.Minute
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n > 0 {
			if n > 86400 {
				n = 86400
			}
			delay = time.Duration(n) * time.Second
		} else if t, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil && time.Until(t) > 0 {
			delay = time.Until(t)
		}
		return nil, ports.NewRateLimitError(delay, "RSS rate limited")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ports.ErrAuthRequired
	}
	if resp.StatusCode >= 500 {
		return nil, ports.ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ports.ErrBadResponse
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, (5<<20)+1))
	if err != nil {
		return nil, requestError(ctx, err)
	}

	if len(bodyBytes) > 5<<20 {
		return nil, ports.ErrBadResponse
	}
	var root rssRoot
	if err := xml.Unmarshal(bodyBytes, &root); err != nil {
		return nil, ports.ErrBadResponse
	}

	results := make([]*ports.ReleaseItem, 0, len(root.Channel.Items))
	for _, it := range root.Channel.Items {
		dlURL := it.Enclosure.URL
		if dlURL == "" {
			dlURL = it.TorrentCustom.Link
		}
		if dlURL == "" {
			dlURL = it.Link
		}

		infoHash := ""
		if m := btihRegex.FindStringSubmatch(dlURL); len(m) >= 2 {
			infoHash = strings.ToLower(m[1])
		}

		guid := it.Guid
		if guid == "" {
			guid = it.Link
		}

		size := it.Enclosure.Length
		if size <= 1 && it.TorrentCustom.ContentLength > 0 {
			size = it.TorrentCustom.ContentLength
		}

		var pubTime *time.Time
		if it.PubDate != "" {
			if t, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
				pubTime = &t
			} else if t, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
				pubTime = &t
			} else if t, err := time.Parse(time.RFC3339, it.PubDate); err == nil {
				pubTime = &t
			}
		}

		results = append(results, &ports.ReleaseItem{
			Title:       strings.TrimSpace(it.Title),
			GUID:        guid,
			DownloadURL: dlURL,
			InfoHash:    infoHash,
			SizeBytes:   size,
			PublishDate: pubTime,
		})
	}

	return results, nil
}

// FetchTorrent fetches the raw torrent or returns the magnet link bytes.
func (c *Client) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, requestError(ctx, err)
	}
	if strings.HasPrefix(downloadURL, "magnet:") {
		return []byte(downloadURL), nil
	}
	return torznab.NewClient(c.baseURL, "", c.httpClient).FetchTorrent(ctx, downloadURL)
}

func requestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
