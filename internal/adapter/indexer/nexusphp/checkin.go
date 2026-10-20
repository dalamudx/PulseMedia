package nexusphp

import (
	"bytes"
	"context"
	"crypto/tls"
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

var _ ports.SiteCheckinNativeClient = (*CheckinClient)(nil)

// CheckinClient supports only NexusPHP 1.10.3's native welcome attendance mark.
// Submit requires a standard *http.Transport: arbitrary RoundTrippers cannot
// promise that a side effect is sent once. Neither method retries.
type CheckinClient struct {
	baseURL, cookie string
	client          http.Client
	clock           ports.Clock
}

func NewCheckinClient(baseURL, cookie string, client *http.Client, clock ports.Clock) *CheckinClient {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	clone := *client
	clone.Jar = nil
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if clone.Timeout <= 0 || clone.Timeout > 15*time.Second {
		clone.Timeout = 15 * time.Second
	}
	return &CheckinClient{baseURL: baseURL, cookie: cookie, client: clone, clock: clock}
}

func (c *CheckinClient) ObserveCheckin(ctx context.Context, expectedUID string) (ports.CheckinObservation, error) {
	observation, _, err := c.checkin(ctx, expectedUID, false)
	return observation, err
}

func (c *CheckinClient) SubmitCheckin(ctx context.Context, expectedUID string) (ports.CheckinSubmission, error) {
	observation, dispatched, err := c.checkin(ctx, expectedUID, true)
	result := ports.CheckinSubmission{State: ports.CheckinSubmissionNotSent}
	if dispatched {
		result.State = ports.CheckinSubmissionUnknown
	}
	if err != nil {
		return result, err
	}
	if observation.Mark != ports.CheckinMarkAttended {
		return result, ports.ErrBadResponse
	}
	result.State = ports.CheckinSubmissionAttended
	result.Observation = &observation
	return result, nil
}

func (c *CheckinClient) checkin(ctx context.Context, uid string, submit bool) (ports.CheckinObservation, bool, error) {
	empty := ports.CheckinObservation{}
	if err := ctx.Err(); err != nil {
		return empty, false, requestError(ctx, err)
	}
	if c.clock == nil {
		return empty, false, ports.ErrUnavailable
	}
	if strings.TrimSpace(c.cookie) == "" {
		return empty, false, ports.ErrAuthRequired
	}
	if strings.ContainsAny(c.cookie, "\r\n") || !indexer.ValidSiteAccountID(uid) {
		return empty, false, ports.ErrRejected
	}
	target, ok := accountStatsURL(c.baseURL)
	if !ok || strings.Contains(c.baseURL, "#") {
		return empty, false, ports.ErrRejected
	}
	transport := c.client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	standard, ok := transport.(*http.Transport)
	if !ok || standard == nil {
		return empty, false, ports.ErrRejected
	}
	owned := standard.Clone()
	owned.DisableKeepAlives = true
	owned.DisableCompression = true
	owned.ForceAttemptHTTP2 = false
	owned.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if owned.TLSClientConfig == nil {
		owned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		owned.TLSClientConfig = owned.TLSClientConfig.Clone()
	}
	owned.TLSClientConfig.NextProtos = []string{"http/1.1"}
	// Protocols overrides ForceAttemptHTTP2 on newer Go versions.
	owned.Protocols = new(http.Protocols)
	owned.Protocols.SetHTTP1(true)
	defer owned.CloseIdleConnections()
	client := c.client
	client.Transport = owned
	method := http.MethodGet
	if submit {
		method = http.MethodPost
		target.Path = path.Join(path.Dir(target.Path), "attendance.php")
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return empty, false, ports.ErrRejected
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("User-Agent", "PulseMedia/1.0")
	req.Header.Set("Accept", "text/html")
	if err := ctx.Err(); err != nil {
		return empty, false, requestError(ctx, err)
	}
	requested := c.clock.Now()
	// From Do onward we conservatively retain physical uncertainty, even on cancel.
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return empty, submit, requestError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return empty, submit, requestError(ctx, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return empty, submit, ports.ErrAuthRequired
	case resp.StatusCode == http.StatusForbidden:
		return empty, submit, ports.ErrBlocked
	case resp.StatusCode == http.StatusTooManyRequests:
		return empty, submit, ports.NewRateLimitError(accountRetryAfter(resp.Header.Get("Retry-After"), c.clock.Now()), "indexer rate limit reached")
	case resp.StatusCode >= 500 && resp.StatusCode <= 599:
		return empty, submit, ports.ErrUnavailable
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		dest, valid := accountLink(target, resp.Header.Get("Location"))
		if valid && dest.Path == path.Join(path.Dir(target.Path), "login.php") {
			return empty, submit, ports.ErrAuthRequired
		}
		return empty, submit, ports.ErrBadResponse
	case resp.StatusCode != http.StatusOK:
		return empty, submit, ports.ErrBadResponse
	}
	body, err := checkinHTML(resp)
	if err != nil {
		if ctx.Err() != nil {
			return empty, submit, requestError(ctx, ctx.Err())
		}
		return empty, submit, err
	}
	mark, err := parseCheckin(body, target, uid)
	if ctx.Err() != nil {
		return empty, submit, requestError(ctx, ctx.Err())
	}
	if err != nil {
		return empty, submit, err
	}
	return ports.CheckinObservation{AccountID: uid, Mark: mark, RequestedAt: requested, ObservedAt: c.clock.Now()}, submit, nil
}

func checkinHTML(resp *http.Response) ([]byte, error) {
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "text/html" {
		return nil, ports.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return nil, requestError(context.Background(), err)
	}
	if len(raw) == 0 || len(raw) > 2<<20 || bytes.IndexByte(raw, 0) >= 0 {
		return nil, ports.ErrBadResponse
	}
	decoded := raw
	label := strings.ToLower(params["charset"])
	if label != "" && label != "utf-8" && label != "utf8" {
		if _, name := charset.Lookup(label); name == "" {
			return nil, ports.ErrBadResponse
		}
		reader, e := charset.NewReaderLabel(label, bytes.NewReader(raw))
		if e != nil {
			return nil, ports.ErrBadResponse
		}
		decoded, e = io.ReadAll(io.LimitReader(reader, (4<<20)+1))
		if e != nil {
			return nil, ports.ErrBadResponse
		}
	}
	if len(decoded) == 0 || len(decoded) > 4<<20 || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 {
		return nil, ports.ErrBadResponse
	}
	return bytes.TrimPrefix(decoded, []byte{0xef, 0xbb, 0xbf}), nil
}

// Proof: xiaomlove/nexusphp v1.10.3, 31c176acae9dab3da038f72d15629d7c5bbf6eec,
// include/functions.php:2699-2735 and lang/{chs,cht,en}/lang_functions.php.
// The welcome mark has no absolute date and proves no attribution to this call.
func parseCheckin(body []byte, base *url.URL, uid string) (ports.CheckinMark, error) {
	welcome, err := parseAccountWelcome(body, base, uid)
	if err != nil {
		return "", err
	}
	var candidate *html.Node
	count := 0
	invalid := false
	// Unlike accountWalk, inspect inert subtrees too: hidden duplicates are not proof.
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, key := range []string{"id", "name", "class"} {
				value := strings.ToLower(accountAttr(n, key))
				if strings.Contains(value, "captcha") || strings.Contains(value, "challenge") || value == "imagehash" || value == "imagestring" {
					invalid = true
				}
			}
		}
		if n.Type == html.TextNode && n.Parent != nil && (n.Parent.Data == "script" || n.Parent.Data == "style") && (strings.Contains(n.Data, "attendance.php") || strings.Contains(n.Data, "[Attend") || strings.Contains(n.Data, "[签到") || strings.Contains(n.Data, "[簽到")) {
			invalid = true
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			link, ok := accountLink(base, accountAttr(n, "href"))
			if ok && link.Path == path.Join(path.Dir(base.Path), "attendance.php") {
				count++
				candidate = n
				if link.RawQuery != "" || link.ForceQuery || link.Fragment != "" || strings.Contains(accountAttr(n, "href"), "#") || accountAncestor(n, "tr") != welcome {
					invalid = true
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	root := welcome
	for root.Parent != nil {
		root = root.Parent
	}
	walk(root)
	if invalid || count != 1 {
		return "", ports.ErrBadResponse
	}
	for n := candidate; n != nil; n = n.Parent {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "template" || n.Data == "script" || n.Data == "style" || n.Data == "noscript" {
			return "", ports.ErrBadResponse
		}
		for _, a := range n.Attr {
			// Inline styles and arbitrary classes on the mark could conceal a false claim.
			if a.Key == "hidden" || a.Key == "inert" || a.Key == "style" || (a.Key == "aria-hidden" && a.Val != "false") || (a.Key == "class" && (strings.Contains(strings.ToLower(a.Val), "hidden") || strings.Contains(strings.ToLower(a.Val), "invisible"))) {
				return "", ports.ErrBadResponse
			}
		}
	}
	text := ""
	for n := candidate.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.TextNode {
			return "", ports.ErrBadResponse
		}
		text += n.Data
	}
	hasClass := false
	for _, attr := range candidate.Attr {
		if attr.Key == "class" {
			hasClass = true
		}
	}
	if !hasClass {
		return "", ports.ErrBadResponse
	}
	class := accountAttr(candidate, "class")
	if class == "faqlink" {
		switch text {
		case "[签到得魔力]", "[簽到得魔力]", "[Attend get bonus]":
			return ports.CheckinMarkPending, nil
		}
	}
	if class == "" {
		for _, pattern := range [][2]string{{"[签到已得", ", 补签卡: "}, {"[簽到已得", ", 補簽卡: "}, {"[Attend got: ", ", card: "}} {
			if !strings.HasPrefix(text, pattern[0]) || !strings.HasSuffix(text, "]") {
				continue
			}
			parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, pattern[0]), "]"), pattern[1])
			if len(parts) == 2 && checkinCounter(parts[0]) && checkinCounter(parts[1]) {
				return ports.CheckinMarkAttended, nil
			}
		}
	}
	return "", ports.ErrBadResponse
}

func checkinCounter(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 63)
	return err == nil
}
