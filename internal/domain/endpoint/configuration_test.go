package endpoint

import (
	"net/url"
	"strings"
	"testing"
)

func TestDisplayURL(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://example.test/base?page=2&category=movie", "https://example.test/base?page=2&category=movie"},
		{"https://user:private@example.test/base?token=private&category=movie#private", "https://example.test/base?category=movie&token=%2A%2A%2A"},
		{"https://example.test/base?token=private&bad=%zz", "https://example.test/base"},
		{"https://example.test/base?token=private;bad=x", "https://example.test/base"},
		{"https://example.test/%zz?token=private", ""},
		{"ftp://example.test/path", ""},
		{"/relative?token=private", ""},
		{"http://127.0.0.1:8080/?x=1", "http://127.0.0.1:8080/?x=1"},
		{"https://example.test/path?x=1#", "https://example.test/path?x=1#"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := DisplayURL(tc.raw); got != tc.want {
				t.Fatalf("projection mismatch: %q", got)
			}
		})
	}
}

func TestDisplayURLSensitiveKeys(t *testing.T) {
	keys := []string{"password", "passwd", "TOKEN", "api_key", "Api-Key", "api.key", "passkey", "Cookie", "auth", "credential", "dsn", "download_url", "magnet", "url", "link", "callbackURL", "nextLink", "secret"}
	q := url.Values{"page": {"2", "3"}, "category": {"movie"}}
	for _, key := range keys {
		q[key] = []string{"private"}
	}
	got := DisplayURL("https://user:private@example.test/base?" + q.Encode() + "#private")
	if strings.Contains(got, "private") || strings.Contains(got, "user") {
		t.Fatal("projection disclosed credentials")
	}
	u, err := url.Parse(got)
	if err != nil || u.User != nil || len(u.Query()["page"]) != 2 || u.Query().Get("category") != "movie" {
		t.Fatal("benign query lost")
	}
	for _, key := range keys {
		if u.Query().Get(key) != "***" {
			t.Fatalf("unmasked key %s", key)
		}
	}
}

func TestURLHasMask(t *testing.T) {
	for _, raw := range []string{"https://x/?token=***", "https://x/?token=%2A%2a%2A", "https://x/%zz"} {
		if !URLHasMask(raw) {
			t.Fatal("mask accepted")
		}
	}
	for _, raw := range []string{"https://x/?page=2", "https://x/?token=%252A%252A%252A", "http://[::1]:8080/"} {
		if URLHasMask(raw) {
			t.Fatal("complete URL rejected")
		}
	}
}
