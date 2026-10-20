package nexusphp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Synthetic native structure, pinned to 31c176acae9dab3da038f72d15629d7c5bbf6eec.
// Deliberately no statistics: checkin does not require statistics to parse.
func checkinFixture(mark string) string {
	return `<!doctype html><html><body><table id="info_block"><tr><td><table><tr><td class="bottom"><span class="medium">Welcome <a href="userdetails.php?id=42"><b>Alice</b></a> [<a href="logout.php">Logout</a>] ` + mark + `</span></td></tr></table></td></tr></table><a href="userdetails.php?id=999">Other user</a></body></html>`
}

const checkinPositive = `<a href="attendance.php" class="">[Attend got: 0, card: 0]</a>`
const checkinPending = `<a href="attendance.php" class="faqlink">[Attend get bonus]</a>`

func TestCheckinNativeMarks(t *testing.T) {
	base, _ := url.Parse("https://example.test/pt/index.php")
	for _, tc := range []struct {
		label, class string
		mark         ports.CheckinMark
	}{
		{"[签到得魔力]", "faqlink", ports.CheckinMarkPending},
		{"[簽到得魔力]", "faqlink", ports.CheckinMarkPending},
		{"[Attend get bonus]", "faqlink", ports.CheckinMarkPending},
		{"[签到已得0, 补签卡: 0]", "", ports.CheckinMarkAttended},
		{"[簽到已得123, 補簽卡: 4]", "", ports.CheckinMarkAttended},
		{"[Attend got: 4294967295, card: 12]", "", ports.CheckinMarkAttended},
	} {
		t.Run(tc.label, func(t *testing.T) {
			mark, err := parseCheckin([]byte(checkinFixture(`<a href="attendance.php" class="`+tc.class+`">`+tc.label+`</a>`)), base, "42")
			if err != nil || mark != tc.mark {
				t.Fatalf("mark=%q err=%v", mark, err)
			}
		})
	}
	for _, counter := range []string{"-1", "+1", "01", "1.0", "1,000", "18446744073709551616", "١", "", " 1"} {
		_, err := parseCheckin([]byte(checkinFixture(strings.Replace(checkinPositive, "got: 0", "got: "+counter, 1))), base, "42")
		if !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("counter %q accepted", counter)
		}
	}
}

func TestCheckinRejectsUntrustedDOM(t *testing.T) {
	base, _ := url.Parse("https://example.test/pt/index.php")
	for name, body := range map[string]string{
		"no explicit class":     checkinFixture(strings.Replace(checkinPositive, ` class=""`, "", 1)),
		"script duplicate":      checkinFixture(checkinPositive + `<script>` + checkinPending + `</script>`),
		"template duplicate":    checkinFixture(checkinPositive + `<template>` + checkinPending + `</template>`),
		"captcha with positive": checkinFixture(checkinPositive) + `<form><input name="imagehash"></form>`,
		"hidden class ancestor": checkinFixture(`<span class="hidden">` + checkinPositive + `</span>`),
		"missing":               checkinFixture(""),
		"announcement":          checkinFixture("") + checkinPositive,
		"foreign UID":           strings.Replace(checkinFixture(checkinPositive), "id=42", "id=43", 1),
		"two UID":               checkinFixture(checkinPositive + `<a href="userdetails.php?id=43">Other</a>`),
		"duplicate":             checkinFixture(checkinPositive + checkinPositive),
		"contradiction":         checkinFixture(checkinPositive + checkinPending),
		"different row":         strings.Replace(checkinFixture(checkinPositive), checkinPositive, `</span></td></tr><tr><td><span>`+checkinPositive, 1),
		"cross origin":          checkinFixture(strings.Replace(checkinPositive, "attendance.php", "https://evil.test/pt/attendance.php", 1)),
		"wrong path":            checkinFixture(strings.Replace(checkinPositive, "attendance.php", "/attendance.php", 1)),
		"query":                 checkinFixture(strings.Replace(checkinPositive, "attendance.php", "attendance.php?x=1", 1)),
		"empty query":           checkinFixture(strings.Replace(checkinPositive, "attendance.php", "attendance.php?", 1)),
		"fragment":              checkinFixture(strings.Replace(checkinPositive, "attendance.php", "attendance.php#", 1)),
		"userinfo":              checkinFixture(strings.Replace(checkinPositive, "attendance.php", "https://foo@example.test/pt/attendance.php", 1)),
		"hidden":                checkinFixture(strings.Replace(checkinPositive, `class=""`, `class="" hidden`, 1)),
		"hidden ancestor":       checkinFixture(`<span style="display:none">` + checkinPositive + `</span>`),
		"template":              checkinFixture(`<template>` + checkinPositive + `</template>`),
		"script":                checkinFixture(`<script>` + checkinPositive + `</script>`),
		"challenge":             checkinFixture(checkinPositive) + `<div id="challenge-form"></div>`,
		"duplicate attribute":   checkinFixture(strings.Replace(checkinPositive, `class=""`, `class="" class="faqlink"`, 1)),
		"wrong class":           checkinFixture(strings.Replace(checkinPositive, `class=""`, `class="faqlink"`, 1)),
		"extra class":           checkinFixture(strings.Replace(checkinPositive, `class=""`, `class="success"`, 1)),
		"pending without class": checkinFixture(strings.Replace(checkinPending, `class="faqlink"`, `class=""`, 1)),
		"nested text":           checkinFixture(strings.Replace(checkinPositive, "[Attend got: 0, card: 0]", "<b>[Attend got: 0, card: 0]</b>", 1)),
		"broken":                `<table id="info_block"><a href="attendance.php">[Attend got: 0, card: 0]`,
		"depth":                 checkinFixture(strings.Repeat("<span>", 260) + checkinPositive + strings.Repeat("</span>", 260)),
	} {
		t.Run(name, func(t *testing.T) {
			mark, err := parseCheckin([]byte(body), base, "42")
			if !errors.Is(err, ports.ErrBadResponse) || mark != "" {
				t.Fatalf("accepted mark=%q err=%v", mark, err)
			}
		})
	}
}

func TestCheckinRequestsAndClientIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		wantMethod, wantPath := http.MethodGet, "/pt/index.php"
		if n == 2 {
			wantMethod, wantPath = http.MethodPost, "/pt/attendance.php"
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != wantMethod || r.URL.RequestURI() != wantPath || len(body) != 0 || r.Header.Get("Cookie") != "session=explicit" || r.ProtoMajor != 1 || r.Header.Get("Idempotency-Key") != "" {
			t.Errorf("unexpected request: %s %s proto=%s body=%q", r.Method, r.URL.RequestURI(), r.Proto, body)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(w, &http.Cookie{Name: "injected", Value: "secret"})
		if n == 1 {
			_, _ = io.WriteString(w, checkinFixture(checkinPending))
		} else {
			_, _ = io.WriteString(w, checkinFixture(checkinPositive)+`<p>Already attended error</p>`)
		}
	}))
	defer server.Close()
	original := server.Client()
	original.Timeout = 3 * time.Second
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(server.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "jar", Value: "private"}})
	original.Jar = jar
	redirect := func(*http.Request, []*http.Request) error { return errors.New("borrowed") }
	original.CheckRedirect = redirect
	borrowedTransport := original.Transport
	c := NewCheckinClient(server.URL+"/pt/", "session=explicit", original, statsClock)
	obs, err := c.ObserveCheckin(context.Background(), "42")
	if err != nil || obs.Mark != ports.CheckinMarkPending || obs.AccountID != "42" || !obs.RequestedAt.Equal(statsClock.Now()) || !obs.ObservedAt.Equal(statsClock.Now()) {
		t.Fatalf("observe=%+v err=%v", obs, err)
	}
	result, err := c.SubmitCheckin(context.Background(), "42")
	if err != nil || result.State != ports.CheckinSubmissionAttended || result.Observation == nil || result.Observation.Mark != ports.CheckinMarkAttended {
		t.Fatalf("submit=%+v err=%v", result, err)
	}
	if calls.Load() != 2 || original.Transport != borrowedTransport || original.Jar != jar || original.Timeout != 3*time.Second || original.CheckRedirect(nil, nil) == nil || len(jar.Cookies(u)) != 1 {
		t.Fatal("borrowed client mutated or request replayed")
	}
}

func TestCheckinPreflightZeroIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name, base, cookie, uid string
		clock                   ports.Clock
		ctx                     context.Context
		custom                  bool
	}{
		{"clock", server.URL, "cookie=secret", "42", nil, context.Background(), false},
		{"cancel", server.URL, "cookie=secret", "42", statsClock, canceled, false},
		{"cookie", server.URL, "", "42", statsClock, context.Background(), false},
		{"CRLF", server.URL, "a=b\r\nx=y", "42", statsClock, context.Background(), false},
		{"UID", server.URL, "a=b", "042", statsClock, context.Background(), false},
		{"empty UID", server.URL, "a=b", "", statsClock, context.Background(), false},
		{"userinfo", "http://secret@localhost", "a=b", "42", statsClock, context.Background(), false},
		{"query", server.URL + "?secret=1", "a=b", "42", statsClock, context.Background(), false},
		{"fragment", server.URL + "#secret", "a=b", "42", statsClock, context.Background(), false},
		{"empty fragment", server.URL + "#", "a=b", "42", statsClock, context.Background(), false},
		{"path", server.URL + "/../pt", "a=b", "42", statsClock, context.Background(), false},
		{"transport", server.URL, "a=b", "42", statsClock, context.Background(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := server.Client()
			if tc.custom {
				client = &http.Client{Transport: accountTransport(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("secret") })}
			}
			c := NewCheckinClient(tc.base, tc.cookie, client, tc.clock)
			if _, err := c.ObserveCheckin(tc.ctx, tc.uid); err == nil {
				t.Fatal("observe accepted")
			}
			result, err := c.SubmitCheckin(tc.ctx, tc.uid)
			if err == nil || result.State != ports.CheckinSubmissionNotSent || strings.Contains(err.Error(), "secret") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("preflight performed %d I/O", calls.Load())
	}
}

func TestCheckinHTTPFailuresStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{401, ports.ErrAuthRequired}, {403, ports.ErrBlocked}, {429, nil}, {500, ports.ErrUnavailable}, {302, ports.ErrBadResponse}, {400, ports.ErrBadResponse},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "999999")
				w.Header().Set("Location", "/legacy.php")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "cookie=secret")
			}))
			defer server.Close()
			c := NewCheckinClient(server.URL, "cookie=secret", server.Client(), statsClock)
			result, err := c.SubmitCheckin(context.Background(), "42")
			rate, limited := ports.IsRateLimited(err)
			matches := errors.Is(err, tc.want)
			if tc.status == 429 {
				matches = limited && rate.RetryAfter == time.Hour
			}
			if result.State != ports.CheckinSubmissionUnknown || !matches || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestCheckinLostPostReplyNeverReplays(t *testing.T) {
	for _, tlsServer := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTP2 enabled TLS"}[tlsServer], func(t *testing.T) {
			var posts atomic.Int32
			var proto atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				proto.Store(int32(r.ProtoMajor))
				_, _ = io.ReadAll(r.Body)
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("HTTP/2 negotiated")
					return
				}
				conn, _, err := h.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			server.EnableHTTP2 = true
			if tlsServer {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			client := server.Client()
			original := client.Transport.(*http.Transport)
			h2 := original.ForceAttemptHTTP2
			c := NewCheckinClient(server.URL, "cookie=secret", client, statsClock)
			result, err := c.SubmitCheckin(context.Background(), "42")
			if result.State != ports.CheckinSubmissionUnknown || !errors.Is(err, ports.ErrUnavailable) || posts.Load() != 1 || proto.Load() != 1 {
				t.Fatalf("result=%+v err=%v posts=%d proto=%d", result, err, posts.Load(), proto.Load())
			}
			if original.ForceAttemptHTTP2 != h2 {
				t.Fatal("borrowed transport mutated")
			}
		})
	}
}

func TestCheckinResponseValidation(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String(checkinFixture(`<a href="attendance.php" class="">[签到已得0, 补签卡: 0]</a>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mime, body string
		good             bool
	}{
		{"BOM", "text/html; charset=utf-8", "\xef\xbb\xbf" + checkinFixture(checkinPositive), true},
		{"GBK", "text/html; charset=gbk", gbk, true},
		{"MIME", "application/json", checkinFixture(checkinPositive), false},
		{"unknown charset", "text/html; charset=not-real", checkinFixture(checkinPositive), false},
		{"NUL", "text/html", checkinFixture(checkinPositive) + "\x00", false},
		{"invalid UTF8", "text/html", checkinFixture(checkinPositive) + "\xff", false},
		{"raw limit", "text/html", strings.Repeat("x", (2<<20)+1), false},
		{"decoded limit", "text/html; charset=windows-1252", strings.Repeat("\x80", (2<<20)-1), false},
		{"pending POST", "text/html", checkinFixture(checkinPending), false},
		{"captcha", "text/html", `<form><input name="imagehash"><img src="image.php"></form>`, false},
		{"bad HTML", "text/html", "<html>error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			result, err := NewCheckinClient(server.URL, "a=b", server.Client(), statsClock).SubmitCheckin(context.Background(), "42")
			if tc.good {
				if err != nil || result.State != ports.CheckinSubmissionAttended {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || result.State != ports.CheckinSubmissionUnknown {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCheckinCancellationAfterDispatch(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, checkinFixture(checkinPositive))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var result ports.CheckinSubmission
	var err error
	go func() {
		defer close(done)
		result, err = NewCheckinClient(server.URL, "a=b", server.Client(), statsClock).SubmitCheckin(ctx, "42")
	}()
	<-arrived
	cancel()
	close(release)
	<-done
	if result.State != ports.CheckinSubmissionUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCheckinReadBodyClosure(t *testing.T) {
	// The decoder never owns the body; the request scope closes it on every exit.
	// Verify network connection closure for status, malformed body and valid body.
	for _, status := range []int{200, 403} {
		var closed atomic.Int32
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "bad html")
		}))
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closed.Add(1)
			}
		}
		server.Start()
		_, err := NewCheckinClient(server.URL, "a=b", server.Client(), statsClock).SubmitCheckin(context.Background(), "42")
		server.Close()
		if err == nil || closed.Load() != 1 {
			t.Fatalf("status=%d err=%v closed=%d", status, err, closed.Load())
		}
	}
}

func TestCheckinLoginRedirectAndClockWindow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "login.php")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	result, err := NewCheckinClient(server.URL, "a=b", server.Client(), statsClock).SubmitCheckin(context.Background(), "42")
	if result.State != ports.CheckinSubmissionUnknown || !errors.Is(err, ports.ErrAuthRequired) || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

type checkinWindowClock struct{ calls atomic.Int32 }

func (c *checkinWindowClock) Now() time.Time {
	return statsClock.Now().Add(time.Duration(c.calls.Add(1)) * time.Second)
}
func (c *checkinWindowClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func TestCheckinRootRequestWindowAndRetryDate(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/index.php" || r.Method != http.MethodGet {
			t.Error("wrong observation target")
		}
		if calls.Add(1) == 2 {
			w.Header().Set("Retry-After", statsClock.Now().Add(90*time.Second).Format(http.TimeFormat))
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, checkinFixture(checkinPositive))
	}))
	defer server.Close()
	clock := &checkinWindowClock{}
	obs, err := NewCheckinClient(server.URL, "a=b", nil, clock).ObserveCheckin(context.Background(), "42")
	if err != nil || !obs.RequestedAt.Equal(statsClock.Now().Add(time.Second)) || !obs.ObservedAt.Equal(statsClock.Now().Add(2*time.Second)) {
		t.Fatalf("observation=%+v err=%v", obs, err)
	}
	_, err = NewCheckinClient(server.URL, "a=b", server.Client(), statsClock).ObserveCheckin(context.Background(), "42")
	rate, ok := ports.IsRateLimited(err)
	if !ok || rate.RetryAfter != 90*time.Second {
		t.Fatalf("retry date=%v", err)
	}
}
