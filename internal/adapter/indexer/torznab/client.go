package torznab

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/httpclient"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.IndexerClient = (*Client)(nil)
var _ ports.IndexerPageClient = (*Client)(nil)

type Client struct {
	httpClient     *http.Client
	baseURL        string
	apiKey         string
	configuredUser *url.Userinfo
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var configuredUser *url.Userinfo
	if configured, err := url.Parse(baseURL); err == nil {
		configuredUser = configured.User
		configured.User = nil
		baseURL = configured.String()
	}
	return &Client{
		httpClient:     &clone,
		baseURL:        baseURL,
		configuredUser: configuredUser,
		apiKey:         apiKey,
	}
}

type rssResponse struct {
	XMLName  xml.Name     `xml:"rss"`
	Channels []rssChannel `xml:"channel"`
}
type rssChannel struct {
	Items     []rssItem   `xml:"item"`
	Responses []rssWindow `xml:"response"`
}
type rssWindow struct {
	Offset string  `xml:"offset,attr"`
	Total  *string `xml:"total,attr"`
}

type rssItem struct {
	Title     string        `xml:"title"`
	GUID      string        `xml:"guid"`
	Link      string        `xml:"link"`
	Enclosure *rssEnclosure `xml:"enclosure"`
	PubDate   string        `xml:"pubDate"`
	Size      int64         `xml:"size"`
	Attrs     []torznabAttr `xml:"attr"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (c *Client) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	page, err := c.SearchPage(ctx, query, categories, offset, limit)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (c *Client) SearchPage(ctx context.Context, query string, categories []int32, offset, limit int32) (*ports.IndexerPage, error) {
	if offset < 0 || limit < 0 {
		return nil, ports.ErrBadResponse
	}
	params := url.Values{}
	params.Set("t", "movie")
	params.Set("q", query)
	params.Set("apikey", c.apiKey)
	if offset > 0 {
		params.Set("offset", strconv.Itoa(int(offset)))
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(int(limit)))
	}
	if len(categories) > 0 {
		catStrs := make([]string, len(categories))
		for i, cat := range categories {
			catStrs[i] = strconv.Itoa(int(cat))
		}
		params.Set("cat", strings.Join(catStrs, ","))
	}

	reqURL, err := c.apiURL(params)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, reqURL, 5<<20)
	if err != nil {
		return nil, err
	}
	if err := apiError(body); err != nil {
		return nil, err
	}
	items, proof, err := searchPageItems(body, offset, limit)
	if err != nil {
		return nil, err
	}
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

	proof.Items = releases
	return proof, nil
}

func (c *Client) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	return c.get(ctx, downloadURL, 10<<20)
}

// get returns safe port errors without request URLs or provider bodies.
func (c *Client) get(ctx context.Context, requestURL string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, transportError(ctx, err)
	}
	if !httpclient.AllowsURL(c.baseURL, requestURL) {
		return nil, ports.ErrBadResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	if c.configuredUser != nil {
		authValue, _ := c.configuredUser.Password()
		req.SetBasicAuth(c.configuredUser.Username(), authValue)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err = responseError(resp); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if len(body) == 0 || int64(len(body)) > maxBytes {
		return nil, ports.ErrBadResponse
	}
	return body, nil
}
func transportError(ctx context.Context, err error) error {
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
func responseError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ports.ErrAuthRequired
	case resp.StatusCode == http.StatusNotFound:
		return ports.ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		retry := 10 * time.Second
		value := resp.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			if seconds > 86400 {
				seconds = 86400
			}
			retry = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(value); err == nil {
			if remaining := time.Until(date); remaining > 0 {
				retry = remaining
			}
		}
		if retry > 24*time.Hour {
			retry = 24 * time.Hour
		}
		return ports.NewRateLimitError(retry, "Torznab rate limit reached")
	case resp.StatusCode >= 500:
		return ports.ErrUnavailable
	default:
		return ports.ErrBadResponse
	}
}
