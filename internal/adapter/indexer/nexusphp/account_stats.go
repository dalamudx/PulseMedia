package nexusphp

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

var _ ports.SiteAccountStatsClient = (*AccountStatsClient)(nil)

// AccountStatsClient supports the classic NexusPHP info_block profile only.
type AccountStatsClient struct {
	baseURL    string
	cookie     string
	httpClient *http.Client
	clock      ports.Clock
}

func NewAccountStatsClient(baseURL, cookie string, httpClient *http.Client, clock ports.Clock) *AccountStatsClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	clone := *httpClient
	clone.Jar = nil
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &AccountStatsClient{baseURL: baseURL, cookie: cookie, httpClient: &clone, clock: clock}
}

func (c *AccountStatsClient) ReadAccountStats(ctx context.Context, expectedID string) (ports.SiteAccountStats, error) {
	empty := ports.SiteAccountStats{}
	if err := ctx.Err(); err != nil {
		return empty, requestError(ctx, err)
	}
	if c.clock == nil {
		return empty, ports.ErrUnavailable
	}
	if strings.TrimSpace(c.cookie) == "" {
		return empty, ports.ErrAuthRequired
	}
	if !indexer.ValidSiteAccountID(expectedID) {
		return empty, ports.ErrRejected
	}
	target, ok := accountStatsURL(c.baseURL)
	if !ok {
		return empty, ports.ErrRejected
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return empty, ports.ErrRejected
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("User-Agent", "PulseMedia/1.0")
	req.Header.Set("Accept", "text/html")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return empty, requestError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := ctx.Err(); err != nil {
		return empty, requestError(ctx, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return empty, ports.ErrAuthRequired
	case resp.StatusCode == http.StatusForbidden:
		return empty, ports.ErrBlocked
	case resp.StatusCode == http.StatusTooManyRequests:
		return empty, ports.NewRateLimitError(accountRetryAfter(resp.Header.Get("Retry-After"), c.clock.Now()), "indexer rate limit reached")
	case resp.StatusCode >= 500 && resp.StatusCode <= 599:
		return empty, ports.ErrUnavailable
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		dest, valid := accountLink(target, resp.Header.Get("Location"))
		if valid && dest.Path == path.Join(path.Dir(target.Path), "login.php") {
			return empty, ports.ErrAuthRequired
		}
		return empty, ports.ErrBadResponse
	case resp.StatusCode != http.StatusOK:
		return empty, ports.ErrBadResponse
	}
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "text/html" {
		return empty, ports.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return empty, requestError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return empty, requestError(ctx, err)
	}
	if len(raw) == 0 || len(raw) > 2<<20 || bytes.IndexByte(raw, 0) >= 0 {
		return empty, ports.ErrBadResponse
	}
	decoded := raw
	encoding := strings.ToLower(params["charset"])
	if encoding != "" && encoding != "utf-8" && encoding != "utf8" {
		if _, name := charset.Lookup(encoding); name == "" {
			return empty, ports.ErrBadResponse
		}
		reader, e := charset.NewReaderLabel(encoding, bytes.NewReader(raw))
		if e != nil {
			return empty, ports.ErrBadResponse
		}
		decoded, e = io.ReadAll(io.LimitReader(reader, (4<<20)+1))
		if e != nil {
			return empty, ports.ErrBadResponse
		}
	}
	if len(decoded) == 0 || len(decoded) > 4<<20 || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 {
		return empty, ports.ErrBadResponse
	}
	stats, err := parseAccountStats(decoded, target, expectedID)
	if e := ctx.Err(); e != nil {
		return empty, requestError(ctx, e)
	}
	return stats, err
}

func accountStatsURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return nil, false
	}
	if u.RawPath != "" || strings.Contains(u.Path, "%") || strings.Contains(u.Path, "//") {
		return nil, false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, false
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, false
		}
	}
	u.Path = path.Join("/", u.Path, "index.php")
	return u, true
}

func accountLink(base *url.URL, raw string) (*url.URL, bool) {
	if raw == "" || strings.ContainsAny(raw, "\\\r\n\t") {
		return nil, false
	}
	ref, err := url.Parse(raw)
	if err != nil || ref.User != nil || ref.RawPath != "" || strings.Contains(ref.Path, "%") {
		return nil, false
	}
	u := base.ResolveReference(ref)
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return nil, false
	}
	return u, true
}

func accountRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	var wait time.Duration
	if raw != "" && strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > 3600 {
			return time.Hour
		}
		wait = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(raw); err == nil {
		wait = date.Sub(now)
	} else {
		return 10 * time.Second
	}
	if wait < 0 {
		return 0
	}
	if wait > time.Hour {
		return time.Hour
	}
	return wait
}

func accountAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func accountWalk(n *html.Node, visit func(*html.Node)) {
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
		return
	}
	visit(n)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		accountWalk(child, visit)
	}
}

func accountAncestor(n *html.Node, tag string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return p
		}
	}
	return nil
}

// The identity proof follows include/functions.php:2353 at NexusPHP commit
// a3ba82be64e7c1cbfa72230d6b0dfb35dd3df459: a unique welcome row,
// containing get_username(CURUSER[id]), logout, and the four field markers.
func parseAccountStats(body []byte, base *url.URL, expected string) (ports.SiteAccountStats, error) {
	bad := func() (ports.SiteAccountStats, error) { return ports.SiteAccountStats{}, ports.ErrBadResponse }
	// Reject duplicate attributes before HTML's error recovery can hide contradictions.
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	tokens := 0
	stack := []string{}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() != io.EOF {
				return bad()
			}
			break
		}
		tokens++
		if tokens > 32768 {
			return bad()
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			raw := tokenizer.Raw()
			if len(raw) > 65536 || !accountTagAttributes(raw) {
				return bad()
			}
			name, _ := tokenizer.TagName()
			tag := string(name)
			if kind == html.StartTagToken && !strings.Contains("|area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr|", "|"+tag+"|") {
				stack = append(stack, tag)
				if len(stack) > 256 {
					return bad()
				}
			}
		} else if kind == html.EndTagToken {
			name, _ := tokenizer.TagName()
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == string(name) {
					stack = stack[:i]
					break
				}
			}
		}
	}
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return bad()
	}
	var block *html.Node
	blocks := 0
	invalid := false
	accountWalk(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if accountAttr(n, "id") == "info_block" {
			blocks++
			block = n
		}
		id := strings.ToLower(accountAttr(n, "id"))
		if n.Data == "base" || (n.Data == "input" && strings.EqualFold(accountAttr(n, "type"), "password")) || strings.Contains(id, "challenge") || strings.HasPrefix(id, "cf-") {
			invalid = true
		}
	})
	if invalid || blocks != 1 || block.Data != "table" {
		return bad()
	}
	var welcome *html.Node
	logouts := 0
	accountWalk(block, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		link, ok := accountLink(base, accountAttr(n, "href"))
		if !ok {
			invalid = true
			return
		}
		if link.Path == path.Join(path.Dir(base.Path), "logout.php") {
			if link.RawQuery != "" || link.Fragment != "" {
				invalid = true
			}
			logouts++
			welcome = accountAncestor(n, "tr")
		}
	})
	if invalid || logouts != 1 || welcome == nil {
		return bad()
	}
	ids := 0
	accountWalk(welcome, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "a" {
			return
		}
		link, ok := accountLink(base, accountAttr(n, "href"))
		if !ok {
			invalid = true
			return
		}
		if link.Path == path.Join(path.Dir(base.Path), "userdetails.php") {
			ids++
			query, e := url.ParseQuery(link.RawQuery)
			if e != nil || len(query) != 1 || len(query["id"]) != 1 || query.Get("id") != expected || !indexer.ValidSiteAccountID(query.Get("id")) || link.Fragment != "" {
				invalid = true
			}
		}
	})
	if invalid || ids != 1 {
		return bad()
	}
	fields := map[string]string{}
	counts := map[string]int{}
	active := ""
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "script" || n.Data == "style" || n.Data == "template" {
				return
			}
			marker := ""
			for _, cl := range strings.Fields(accountAttr(n, "class")) {
				if strings.HasPrefix(cl, "color_") {
					if marker != "" {
						invalid = true
					}
					marker = cl
				}
			}
			if marker != "" {
				active = marker
				counts[marker]++
				if accountAncestor(n, "tr") != welcome {
					invalid = true
				}
				return
			}
			if n.Data == "a" && active == "color_bonus" {
				link, ok := accountLink(base, accountAttr(n, "href"))
				if !ok || link.Path != path.Join(path.Dir(base.Path), "mybonus.php") || link.RawQuery != "" {
					invalid = true
				}
				return
			}
		}
		if n.Type == html.TextNode && active != "" {
			if len(fields[active])+len(n.Data) > 512 {
				invalid = true
				return
			}
			fields[active] += n.Data
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(block)
	for _, key := range []string{"color_uploaded", "color_downloaded", "color_ratio", "color_bonus"} {
		if counts[key] != 1 || strings.TrimSpace(fields[key]) == "" {
			invalid = true
		}
	}
	if invalid {
		return bad()
	}
	uploaded, e1 := indexer.ParseSiteAccountAmount(strings.TrimSpace(fields["color_uploaded"]))
	downloaded, e2 := indexer.ParseSiteAccountAmount(strings.TrimSpace(fields["color_downloaded"]))
	ratio, e3 := indexer.ParseSiteAccountRatio(strings.TrimSpace(fields["color_ratio"]))
	bonusRaw := strings.TrimSpace(fields["color_bonus"])
	// The classic bonus marker is followed by [<a href=mybonus.php>Use</a>]:.
	if !strings.HasPrefix(bonusRaw, "[]:") {
		return bad()
	}
	bonus, e4 := indexer.ParseSiteAccountBonus(strings.TrimSpace(strings.TrimPrefix(bonusRaw, "[]:")))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return bad()
	}
	stats := ports.SiteAccountStats{AccountID: expected, Uploaded: uploaded, Downloaded: downloaded, Ratio: ratio, Bonus: &bonus}
	if indexer.ValidateSiteAccountStats(stats) != nil {
		return bad()
	}
	return stats, nil
}

// x/net/html discards duplicate attributes during tokenization; inspect each raw
// tag before that normalization can hide a contradictory identity or field.
func accountTagAttributes(raw []byte) bool {
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' }
	i := 1
	for i < len(raw) && !space(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	seen := map[string]bool{}
	for i < len(raw) {
		for i < len(raw) && space(raw[i]) {
			i++
		}
		if i == len(raw) || raw[i] == '>' || raw[i] == '/' {
			return true
		}
		start := i
		for i < len(raw) && !space(raw[i]) && raw[i] != '=' && raw[i] != '>' && raw[i] != '/' {
			i++
		}
		if i == start {
			return false
		}
		key := strings.ToLower(string(raw[start:i]))
		if seen[key] {
			return false
		}
		seen[key] = true
		for i < len(raw) && space(raw[i]) {
			i++
		}
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && space(raw[i]) {
				i++
			}
			if i == len(raw) {
				return false
			}
			if raw[i] == '\'' || raw[i] == '"' {
				quote := raw[i]
				i++
				for i < len(raw) && raw[i] != quote {
					i++
				}
				if i == len(raw) {
					return false
				}
				i++
			} else {
				for i < len(raw) && !space(raw[i]) && raw[i] != '>' {
					i++
				}
			}
		}
	}
	return true
}
