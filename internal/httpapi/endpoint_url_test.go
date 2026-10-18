package httpapi

import (
	"net/url"
	"strings"
	"testing"
)

func TestEndpointDisplayURLRedactsSecretsAndPreservesBenignQuery(t *testing.T) {
	raw := "https://legacy-user:private-userinfo@example.test/base?category=movie&page=2&page=3&api%5fkey=private-api&ApiKey=private-api2&passkey=private-pass&token=private-token&Cookie=private-cookie&password=private-password&download_url=https%3A%2F%2Ftracker.test%2Fprivate-link&auth=private-auth#private-fragment"
	raw += "&passkey=private-passkey&token=private-token&Cookie=private-cookie&password=private-password&download_url=private-link&auth=private-auth#private-fragment"
	got := endpointDisplayURL(raw)
	if strings.Contains(got, "private-") || strings.Contains(got, "legacy-user") {
		t.Fatalf("secret in display: %s", got)
	}
	u, err := url.Parse(got)
	if err != nil || u.User != nil || u.Path != "/base" || u.Query().Get("category") != "movie" || len(u.Query()["page"]) != 2 {
		t.Fatalf("safe endpoint identity lost: %s", got)
	}
	for _, key := range []string{"api_key", "ApiKey", "passkey", "token", "Cookie", "password", "download_url", "auth"} {
		if u.Query().Get(key) != "***" {
			t.Fatalf("query %s not redacted", key)
		}
	}
	if got = endpointDisplayURL("https://example.test/base?token=private-token&broken=%zz"); strings.Contains(got, "private-") {
		t.Fatal("malformed query disclosed")
	}
	if got = endpointDisplayURL("https://example.test/%zz?token=private-token"); got != "" {
		t.Fatal("invalid URL disclosed")
	}
	if got = endpointDisplayURL("https://example.test/base?page=2&category=movie"); got != "https://example.test/base?page=2&category=movie" {
		t.Fatalf("benign URL changed: %s", got)
	}
}
