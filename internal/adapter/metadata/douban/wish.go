package douban

import (
	"bytes"
	"context"
	"errors"
	stdhtml "html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/net/html"
)

const wishPublicOrigin = "https://movie.douban.com"

var (
	wishUID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	wishDigits  = regexp.MustCompile(`^[0-9]+$`)
	wishSubject = regexp.MustCompile(`^/subject/([1-9][0-9]*)/$`)
	wishIcon    = regexp.MustCompile(`^/icon/u([1-9][0-9]*)-[A-Za-z0-9]+\.(jpg|jpeg|png)$`)
	wishRange   = regexp.MustCompile(`^([0-9]+)-([0-9]+)\s*/\s*([0-9]+)$`)
)

var _ ports.DoubanWishProvider = (*WishClient)(nil)

// WishClient reads only public wish pages, without the Frodo client's credentials.
type WishClient struct {
	base       *url.URL
	httpClient *http.Client
}

// NewWishClient copies the client; an unsafe origin is rejected before any I/O.
func NewWishClient(baseURL string, httpClient *http.Client) *WishClient {
	hc := http.Client{}
	if httpClient != nil {
		hc = *httpClient
	}
	if hc.Timeout == 0 {
		hc.Timeout = 10 * time.Second
	}
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if baseURL == "" {
		baseURL = wishPublicOrigin
	}
	base, err := url.Parse(baseURL)
	if err != nil || strings.Contains(baseURL, "#") || !safeWishBase(base) {
		base = nil
	}
	return &WishClient{base: base, httpClient: &hc}
}

func safeWishBase(u *url.URL) bool {
	if u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}

func (c *WishClient) ReadWishPage(ctx context.Context, userID, kind string, start int) (ports.DoubanWishPage, error) {
	empty := ports.DoubanWishPage{}
	if !wishUID.MatchString(userID) || (kind != "movie" && kind != "tv") || start < 0 || start > ports.MaxDoubanWishStart || start%ports.DoubanWishPageSize != 0 || c.base == nil {
		return empty, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	target := *c.base
	target.Path = "/people/" + userID + "/wish"
	target.RawQuery = url.Values{"type": {kind}, "start": {strconv.Itoa(start)}, "sort": {"time"}, "filter": {"all"}, "mode": {"grid"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return empty, ports.ErrRejected
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PulseMedia/0.1)")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return empty, wishIOError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		return empty, ports.ErrDoubanWishAccessDenied
	case http.StatusTooManyRequests:
		return empty, ports.NewRateLimitError(5*time.Second, "douban rate limited")
	case http.StatusOK:
	default:
		return empty, ports.ErrBadResponse
	}
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "text/html" || !strings.EqualFold(params["charset"], "utf-8") || resp.ContentLength > maxBodyBytes {
		return empty, ports.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return empty, wishIOError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(raw) > maxBodyBytes || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return empty, ports.ErrBadResponse
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return empty, ports.ErrBadResponse
	}
	page, err := c.parseWish(doc, userID, kind, start)
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	if err != nil {
		return empty, err
	}
	return page, nil
}

func wishIOError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

func (c *WishClient) parseWish(doc *html.Node, uid, kind string, start int) (ports.DoubanWishPage, error) {
	p := ports.DoubanWishPage{RequestedID: uid, MediaType: kind, Start: start, Items: make([]ports.DoubanWishItem, 0)}
	bad := ports.ErrBadResponse
	profiles := wishFind(doc, func(n *html.Node) bool { return wishAttr(n, "id") == "db-usr-profile" })
	grids := wishClass(doc, "grid-view")
	ranges := wishClass(doc, "subject-num")
	if len(profiles) != 1 || len(grids) != 1 || len(ranges) != 1 {
		return p, bad
	}
	pics := wishClass(profiles[0], "pic")
	if len(pics) != 1 {
		return p, bad
	}
	links := wishTag(pics[0], "a")
	if len(links) != 1 {
		return p, bad
	}
	owner, ok := c.wishURL(wishAttr(links[0], "href"))
	if !ok || owner.RawQuery != "" || owner.ForceQuery {
		return p, bad
	}
	parts := strings.Split(owner.Path, "/")
	if len(parts) != 4 || parts[1] != "people" || parts[3] != "" || !wishUID.MatchString(parts[2]) {
		return p, bad
	}
	p.CanonicalID = parts[2]
	images := wishTag(pics[0], "img")
	if len(images) == 1 {
		img, e := url.Parse(wishAttr(images[0], "src"))
		if e == nil && img.Scheme == "https" && img.User == nil && img.Port() == "" && posterHostRe.MatchString(img.Hostname()) && img.RawQuery == "" && !img.ForceQuery && img.Fragment == "" && img.RawPath == "" {
			if match := wishIcon.FindStringSubmatch(img.Path); match != nil {
				p.ProfileNumber = match[1]
			}
		}
	}
	if uid != p.CanonicalID && (!wishDigits.MatchString(uid) || uid != p.ProfileNumber) {
		return p, bad
	}
	selected := 0
	for _, tab := range wishClass(doc, "tabs-more") {
		labels := wishFind(tab, func(n *html.Node) bool {
			return n.Data == "span" && wishHasClass(n, "gray") && wishText(n) == "影片类型:"
		})
		if len(labels) == 0 {
			continue
		}
		controls := wishClass(tab, "lnk-tab-more")
		if len(labels) != 1 || len(controls) != 1 {
			return p, bad
		}
		spans := wishTag(controls[0], "span")
		expected := "电影"
		if kind == "tv" {
			expected = "电视"
		}
		if len(spans) != 1 || wishText(spans[0]) != expected {
			return p, bad
		}
		selected++
	}
	if selected != 1 {
		return p, bad
	}
	match := wishRange.FindStringSubmatch(wishText(ranges[0]))
	if match == nil {
		return p, bad
	}
	nums := [3]int{}
	for i := range nums {
		v, e := strconv.Atoi(match[i+1])
		if e != nil || v > 1000000 {
			return p, bad
		}
		nums[i] = v
	}
	first, last, total := nums[0], nums[1], nums[2]
	p.Total = total
	headings := wishTag(profiles[0], "h1")
	suffix := "想看的电影(" + strconv.Itoa(total) + ")"
	if kind == "tv" {
		suffix = "想看的电视剧(" + strconv.Itoa(total) + ")"
	}
	if len(headings) != 1 || !strings.HasSuffix(wishText(headings[0]), suffix) {
		return p, bad
	}
	seen := map[string]bool{}
	for n := grids[0].FirstChild; n != nil; n = n.NextSibling {
		if !wishHasClass(n, "item") {
			if n.Type == html.ElementNode {
				return p, bad
			}
			continue
		}
		if len(p.Items) >= ports.DoubanWishPageSize {
			return p, bad
		}
		item, err := c.wishItem(n, kind)
		if err != nil || seen[item.DoubanID] {
			return p, bad
		}
		seen[item.DoubanID] = true
		p.Items = append(p.Items, item)
	}
	if len(wishClass(grids[0], "item")) != len(p.Items) {
		return p, bad
	}
	if total == 0 {
		if start != 0 || first != 0 || last != 0 || len(p.Items) != 0 {
			return p, bad
		}
	} else if start >= total || first != start+1 || last != min(start+ports.DoubanWishPageSize, total) || len(p.Items) > last-first+1 {
		return p, bad
	}
	p.WindowEnd = last
	p.WindowComplete = len(p.Items) == last-start
	paginators := wishClass(doc, "paginator")
	if len(paginators) > 1 || total > ports.DoubanWishPageSize && len(paginators) != 1 {
		return p, bad
	}
	if len(paginators) == 1 {
		current := wishClass(paginators[0], "thispage")
		if len(current) != 1 {
			return p, bad
		}
		num, e1 := strconv.Atoi(wishText(current[0]))
		pages, e2 := strconv.Atoi(wishAttr(current[0], "data-total-page"))
		if e1 != nil || e2 != nil || num != start/ports.DoubanWishPageSize+1 || pages != max(1, (total+ports.DoubanWishPageSize-1)/ports.DoubanWishPageSize) {
			return p, bad
		}
	}
	next := wishFind(doc, func(n *html.Node) bool {
		return strings.Contains(" "+strings.Join(strings.Fields(wishAttr(n, "rel")), " ")+" ", " next ")
	})
	more := last < total
	if more && len(next) != 1 || !more && len(next) != 0 {
		return p, bad
	}
	if more {
		offset := last
		if next[0].Data != "link" || !c.validWishNext(wishAttr(next[0], "href"), uid, p.CanonicalID, kind, offset) {
			return p, bad
		}
		p.NextStart = &offset
	}
	return p, nil
}

// DOM decoding removes the first entity layer; Douban sometimes emits two.
func (c *WishClient) wishURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(stdhtml.UnescapeString(raw))
	if err != nil || raw == "" || strings.Contains(stdhtml.UnescapeString(raw), "#") || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, false
	}
	if u.Host != "" || u.Scheme != "" {
		sameBase := u.Scheme == c.base.Scheme && u.Host == c.base.Host
		samePublic := u.Scheme == "https" && u.Host == "movie.douban.com"
		if !sameBase && !samePublic {
			return nil, false
		}
	}
	return u, true
}

func (c *WishClient) validWishNext(raw, uid, canonical, kind string, offset int) bool {
	u, ok := c.wishURL(raw)
	if !ok || u.Path != "/people/"+uid+"/wish" && u.Path != "/people/"+canonical+"/wish" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("type") != kind || q.Get("start") != strconv.Itoa(offset) {
		return false
	}
	for key, vals := range q {
		if len(vals) != 1 {
			return false
		}
		switch key {
		case "type", "start":
		case "filter":
			if vals[0] != "all" {
				return false
			}
		case "mode":
			if vals[0] != "grid" {
				return false
			}
		case "sort":
			if vals[0] != "time" {
				return false
			}
		case "tags_sort":
			if vals[0] != "count" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (c *WishClient) wishSubjectID(raw string) (string, bool) {
	u, ok := c.wishURL(raw)
	if !ok || u.RawQuery != "" || u.ForceQuery {
		return "", false
	}
	match := wishSubject.FindStringSubmatch(u.Path)
	if match == nil {
		return "", false
	}
	return match[1], true
}

func (c *WishClient) wishItem(n *html.Node, kind string) (ports.DoubanWishItem, error) {
	item := ports.DoubanWishItem{MediaType: kind}
	bad := ports.ErrBadResponse
	infos := wishClass(n, "info")
	if len(infos) != 1 {
		return item, bad
	}
	titles := wishFind(infos[0], func(n *html.Node) bool { return n.Data == "li" && wishHasClass(n, "title") })
	if len(titles) != 1 {
		return item, bad
	}
	anchors := wishTag(titles[0], "a")
	if len(anchors) != 1 || len(wishTag(anchors[0], "em")) != 1 {
		return item, bad
	}
	id, ok := c.wishSubjectID(wishAttr(anchors[0], "href"))
	if !ok {
		return item, bad
	}
	item.DoubanID = id
	for _, pic := range wishClass(n, "pic") {
		for _, a := range wishTag(pic, "a") {
			picID, valid := c.wishSubjectID(wishAttr(a, "href"))
			if !valid || picID != id {
				return item, bad
			}
		}
	}
	remaining := " " + wishText(anchors[0]) + " "
	for {
		index := strings.Index(remaining, " / ")
		alias := remaining
		if index >= 0 {
			alias = remaining[:index]
		}
		alias = strings.TrimSpace(alias)
		if alias == "" || len(alias) > 512 || len(item.Titles) >= 8 {
			return item, bad
		}
		item.Titles = append(item.Titles, alias)
		if index < 0 {
			break
		}
		// Retain the separator's trailing space to detect consecutive empty aliases.
		remaining = remaining[index+2:]
	}
	intros := wishClass(infos[0], "intro")
	if len(intros) > 1 {
		return item, bad
	}
	if len(intros) == 1 {
		intro := wishText(intros[0])
		if len(intro) >= 4 && wishDigits.MatchString(intro[:4]) && (len(intro) == 4 || strings.ContainsRune("-(/", rune(intro[4])) || unicode.IsSpace(rune(intro[4]))) {
			item.Year, _ = strconv.Atoi(intro[:4])
		}
	}
	return item, nil
}

func wishAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func wishHasClass(n *html.Node, class string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, v := range strings.Fields(wishAttr(n, "class")) {
		if v == class {
			return true
		}
	}
	return false
}
func wishFind(root *html.Node, predicate func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && predicate(n) {
			found = append(found, n)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return found
}
func wishClass(root *html.Node, class string) []*html.Node {
	return wishFind(root, func(n *html.Node) bool { return wishHasClass(n, class) })
}
func wishTag(root *html.Node, tag string) []*html.Node {
	return wishFind(root, func(n *html.Node) bool { return n.Data == tag })
}
func wishText(root *html.Node) string {
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return strings.Join(strings.Fields(b.String()), " ")
}
