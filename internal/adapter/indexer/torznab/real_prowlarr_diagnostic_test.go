package torznab_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"
)

// Replay only the public Archive search found in the bounded management log.
// Never print provider messages, URLs, credentials, or arbitrary query values.
func TestM4RealProwlarrBoundedDiagnostic(t *testing.T) {
	if os.Getenv("PULSE_M4_PROWLARR_DIAGNOSE") != "1" {
		t.Skip("explicit diagnostics required")
	}
	p := openM4Prowlarr(t)
	var logs struct {
		Records []struct {
			Message string `json:"message"`
			Exception string `json:"exception"`
		} `json:"records"`
	}
	if json.Unmarshal(p.read(t, "/api/v1/log?page=1&pageSize=50&sortKey=time&sortDirection=descending"), &logs) != nil {
		t.Fatal("invalid log snapshot")
	}
	for _, record := range logs.Records {
		for _, raw := range regexp.MustCompile(`https?://[^\s<>"']+`).FindAllString(record.Message+" "+record.Exception, -1) {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host != "archive.org" || u.User != nil || u.Fragment != "" || u.Path != "/advancedsearch.php" {
				continue
			}
			query := u.Query()
			for key := range query {
				if key != "q" && key != "fl[]" && key != "sort" && key != "rows" && key != "output" {
					t.Fatal("unexpected outbound key; replay refused")
				}
			}
			if query.Get("q") != `identifier:BigBuckBunny_328 AND format:("Archive BitTorrent") AND mediatype:(movies)` {
				continue
			}
			t.Logf("M4_OUTBOUND keys=%d exact_query=true descending_publicdate=%t", len(query), query.Get("sort") == "-publicdate")
			start := time.Now()
			req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				t.Fatal("public replay construction failed")
			}
			res, err := p.client.Do(req)
			if err != nil {
				t.Fatalf("public replay failed elapsed=%s (URL withheld)", time.Since(start))
			}
			data, readErr := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("public replay HTTP %d elapsed=%s", res.StatusCode, time.Since(start))
			}
			var result struct {
				Response struct {
					NumFound int `json:"numFound"`
				} `json:"response"`
			}
			if readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &result) != nil || result.Response.NumFound != 1 {
				t.Fatal("public replay did not establish bounded unique result")
			}
			t.Logf("M4_PUBLIC_REPLAY elapsed=%s status=%d count=%d", time.Since(start), res.StatusCode, result.Response.NumFound)
			return
		}
	}
	t.Fatal("bounded log contains no exact public search evidence")
}
