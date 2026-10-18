package torznab_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTorznabCapabilitiesEvidenceAndFailures(t *testing.T) {
	valid := `<caps><limits default="25" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid, q"/><tv-search available="no" supportedParams="q,tvdbid"/></searching></caps>`
	for _, tt := range []struct {
		name, body string
		want       error
	}{
		{"valid", valid, nil},
		{"optional_limits", `<caps><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`, nil},
		{"unavailable_is_evidence", `<caps><searching><movie-search available="no"/></searching></caps>`, nil},
		{"rss_is_not_caps", `<rss><channel/></rss>`, ports.ErrBadResponse},
		{"missing_searching", `<caps/>`, ports.ErrBadResponse},
		{"invalid_available", `<caps><searching><movie-search available="perhaps"/></searching></caps>`, ports.ErrBadResponse},
		{"negative_limit", strings.Replace(valid, `default="25"`, `default="-1"`, 1), ports.ErrBadResponse},
		{"overflow_limit", strings.Replace(valid, `max="100"`, `max="2147483648"`, 1), ports.ErrBadResponse},
		{"inverted_limits", strings.Replace(valid, `default="25"`, `default="101"`, 1), ports.ErrBadResponse},
		{"api_auth", `<error code="100" description="apikey=synthetic-caps-secret"/>`, ports.ErrAuthRequired},
		{"api_unknown", `<error code="299" description="apikey=synthetic-caps-secret"/>`, ports.ErrBadResponse},
		{"malformed", `<caps>`, ports.ErrBadResponse},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("t") != "caps" || r.URL.Query().Get("apikey") != "synthetic-caps-secret" || r.URL.Query().Has("q") {
					t.Error("incorrect capabilities request")
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			got, e := torznab.NewClient(server.URL, "synthetic-caps-secret", server.Client()).Capabilities(context.Background())
			if !errors.Is(e, tt.want) {
				t.Fatal("wrong error class", e)
			}
			if e != nil && strings.Contains(e.Error(), "synthetic") {
				t.Fatal("error leaked credentials")
			}
			if tt.name == "valid" && (got.MaxLimit != 100 || got.DefaultLimit != 25 || !got.MovieSearch.Available || len(got.MovieSearch.SupportedParams) != 2 || got.TVSearch.Available) {
				t.Fatal("capabilities mismatch", got)
			}
		})
	}
}
func TestTorznabSearchXMLAuthenticationIsSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<error code="100" description="passkey=synthetic-secret"/>`)
	}))
	defer server.Close()
	got, e := torznab.NewClient(server.URL, "synthetic-secret", server.Client()).Search(context.Background(), "movie", nil, 0, 1)
	if got != nil || !errors.Is(e, ports.ErrAuthRequired) || strings.Contains(e.Error(), "synthetic") {
		t.Fatal("unsafe API error", e)
	}
}
