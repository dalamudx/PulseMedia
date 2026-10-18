package app

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestTorznabCapabilitiesBrowserHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, dsn := bootstrapTestPool(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Database.URL = dsn
	a, e := New(cfg, WithDBPool(pool))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := a.Close(); e != nil {
			t.Error(e)
		}
	})
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	user, _, e := as.SetupAdmin(ctx, "caps-ui-admin", "SyntheticCapsFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	_, session, e := as.Login(ctx, user.Username, "SyntheticCapsFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	var caps, searches atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "synthetic-caps-browser-key" {
			t.Error("missing credential")
		}
		if r.URL.Query().Get("t") == "caps" {
			caps.Add(1)
			if strings.HasPrefix(r.URL.Path, "/unsupported/") {
				_, _ = fmt.Fprint(w, `<caps><searching><movie-search available="no"/></searching></caps>`)
				return
			}
			_, _ = fmt.Fprint(w, `<caps><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
			return
		}
		searches.Add(1)
		if r.URL.Query().Get("t") != "movie" || r.URL.Query().Get("limit") != "1" {
			t.Error("unbounded probe")
		}
		if strings.HasPrefix(r.URL.Path, "/bad-key/") {
			_, _ = fmt.Fprint(w, `<error code="100" description="synthetic-caps-browser-key"/>`)
			return
		}
		_, _ = fmt.Fprint(w, `<rss><channel><item><title>Probe.1999.1080p.BluRay</title><guid>probe</guid><enclosure url="https://fixture.invalid/?passkey=synthetic-caps-browser-key" length="1000"/></item></channel></rss>`)
	}))
	defer stub.Close()
	key := bytes.Repeat([]byte{7}, 32)
	svc := search.NewService(postgres.NewSearchExecutionStore(a.DBPool()), a.IDGen(), crypto.NewSecretProtector(key), 0)
	svc.SetClientFactory(func(base, key string) ports.IndexerClient { return torznab.NewClient(base, key, stub.Client()) })
	svc.SetHealthRepository(postgres.NewIndexerHealthStore(a.DBPool(), a.IDGen()))
	for _, name := range []string{"unsupported", "bad-key", "valid"} {
		if _, e = svc.CreateIndexer(ctx, name, stub.URL+"/"+name, "synthetic-caps-browser-key", nil); e != nil {
			t.Fatal(e)
		}
	}
	handler := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, key)
	server := httptest.NewServer(httpapi.NewRouter(a, handler, as, nil))
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/torznab-caps-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_CAPS_BROWSER_BASE="+server.URL, "PULSE_CAPS_BROWSER_SESSION="+session)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("caps browser %v: %s", e, raw)
	}
	t.Log(string(raw))
	var candidates, queries, audits, events, online int
	var leak bool
	e = a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query),(SELECT count(*) FROM audit_log WHERE action='indexer.health_observed'),(SELECT count(*) FROM outbox_event WHERE event_type='indexer.health_observed'),(SELECT count(*) FROM indexer WHERE health='online'),EXISTS(SELECT 1 FROM indexer_query WHERE error LIKE '%synthetic-caps-browser-key%')`).Scan(&candidates, &queries, &audits, &events, &online, &leak)
	if e != nil || candidates != 0 || queries != 3 || audits != 3 || events != 3 || online != 1 || leak || caps.Load() != 3 || searches.Load() != 2 {
		t.Fatal("incorrect native browser effects", candidates, queries, audits, events, online, leak, caps.Load(), searches.Load(), e)
	}
}
