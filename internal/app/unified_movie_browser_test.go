package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type unifiedMetadataTransport struct{ base *url.URL }

func (t unifiedMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.base.Scheme
	u.Host = t.base.Host
	copy.URL = &u
	copy.Host = u.Host
	return http.DefaultTransport.RoundTrip(copy)
}

func TestUnifiedMovieCookieWorkflow(t *testing.T)        { runUnifiedMovieCookieWorkflow(t, false) }
func TestUnifiedSubscriptionCookieWorkflow(t *testing.T) { runUnifiedMovieCookieWorkflow(t, true) }

func runUnifiedMovieCookieWorkflow(t *testing.T, subscriptionMode bool) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	runFullMoviePipelineSource(t, true, "", "unified", func(f *organizationFixture, actor *ports.Actor, _ uuid.UUID, a *App) func() {
		ctx := context.Background()
		hashed, e := crypto.HashPassword("unified-login-password")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE app_user SET password_hash=$2,role='admin' WHERE id=$1", uuid.MustParse(actor.ID), hashed); e != nil {
			t.Fatal(e)
		}
		metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("api_key") != "unified-metadata-key" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(r.URL.Path, "/3/movie/1") {
				_, _ = io.WriteString(w, `{"id":1,"title":"Movie","original_title":"Movie","release_date":"2020-01-01","overview":"Native metadata fixture movie","genres":[],"imdb_id":"tt0000001"}`)
				return
			}
			_, _ = io.WriteString(w, `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":1,"title":"Movie","original_title":"Movie","media_type":"movie","release_date":"2020-01-01","overview":"Native metadata fixture movie"}]}`)
		}))
		t.Cleanup(metadata.Close)
		base, _ := url.Parse(metadata.URL)
		a.metadataHTTPClient = &http.Client{Transport: unifiedMetadataTransport{base: base}, Timeout: 10 * time.Second}
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		a.cfg.Server.HTTPAddr = address
		browserCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
		t.Cleanup(cancel)
		cdp := startOwnedRevisionChrome(t, browserCtx)
		script := "tests/unified-movie-browser.mjs"
		if subscriptionMode {
			script = "tests/unified-subscription-browser.mjs"
		}
		cmd := exec.CommandContext(browserCtx, "node", script)
		cmd.Dir = "../../web"
		cmd.Env = append(os.Environ(), "PULSE_UI_CDP_URL="+cdp, "PULSE_UNIFIED_BASE=http://"+address, "PULSE_UNIFIED_ROOT="+f.root.String(), "PULSE_UNIFIED_CLIENT="+f.client.String())
		result := make(chan struct{}, 1)
		var output []byte
		var commandErr error
		go func() { output, commandErr = cmd.CombinedOutput(); close(result) }()
		t.Cleanup(func() {
			cancel()
			<-result
			if commandErr != nil {
				t.Logf("unified browser failure: %v %s", commandErr, output)
			}
		})
		return func() {
			select {
			case <-result:
			case <-browserCtx.Done():
				t.Fatal("unified browser deadline")
			}
			if commandErr != nil {
				t.Fatalf("unified browser: %v %s", commandErr, output)
			}
			t.Log(string(output))
			var receipts, requirements int
			if e := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM idempotency_record WHERE response_body ? 'candidate_id' AND response_body ? 'root_id'),(SELECT count(*) FROM download_requirement WHERE state='imported')`).Scan(&receipts, &requirements); e != nil {
				t.Fatal(e)
			}
			expectedReceipts := 1
			if subscriptionMode {
				expectedReceipts = 0
			}
			if receipts != expectedReceipts || requirements != 1 {
				t.Fatalf("native receipt/import mismatch %d %d", receipts, requirements)
			}
		}
	})
}
