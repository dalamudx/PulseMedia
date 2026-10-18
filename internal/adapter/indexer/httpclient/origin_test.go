package httpclient_test

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/httpclient"
	"testing"
)

func TestConfiguredOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, base, request string
		allowed             bool
	}{
		{"explicit loopback", "http://127.0.0.1:8080", "http://127.0.0.1:8080/download?passkey=synthetic", true},
		{"explicit private", "http://192.168.1.9:8080/api", "http://192.168.1.9:8080/file", true},
		{"default https port", "https://Indexer.example/api", "https://indexer.example:443/file", true},
		{"ipv6", "http://[::1]:8080", "http://[::1]:8080/file", true},
		{"other host", "https://indexer.example", "https://other.example/file", false},
		{"subdomain", "https://indexer.example", "https://other.indexer.example/file", false},
		{"other port", "http://localhost:8080", "http://localhost:8081/file", false},
		{"downgrade", "https://indexer.example", "http://indexer.example/file", false},
		{"userinfo", "https://indexer.example", "https://user:secret@indexer.example/file", false},
		{"bad base", "https://user:secret@indexer.example", "https://indexer.example/file", false},
		{"relative", "https://indexer.example", "/file", false},
		{"fragment", "https://indexer.example", "https://indexer.example/file#secret", false},
		{"file scheme", "https://indexer.example", "file:///etc/passwd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpclient.AllowsURL(tc.base, tc.request); got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
}
