package cookies_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/cookies"
)

func flag(v bool) *bool         { return &v }
func number(v float64) *float64 { return &v }
func scoped() cookies.Cookie {
	return cookies.Cookie{Name: "sid", Value: "owned-fixture", Domain: "tracker.example", Path: "/", HostOnly: flag(true), Secure: flag(true), Session: flag(true)}
}

func TestCookieScopeBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name, target string
		change       func(*cookies.Cookie)
		match        bool
	}{
		{"exact", "https://tracker.example/api", nil, true},
		{"canonical-case", "https://TRACKER.EXAMPLE/api", nil, true},
		{"port-is-not-cookie-scope", "https://tracker.example:9443/api", nil, true},
		{"host-only-no-child", "https://sub.tracker.example/api", nil, false},
		{"domain-child", "https://sub.tracker.example/api", func(c *cookies.Cookie) { c.Domain = ".tracker.example"; c.HostOnly = flag(false) }, true},
		{"host-only-leading-dot", "https://tracker.example/api", func(c *cookies.Cookie) { c.Domain = ".tracker.example" }, false},
		{"suffix-confusion", "https://eviltracker.example/api", func(c *cookies.Cookie) { c.HostOnly = flag(false) }, false},
		{"foreign-child", "https://tracker.example.attacker.test/api", func(c *cookies.Cookie) { c.HostOnly = flag(false) }, false},
		{"public-suffix", "https://tracker.com/api", func(c *cookies.Cookie) { c.Domain = ".com"; c.HostOnly = flag(false) }, false},
		{"compound-public-suffix", "https://tracker.co.uk/api", func(c *cookies.Cookie) { c.Domain = ".co.uk"; c.HostOnly = flag(false) }, false},
		{"private-public-suffix", "https://tracker.github.io/api", func(c *cookies.Cookie) { c.Domain = ".github.io"; c.HostOnly = flag(false) }, false},
		{"unknown-domain", "https://tracker.example/api", func(c *cookies.Cookie) { c.Domain = "" }, false},
		{"unknown-path", "https://tracker.example/api", func(c *cookies.Cookie) { c.Path = "" }, false},
		{"unknown-host-only", "https://tracker.example/api", func(c *cookies.Cookie) { c.HostOnly = nil }, false},
		{"unknown-secure", "https://tracker.example/api", func(c *cookies.Cookie) { c.Secure = nil }, false},
		{"unknown-session", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = nil }, false},
		{"secure-http", "http://tracker.example/api", nil, false},
		{"plain-http", "http://tracker.example/api", func(c *cookies.Cookie) { c.Secure = flag(false) }, true},
		{"path-exact", "https://tracker.example/api", func(c *cookies.Cookie) { c.Path = "/api" }, true},
		{"path-child", "https://tracker.example/api/details", func(c *cookies.Cookie) { c.Path = "/api" }, true},
		{"path-not-substring", "https://tracker.example/api2", func(c *cookies.Cookie) { c.Path = "/api" }, false},
		{"encoded-separator", "https://tracker.example/api%2fprivate", func(c *cookies.Cookie) { c.Path = "/api" }, false},
		{"query-is-not-path", "https://tracker.example/else?redirect=/api", func(c *cookies.Cookie) { c.Path = "/api" }, false},
		{"future-expiry", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false); c.ExpirationDate = number(float64(now.Unix()) + 1) }, true},
		{"expiry-at-now", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false); c.ExpirationDate = number(float64(now.Unix())) }, false},
		{"expired", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false); c.ExpirationDate = number(float64(now.Unix()) - 1) }, false},
		{"unknown-expiry", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false) }, false},
		{"nan-expiry", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false); c.ExpirationDate = number(math.NaN()) }, false},
		{"infinite-expiry", "https://tracker.example/api", func(c *cookies.Cookie) { c.Session = flag(false); c.ExpirationDate = number(math.Inf(1)) }, false},
		{"contradictory-session", "https://tracker.example/api", func(c *cookies.Cookie) { c.ExpirationDate = number(float64(now.Unix()) + 1) }, false},
		{"partitioned", "https://tracker.example/api", func(c *cookies.Cookie) { c.Partitioned = true }, false},
		{"secure-prefix-invalid", "https://tracker.example/api", func(c *cookies.Cookie) { c.Name = "__Secure-sid"; c.Secure = flag(false) }, false},
		{"host-prefix-valid", "https://tracker.example/api", func(c *cookies.Cookie) { c.Name = "__Host-sid" }, true},
		{"host-prefix-domain", "https://tracker.example/api", func(c *cookies.Cookie) { c.Name = "__Host-sid"; c.HostOnly = flag(false) }, false},
		{"host-prefix-path", "https://tracker.example/api", func(c *cookies.Cookie) { c.Name = "__Host-sid"; c.Path = "/api" }, false},
		{"ip-host-only", "https://127.0.0.1/api", func(c *cookies.Cookie) { c.Domain = "127.0.0.1" }, true},
		{"ip-domain-cookie", "https://127.0.0.1/api", func(c *cookies.Cookie) { c.Domain = "127.0.0.1"; c.HostOnly = flag(false) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := scoped()
			if tc.change != nil {
				tc.change(&c)
			}
			result, err := cookies.Select(tc.target, []cookies.Cookie{c}, now)
			if err != nil || (result.Count == 1) != tc.match {
				t.Fatal("scope admission mismatch", err)
			}
			if !tc.match && (result.Header != "" || result.Ignored != 1) {
				t.Fatal("unknown scope produced a credential")
			}
		})
	}
}

func TestCookieSelectionConflictsAndStableEvidence(t *testing.T) {
	now := time.Now()
	a := scoped()
	a.Name = "a"
	b := scoped()
	b.Name = "b"
	input := []cookies.Cookie{b, a, a}
	original := append([]cookies.Cookie(nil), input...)
	result, err := cookies.Select("https://tracker.example/api", input, now)
	if err != nil || result.Header != "a=owned-fixture; b=owned-fixture" || result.Count != 2 || !reflect.DeepEqual(input, original) {
		t.Fatal("selection changed observations or lacked deterministic deduplication")
	}
	if strings.Contains(fmt.Sprint(result), "owned-fixture") || strings.Contains(fmt.Sprint(a), "owned-fixture") {
		t.Fatal("formatting exposed credentials")
	}
	a.Value = "conflict"
	a.Path = "/api"
	if result, err = cookies.Select("https://tracker.example/api", []cookies.Cookie{input[1], a}, now); !errors.Is(err, cookies.ErrAmbiguous) || result.Header != "" {
		t.Fatal("a conflicting cookie was selected arbitrarily")
	}
	result, err = cookies.Select("https://other.example/api", input, now)
	if err != nil || result.Count != 0 || result.Header != "" {
		t.Fatal("unmatched data created clearing authority")
	}
}

func TestCookieSelectionInvalidTargetsAndHeaderBounds(t *testing.T) {
	for _, target := range []string{"/relative", "ftp://tracker.example/api", "https://private:secret@tracker.example/api", "https://tracker.example./api", "https://tracker.example/api#secret", "https://媒体.example/api"} {
		if result, err := cookies.Select(target, []cookies.Cookie{scoped()}, time.Now()); !errors.Is(err, cookies.ErrInvalidTarget) || result.Header != "" {
			t.Fatal("unsafe target accepted")
		}
	}
	if _, err := cookies.Select("https://tracker.example/api", nil, time.Time{}); !errors.Is(err, cookies.ErrInvalidTarget) {
		t.Fatal("missing clock accepted")
	}
	a := scoped()
	a.Value = "injected; header=bad"
	if _, err := cookies.Select("https://tracker.example/api", []cookies.Cookie{a}, time.Now()); !errors.Is(err, cookies.ErrInvalidSnapshot) {
		t.Fatal("header injection accepted")
	}
	a = scoped()
	a.Value = strings.Repeat("x", 16384)
	b := a
	b.Name = "other"
	if result, err := cookies.Select("https://tracker.example/api", []cookies.Cookie{a, b}, time.Now()); !errors.Is(err, cookies.ErrInvalidSnapshot) || result.Header != "" {
		t.Fatal("oversized header was published")
	}
	if _, err := cookies.Select("https://tracker.example/api", make([]cookies.Cookie, 10001), time.Now()); !errors.Is(err, cookies.ErrInvalidSnapshot) {
		t.Fatal("unbounded observation list accepted")
	}
}

func TestCookieSharedOriginDoesNotWidenPath(t *testing.T) {
	session := scoped()
	specific := scoped()
	specific.Name = "private-path"
	specific.Path = "/api"
	result, err := cookies.SelectSharedOrigin("https://tracker.example/api/details?key=private", []cookies.Cookie{session, specific}, time.Now())
	if err != nil || result.Header != "sid=owned-fixture" || result.Count != 1 || result.Ignored != 1 {
		t.Fatal("path-limited cookie escaped into the shared credential", err)
	}
	only, err := cookies.SelectSharedOrigin("https://tracker.example/api", []cookies.Cookie{specific}, time.Now())
	if err != nil || only.Header != "" || only.Count != 0 {
		t.Fatal("partial path cookie authorized replacing a site credential", err)
	}
}

func TestCookieSelectionCarriesEarliestExpiry(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	a := scoped()
	a.Session = flag(false)
	a.ExpirationDate = number(float64(now.Unix()) + 12.5)
	b := a
	b.Name = "later"
	b.ExpirationDate = number(float64(now.Unix()) + 90)
	session := scoped()
	session.Name = "session"
	result, err := cookies.SelectSharedOrigin("https://tracker.example", []cookies.Cookie{b, session, a}, now)
	if err != nil || result.ValidUntil == nil || !result.ValidUntil.Equal(now.Add(12500*time.Millisecond)) {
		t.Fatal("earliest native expiry was lost", err)
	}
	rejected, err := cookies.SelectSharedOrigin("https://tracker.example", []cookies.Cookie{a}, *result.ValidUntil)
	if err != nil || rejected.Count != 0 || rejected.ValidUntil != nil {
		t.Fatal("cookie remained eligible at its commit expiry", err)
	}
	sessionOnly, err := cookies.SelectSharedOrigin("https://tracker.example", []cookies.Cookie{session}, now)
	if err != nil || sessionOnly.ValidUntil != nil {
		t.Fatal("session cookie acquired an invented deadline", err)
	}
	a.ExpirationDate = number(1e300)
	invalid, err := cookies.SelectSharedOrigin("https://tracker.example", []cookies.Cookie{a}, now)
	if err != nil || invalid.Count != 0 {
		t.Fatal("unrepresentable cookie expiry accepted", err)
	}
}
