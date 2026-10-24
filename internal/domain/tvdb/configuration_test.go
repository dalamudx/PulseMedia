package tvdb_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/tvdb"
)

func TestTVDBCredentialsAndSwitch(t *testing.T) {
	for _, raw := range []string{`{"api_key":"key"}`, `{"api_key":"key","pin":"owned-pin"}`, `{"pin":"","api_key":"key"}`} {
		credentials, err := tvdb.DecodeCredentials([]byte(raw))
		if err != nil || credentials.APIKey != "key" {
			t.Fatalf("valid credentials rejected: %v", err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"api_key":null}`, `{"api_key":"key","pin":null}`, `{"api_key":"key","PIN":"x"}`, `{"api_key":"key","api_key":"different"}`, `{"api_key":"key","pin":"x","pin":"y"}`, `{"api_key":" key"}`, `{"api_key":"key","url":"https://wrong.example"}`, `{"api_key":"key"} {}`, `{"api_key":"` + strings.Repeat("x", 513) + `"}`, `{"api_key":"key","pin":"bad\nline"}`} {
		if _, err := tvdb.DecodeCredentials([]byte(raw)); err == nil {
			t.Error("malformed credential object accepted")
		}
	}
	for _, raw := range []string{`{"enabled":true}`, `{"enabled":false}`} {
		if _, err := tvdb.DecodeEnabled([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":false,"api_key":"key"}`, `{"enabled":false,"enabled":true}`, `{"Enabled":false}`, `{"enabled":false} {}`} {
		if _, err := tvdb.DecodeEnabled([]byte(raw)); err == nil {
			t.Error("malformed switch accepted")
		}
	}
}
