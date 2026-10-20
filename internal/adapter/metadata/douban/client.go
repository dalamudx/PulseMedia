package douban

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is the signature scheme the Douban API requires.
	"encoding/base64"
	"encoding/json"
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
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	defaultBaseURL = "https://frodo.douban.com/api/v2"
	userAgent      = "api-client/1 com.douban.frodo/7.18.0(230) Android/22 product/MI 9 vendor/Xiaomi model/MI 9 brand/Android  rom/miui6  network/wifi  platform/mobile nd/1"
	maxBodyBytes   = 2 << 20
	maxItems       = 100
	maxCount       = 50
	maxTextRunes   = 200
)

var (
	idPattern     = regexp.MustCompile(`^[0-9]{1,20}$`)
	posterHostRe  = regexp.MustCompile(`^img[0-9]*\.doubanio\.com$`)
	errBadPayload = fmt.Errorf("%w: invalid douban payload", ports.ErrBadResponse)

	// Douban serves these generic images when a title has no real poster.
	placeholderPosters = []string{"movie_large.jpg", "tv_normal.png", "tv_normal.jpg", "tv_large.jpg"}
)

// Credentials are the API key and signing secret. They are runtime
// configuration and are never logged or placed in returned errors.
type Credentials struct {
	APIKey string
	Secret string
}

// CredentialSource returns the current credentials, empty when unconfigured.
type CredentialSource func(ctx context.Context) Credentials

var _ ports.DoubanRankingProvider = (*Client)(nil)

type Client struct {
	httpClient  *http.Client
	baseURL     string
	credentials CredentialSource
	clock       ports.Clock
}

// Option configures a Client.
type Option func(*Client)

// WithCredentials fixes the credentials for the client's lifetime.
func WithCredentials(c Credentials) Option {
	return func(cl *Client) { cl.credentials = func(context.Context) Credentials { return c } }
}

// WithCredentialSource reads credentials on every request so a configuration
// change takes effect without rebuilding the client.
func WithCredentialSource(source CredentialSource) Option {
	return func(cl *Client) { cl.credentials = source }
}

// WithClock sets the clock used for the signed request date.
func WithClock(clock ports.Clock) Option {
	return func(cl *Client) { cl.clock = clock }
}

// NewClient copies the supplied client so redirects and cookies can be
// disabled without mutating the caller's transport policy.
func NewClient(baseURL string, httpClient *http.Client, options ...Option) *Client {
	hc := http.Client{Timeout: 10 * time.Second}
	if httpClient != nil {
		hc = *httpClient
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	c := &Client{httpClient: &hc, baseURL: strings.TrimRight(baseURL, "/")}
	for _, option := range options {
		option(c)
	}
	return c
}

// collections maps each ranking to its Douban collection and media type.
var collections = map[string]struct{ name, kind string }{
	ports.DoubanListHotMovie:      {"movie_hot_gaia", "movie"},
	ports.DoubanListHotTV:         {"tv_hot", "tv"},
	ports.DoubanListWeeklyChinese: {"tv_chinese_best_weekly", "tv"},
	ports.DoubanListWeeklyGlobal:  {"tv_global_best_weekly", "tv"},
	ports.DoubanListTop250:        {"movie_top250", "movie"},
}

// GetRanking reads one explicit window of a ranking. At most count validated
// items are returned.
func (c *Client) GetRanking(ctx context.Context, list string, start, count int) ([]ports.DoubanRankingItem, error) {
	collection, ok := collections[list]
	if !ok || start < 0 || count < 1 || count > maxCount {
		return nil, ports.ErrRejected
	}
	var creds Credentials
	if c.credentials != nil {
		creds = c.credentials(ctx)
	}
	if creds.APIKey == "" || creds.Secret == "" {
		return nil, ports.ErrUnavailable
	}
	now := time.Now()
	if c.clock != nil {
		now = c.clock.Now()
	}
	reqURL, err := c.signedURL(collection.name, creds, now, start, count)
	if err != nil {
		return nil, ports.ErrRejected
	}
	items, err := c.fetch(ctx, reqURL, collection.kind)
	if err != nil {
		return nil, err
	}
	if len(items) > count {
		items = items[:count]
	}
	return items, nil
}

// signedURL builds the request URL. The signature covers the URL path and the
// date only, as the service defines it.
func (c *Client) signedURL(collection string, creds Credentials, now time.Time, start, count int) (string, error) {
	base, err := url.Parse(c.baseURL + "/subject_collection/" + collection + "/items")
	if err != nil {
		return "", err
	}
	ts := now.Format("20060102")
	mac := hmac.New(sha1.New, []byte(creds.Secret))
	_, _ = mac.Write([]byte("GET&" + url.QueryEscape(base.Path) + "&" + ts))
	query := url.Values{}
	query.Set("apiKey", creds.APIKey)
	query.Set("os_rom", "android")
	query.Set("_ts", ts)
	query.Set("_sig", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	query.Set("start", strconv.Itoa(start))
	query.Set("count", strconv.Itoa(count))
	base.RawQuery = query.Encode()
	return base.String(), nil
}

type rawItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          string `json:"year"`
	Type          string `json:"type"`
	Rating        struct {
		Value float64 `json:"value"`
	} `json:"rating"`
	Pic struct {
		Normal string `json:"normal"`
		Large  string `json:"large"`
	} `json:"pic"`
	CoverURL     string          `json:"cover_url"`
	Cover        json.RawMessage `json:"cover"`
	CardSubtitle string          `json:"card_subtitle"`
}

func (c *Client) fetch(ctx context.Context, reqURL, kind string) ([]ports.DoubanRankingItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, ports.ErrRejected
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// The transport error embeds the signed URL, so only sentinels leave.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, ports.ErrTimeout
		}
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ports.NewRateLimitError(5*time.Second, "douban rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if len(raw) > maxBodyBytes {
		return nil, errBadPayload
	}
	var body struct {
		Items *[]rawItem `json:"subject_collection_items"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Items == nil || len(*body.Items) > maxItems {
		return nil, errBadPayload
	}
	seen := make(map[string]bool, len(*body.Items))
	items := make([]ports.DoubanRankingItem, 0, len(*body.Items))
	for _, r := range *body.Items {
		item, err := convert(r, kind)
		if err != nil {
			return nil, err
		}
		if seen[item.DoubanID] {
			continue
		}
		seen[item.DoubanID] = true
		items = append(items, item)
	}
	return items, nil
}

func convert(r rawItem, kind string) (ports.DoubanRankingItem, error) {
	var zero ports.DoubanRankingItem
	title, original := strings.TrimSpace(r.Title), strings.TrimSpace(r.OriginalTitle)
	if !idPattern.MatchString(r.ID) || title == "" || utf8.RuneCountInString(title) > maxTextRunes || utf8.RuneCountInString(original) > maxTextRunes || (r.Type != "" && r.Type != kind) {
		return zero, errBadPayload
	}
	if v := r.Rating.Value; v < 0 || v > 10 {
		return zero, errBadPayload
	}
	year := 0
	if r.Year != "" {
		y, err := strconv.Atoi(r.Year)
		if err != nil || y < 1800 || y > 9999 {
			return zero, errBadPayload
		}
		year = y
	} else if y, ok := subtitleYear(r.CardSubtitle); ok {
		year = y
	}
	return ports.DoubanRankingItem{
		DoubanID: r.ID, Title: title, OriginalTitle: original, Year: year,
		Rating: r.Rating.Value, PosterURL: posterURL(r.Pic.Large, r.Pic.Normal, r.CoverURL, coverObjectURL(r.Cover)), MediaType: kind,
	}, nil
}

// posterURL keeps only real HTTPS images on Douban's image hosts; anything
// else is dropped rather than rendered or fetched later.
func posterURL(candidates ...string) string {
next:
	for _, raw := range candidates {
		u, err := url.Parse(raw)
		if raw == "" || err != nil || u.Scheme != "https" || u.User != nil || !posterHostRe.MatchString(u.Hostname()) || u.Port() != "" || u.Fragment != "" {
			continue
		}
		for _, marker := range placeholderPosters {
			if strings.Contains(u.Path, marker) {
				continue next
			}
		}
		return raw
	}
	return ""
}

var subtitleYearPattern = regexp.MustCompile(`^([0-9]{4})(?: / |$)`)

// subtitleYear reads the release year some collections only expose as the
// leading field of card_subtitle ("1994 / 美国 / 剧情 ..."). Anything else is
// left unknown rather than guessed.
func subtitleYear(subtitle string) (int, bool) {
	m := subtitleYearPattern.FindStringSubmatch(strings.TrimSpace(subtitle))
	if m == nil {
		return 0, false
	}
	y, err := strconv.Atoi(m[1])
	return y, err == nil && y >= 1800
}

// coverObjectURL reads cover.url when a collection uses a cover object. A
// differently shaped cover is ignored instead of failing the whole list.
func coverObjectURL(raw json.RawMessage) string {
	var cover struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &cover) != nil {
		return ""
	}
	return cover.URL
}
