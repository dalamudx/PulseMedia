package cookies

import (
	"errors"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

var (
	ErrInvalidTarget   = errors.New("invalid configured cookie target")
	ErrInvalidSnapshot = errors.New("invalid or oversized cookie observations")
	ErrAmbiguous       = errors.New("matching cookies have conflicting values")
)

type Cookie struct {
	Name, Value, Domain, Path string
	HostOnly, Secure, Session *bool
	ExpirationDate            *float64
	Partitioned               bool
}

func (Cookie) String() string { return "Cookie{redacted}" }

type Selection struct {
	Header  string
	Count   int
	Ignored int
	// ValidUntil is the earliest matched persistent-cookie expiry; nil means
	// session cookies only, without asserting a future lifetime.
	ValidUntil *time.Time
}

func (Selection) String() string { return "CookieSelection{redacted}" }

// SelectSharedOrigin restricts a shared site credential to cookies that may be
// sent to every path on the configured origin. Per-path cookies cannot be widened
// merely because the indexer's configured URL happened to match that path.
func SelectSharedOrigin(target string, observed []Cookie, now time.Time) (Selection, error) {
	endpoint, err := url.Parse(target)
	if err != nil || endpoint == nil || endpoint.User != nil || endpoint.Fragment != "" {
		return Selection{}, ErrInvalidTarget
	}
	return Select(endpoint.Scheme+"://"+endpoint.Host+"/", observed, now)
}

// Select formats a credential for one already configured HTTP endpoint. No
// result is permission to clear a credential, create a site, or change an owner.
// All browser scope facts must be explicit; unknown scope is never widened.
func Select(target string, observed []Cookie, now time.Time) (Selection, error) {
	endpoint, err := url.Parse(target)
	if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" || now.IsZero() {
		return Selection{}, ErrInvalidTarget
	}
	host, ok := canonicalHost(endpoint.Hostname())
	if !ok {
		return Selection{}, ErrInvalidTarget
	}
	if len(observed) > 10000 {
		return Selection{}, ErrInvalidSnapshot
	}
	path := endpoint.EscapedPath()
	if path == "" {
		path = "/"
	}
	values := map[string]string{}
	ignored := 0
	var validUntil *time.Time
	epoch := float64(now.Unix()) + float64(now.Nanosecond())/1e9
	for _, cookie := range observed {
		if !validPair(cookie.Name, cookie.Value) {
			return Selection{}, ErrInvalidSnapshot
		}
		if !matches(cookie, host, path, endpoint.Scheme == "https", epoch) {
			ignored++
			continue
		}
		if value, exists := values[cookie.Name]; exists && value != cookie.Value {
			return Selection{}, ErrAmbiguous
		}
		values[cookie.Name] = cookie.Value
		if !*cookie.Session {
			seconds, fraction := math.Modf(*cookie.ExpirationDate)
			expires := time.Unix(int64(seconds), int64(fraction*1e9)).UTC()
			if validUntil == nil || expires.Before(*validUntil) {
				validUntil = &expires
			}
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var header strings.Builder
	for _, name := range names {
		if header.Len() > 0 {
			header.WriteString("; ")
		}
		header.WriteString(name)
		header.WriteByte('=')
		header.WriteString(values[name])
		if header.Len() > 32768 {
			return Selection{}, ErrInvalidSnapshot
		}
	}
	return Selection{Header: header.String(), Count: len(names), Ignored: ignored, ValidUntil: validUntil}, nil
}

func matches(c Cookie, host, path string, https bool, now float64) bool {
	if c.HostOnly == nil || c.Secure == nil || c.Session == nil || c.Partitioned || (*c.Secure && !https) || !strings.HasPrefix(c.Path, "/") || strings.ContainsAny(c.Path, "\x00\r\n") {
		return false
	}
	domain := c.Domain
	if *c.HostOnly && strings.HasPrefix(domain, ".") {
		return false
	}
	if !*c.HostOnly {
		domain = strings.TrimPrefix(domain, ".")
	}
	domain, ok := canonicalHost(domain)
	if !ok {
		return false
	}
	if *c.HostOnly {
		if host != domain {
			return false
		}
	} else {
		suffix, _ := publicsuffix.PublicSuffix(domain)
		if net.ParseIP(domain) != nil || domain == suffix || (host != domain && !strings.HasSuffix(host, "."+domain)) {
			return false
		}
	}
	if path != c.Path && (!strings.HasPrefix(path, c.Path) || (!strings.HasSuffix(c.Path, "/") && path[len(c.Path)] != '/')) {
		return false
	}
	if strings.HasPrefix(c.Name, "__Secure-") && (!*c.Secure || !https) {
		return false
	}
	if strings.HasPrefix(c.Name, "__Host-") && (!*c.Secure || !https || !*c.HostOnly || c.Path != "/") {
		return false
	}
	if *c.Session {
		return c.ExpirationDate == nil
	}
	if c.ExpirationDate == nil || math.IsNaN(*c.ExpirationDate) || math.IsInf(*c.ExpirationDate, 0) || *c.ExpirationDate > 253402300799 {
		return false
	}
	return *c.ExpirationDate > now
}

func canonicalHost(host string) (string, bool) {
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return "", false
	}
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return "", false
			}
		}
	}
	return host, true
}

func validPair(name, value string) bool {
	if name == "" || len(name) > 1024 || len(value) > 16384 {
		return false
	}
	for _, r := range name {
		if r < 33 || r > 126 || strings.ContainsRune("()<>@,;:\\\"/[]?={} ", r) {
			return false
		}
	}
	for _, r := range value {
		if r < 33 || r > 126 || strings.ContainsRune("\",;\\", r) {
			return false
		}
	}
	return true
}
