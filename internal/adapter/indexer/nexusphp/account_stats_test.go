package nexusphp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Synthetic fixture derived from official include/functions.php:2353 at
// a3ba82be64e7c1cbfa72230d6b0dfb35dd3df459. Never recorded from a live PT site.
const accountStatsFixture = `<!doctype html><html><body>
<table id="info_block"><tr><td><table><tr>
<td class="bottom"><span class="medium">Welcome back,
<a href="userdetails.php?id=42"><b>Alice</b></a> [<a href="logout.php">Logout</a>]
<font class="color_bonus">Bonus</font>[<a href="mybonus.php">Use</a>]: 1,234.5
<font class="color_invite">Invites</font>[<a href="invite.php?id=42">Send</a>]: 9<br>
<font class="color_ratio">Ratio</font> <font color="green">2.000</font>
<font class="color_uploaded">Uploaded</font> 2.00 GB
<font class="color_downloaded">Downloaded</font> 1.00 GB
<font class="color_active">Active</font> 12
<font class="color_connectable">Connectable</font> Yes</span></td>
<td class="bottom">Time: 12:00</td></tr></table></td></tr></table>
<a href="userdetails.php?id=999">Other user</a>
<div>Bonus per hour: 9999.9</div></body></html>`

type accountTestClock struct{ now time.Time }

func (c accountTestClock) Now() time.Time                  { return c.now }
func (c accountTestClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

var statsClock = accountTestClock{time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}

type accountTransport func(*http.Request) (*http.Response, error)

func (f accountTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type accountBody struct {
	io.Reader
	closed bool
	cancel context.CancelFunc
}

func (b *accountBody) Read(p []byte) (int, error) {
	n, e := b.Reader.Read(p)
	if b.cancel != nil {
		b.cancel()
	}
	return n, e
}
func (b *accountBody) Close() error { b.closed = true; return nil }

func TestAccountStatsRequestAndValues(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.RequestURI() != "/pt/index.php" || r.Header.Get("Cookie") != "session=explicit" || r.Header.Get("Accept") != "text/html" || r.Header.Get("User-Agent") != "PulseMedia/1.0" {
			t.Errorf("unexpected request shape")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
			t.Error("unexpected credential header")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, accountStatsFixture)
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(server.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "other", Value: "secret"}})
	original := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("original") }}
	c := NewAccountStatsClient(server.URL+"/pt/", "session=explicit", original, statsClock)
	stats, err := c.ReadAccountStats(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if stats.AccountID != "42" || stats.Uploaded.Value != "2.00" || stats.Uploaded.Unit != "GB" || !stats.Uploaded.Approximate || stats.Downloaded.Value != "1.00" || stats.Ratio.Value != "2.000" || stats.Bonus == nil || *stats.Bonus != "1234.5" {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if calls.Load() != 1 || original.Jar != jar || original.CheckRedirect == nil {
		t.Fatal("client mutated or extra request")
	}
}

func TestAccountStatsPreflightZeroIO(t *testing.T) {
	for _, tc := range []struct {
		name, base, cookie, id string
		clock                  ports.Clock
		want                   error
	}{
		{"blank cookie", "https://example.test", " ", "42", statsClock, ports.ErrAuthRequired},
		{"bad id", "https://example.test", "x=y", "042", statsClock, ports.ErrRejected},
		{"uuid", "https://example.test", "x=y", "aabb-ccdd", statsClock, ports.ErrRejected},
		{"clock", "https://example.test", "x=y", "42", nil, ports.ErrUnavailable},
		{"query", "https://example.test/?passkey=secret", "x=y", "42", statsClock, ports.ErrRejected},
		{"userinfo", "https://user:secret@example.test", "x=y", "42", statsClock, ports.ErrRejected},
		{"fragment", "https://example.test/#x", "x=y", "42", statsClock, ports.ErrRejected},
		{"encoded slash", "https://example.test/pt%2fother", "x=y", "42", statsClock, ports.ErrRejected},
		{"dot path", "https://example.test/../pt", "x=y", "42", statsClock, ports.ErrRejected},
		{"bad scheme", "file:///pt", "x=y", "42", statsClock, ports.ErrRejected},
		{"bad port", "https://example.test:99999", "x=y", "42", statsClock, ports.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: accountTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected IO"); return nil, nil })}
			_, err := NewAccountStatsClient(tc.base, tc.cookie, client, tc.clock).ReadAccountStats(context.Background(), tc.id)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestAccountStatsRejectsMaliciousDOM(t *testing.T) {
	for name, body := range map[string]string{
		"wrong current uid": strings.Replace(accountStatsFixture, "id=42\"", "id=43\"", 1),
		"duplicate uid":     strings.Replace(accountStatsFixture, "[<a href=\"logout.php\">", "<a href=\"userdetails.php?id=42\">Extra</a>[<a href=\"logout.php\">", 1),
		"duplicate query":   strings.Replace(accountStatsFixture, "id=42\"", "id=42&amp;id=42\"", 1),
		"no logout":         strings.Replace(accountStatsFixture, "logout.php", "other.php", 1),
		"duplicate block":   accountStatsFixture + `<table id="info_block"></table>`,
		"foreign identity":  strings.Replace(accountStatsFixture, "userdetails.php?id=42", "https://evil.test/userdetails.php?id=42", 1),
		"wrong marker":      strings.Replace(accountStatsFixture, "color_uploaded", "color_downloaded", 1),
		"empty amount":      strings.Replace(accountStatsFixture, "2.00 GB", "", 1),
		"unknown amount":    strings.Replace(accountStatsFixture, "2.00 GB", "unknown", 1),
		"bad ratio":         strings.Replace(accountStatsFixture, "2.000", "NaN", 1),
		"bad bonus":         strings.Replace(accountStatsFixture, "1,234.5", "999.99", 1),
		"bonus elsewhere":   strings.Replace(accountStatsFixture, "1,234.5", "", 1),
		"login":             accountStatsFixture + `<form action="login.php"><input type="password"></form>`,
		"challenge":         accountStatsFixture + `<div id="cf-challenge"></div>`,
		"base":              `<base href="https://evil.test">` + accountStatsFixture,
		"duplicate attr":    strings.Replace(accountStatsFixture, `id="info_block"`, `id="info_block" id="x"`, 1),
		"binary":            accountStatsFixture + "\x00",
		"invalid utf8":      accountStatsFixture + "\xff",
		"script only":       `<script>` + accountStatsFixture + `</script>`,
	} {
		t.Run(name, func(t *testing.T) {
			c := NewAccountStatsClient("https://example.test/pt", "session=secret", &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}, statsClock)
			_, err := c.ReadAccountStats(context.Background(), "42")
			if !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAccountStatsStatusesAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		status   int
		location string
		want     error
	}{
		{401, "", ports.ErrAuthRequired}, {403, "", ports.ErrBlocked}, {500, "", ports.ErrUnavailable}, {204, "", ports.ErrBadResponse},
		{302, "login.php", ports.ErrAuthRequired}, {301, "/pt/login.php", ports.ErrAuthRequired},
		{302, "https://evil.test/login.php", ports.ErrBadResponse}, {302, "/other/login.php", ports.ErrBadResponse},
		{302, "userdetails.php?id=42", ports.ErrBadResponse}, {302, "//evil.test/pt/login.php", ports.ErrBadResponse},
	} {
		t.Run(http.StatusText(tc.status)+tc.location, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			_, err := NewAccountStatsClient(server.URL+"/pt", "session=secret", server.Client(), statsClock).ReadAccountStats(context.Background(), "42")
			if !errors.Is(err, tc.want) || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestAccountStatsRetryAfter(t *testing.T) {
	for raw, want := range map[string]time.Duration{"0": 0, "17": 17 * time.Second, "999999999999999999999": time.Hour, "7200": time.Hour, "-5": 10 * time.Second, "garbage-secret": 10 * time.Second, "": 10 * time.Second, statsClock.now.Add(37 * time.Second).Format(http.TimeFormat): 37 * time.Second, statsClock.now.Add(-time.Minute).Format(http.TimeFormat): 0, statsClock.now.Add(2 * time.Hour).Format(http.TimeFormat): time.Hour} {
		t.Run(raw, func(t *testing.T) {
			client := &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{raw}}, Body: io.NopCloser(strings.NewReader("secret")), Request: r}, nil
			})}
			_, err := NewAccountStatsClient("https://example.test", "session=secret", client, statsClock).ReadAccountStats(context.Background(), "42")
			limit, ok := ports.IsRateLimited(err)
			if !ok || limit.RetryAfter != want || limit.Reason != "indexer rate limit reached" {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}

func TestAccountStatsBodyBoundsCancellationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body, mime string
		cancel           bool
		want             error
	}{
		{"empty", "", "text/html", false, ports.ErrBadResponse},
		{"wrong mime", accountStatsFixture, "application/json", false, ports.ErrBadResponse},
		{"bad charset", accountStatsFixture, "text/html; charset=unknown-garbage", false, ports.ErrBadResponse},
		{"oversize", strings.Repeat("a", (2<<20)+1), "text/html", false, ports.ErrBadResponse},
		{"decoded oversize", strings.Repeat("\x80", (2 << 20)), "text/html; charset=windows-1252", false, ports.ErrBadResponse},
		{"cancel body", accountStatsFixture, "text/html", true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &accountBody{Reader: strings.NewReader(tc.body)}
			if tc.cancel {
				body.cancel = cancel
			}
			client := &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{tc.mime}}, Body: body, Request: r}, nil
			})}
			_, err := NewAccountStatsClient("https://example.test", "session=secret", client, statsClock).ReadAccountStats(ctx, "42")
			if !errors.Is(err, tc.want) || !body.closed {
				t.Fatalf("err=%v closed=%v", err, body.closed)
			}
		})
	}
	client := &http.Client{Transport: accountTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://secret.test/?passkey=hidden cookie=hidden")
	})}
	_, err := NewAccountStatsClient("https://example.test", "session=hidden", client, statsClock).ReadAccountStats(context.Background(), "42")
	if err != ports.ErrUnavailable || strings.Contains(err.Error(), "hidden") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestAccountStatsIgnoresScriptAndAcceptsSameOriginLinks(t *testing.T) {
	fixture := strings.Replace(accountStatsFixture, "userdetails.php?id=42", "https://example.test/pt/userdetails.php?id=42", 1)
	fixture = strings.Replace(fixture, "<font class=\"color_ratio\">", `<!-- color_uploaded nonsense --><script>"color_bonus": "9999.9"</script><font class="color_ratio">`, 1)
	u, _ := accountStatsURL("https://example.test/pt")
	if _, err := parseAccountStats([]byte(fixture), u, "42"); err != nil {
		t.Fatal(err)
	}
}
func TestAccountStatsDuplicateDirect(t *testing.T) {
	u, _ := accountStatsURL("https://example.test/pt")
	body := strings.Replace(accountStatsFixture, `id="info_block"`, `id="info_block" id="x"`, 1)
	if body == accountStatsFixture {
		t.Fatal("replacement failed")
	}
	_, err := parseAccountStats([]byte(body), u, "42")
	if err == nil {
		t.Fatalf("accepted duplicate: %s", body[:100])
	}
}
func TestAccountStatsNativeSpecialValuesAndImmutableBody(t *testing.T) {
	u, _ := accountStatsURL("https://example.test/pt")
	for _, ratio := range []string{"Inf.", "无限", "無限", "---", "0.000"} {
		fixture := strings.Replace(accountStatsFixture, "2.000", ratio, 1)
		fixture = strings.Replace(fixture, "2.00 GB", "1,234.567 TB", 1)
		fixture = strings.Replace(fixture, "1.00 GB", "0.00 KB", 1)
		fixture += `<form action="index.php"><input name="poll" value="1"></form>`
		raw := []byte(fixture)
		stats, err := parseAccountStats(raw, u, "42")
		if err != nil {
			t.Fatalf("%s: %v", ratio, err)
		}
		if stats.Uploaded.Value != "1234.567" || stats.Downloaded.Value != "0.00" || string(raw) != fixture {
			t.Fatal("precision or source bytes changed")
		}
	}
}

func TestAccountStatsCancellationAndCloseAtResponse(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429, 500, 302} {
		ctx, cancel := context.WithCancel(context.Background())
		body := &accountBody{Reader: strings.NewReader(accountStatsFixture)}
		client := &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
			cancel()
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: body, Request: r}, nil
		})}
		_, err := NewAccountStatsClient("https://example.test", "session=explicit", client, statsClock).ReadAccountStats(ctx, "42")
		cancel()
		if !errors.Is(err, context.Canceled) || !body.closed {
			t.Fatalf("status=%d err=%v closed=%v", status, err, body.closed)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: accountTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled request performed IO")
		return nil, nil
	})}
	if _, err := NewAccountStatsClient("https://example.test", "x=y", client, statsClock).ReadAccountStats(ctx, "42"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAccountStatsRejectsDeepDOM(t *testing.T) {
	u, _ := accountStatsURL("https://example.test/pt")
	raw := strings.Repeat("<div>", 257) + accountStatsFixture + strings.Repeat("</div>", 257)
	if _, err := parseAccountStats([]byte(raw), u, "42"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal(err)
	}
}

func TestAccountStatsExplicitCharset(t *testing.T) {
	raw := strings.Replace(accountStatsFixture, "Alice", "Alic\xe9", 1)
	client := &http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html; charset=windows-1252"}}, Body: io.NopCloser(strings.NewReader(raw)), Request: r}, nil
	})}
	if _, err := NewAccountStatsClient("https://example.test", "x=y", client, statsClock).ReadAccountStats(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}
}
