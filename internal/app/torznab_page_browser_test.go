package app

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestTorznabPageBrowserFailureRetryHTTPPG(t *testing.T) {
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
	user, _, e := as.SetupAdmin(ctx, "page-ui-admin", "SyntheticPageFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	_, session, e := as.Login(ctx, user.Username, "SyntheticPageFixture123!")
	if e != nil {
		t.Fatal(e)
	}
	var searches atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"results":[{"id":550,"title":"Fight Club","original_title":"Fight Club","media_type":"movie","release_date":"1999-10-15","overview":"Native page fixture","vote_average":8.4}]}`)
			return
		}
		n := searches.Add(1)
		if n == 8 {
			if _, err := pool.Exec(r.Context(), "UPDATE indexer SET config_version=config_version+1 WHERE name='page fixture'"); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
		}
		if r.URL.Query().Get("apikey") != "synthetic-page-browser-key" || r.URL.Query().Get("q") != "Fight Club" {
			t.Error("unexpected search request")
		}
		item := `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>browser-page</guid><enclosure url="https://fixture.invalid/?passkey=synthetic-page-browser-key" length="1000"/></item>`
		response := `<response offset="50" total="51"/>`
		switch n {
		case 2, 7, 8, 9:
			response = `<response offset="0" total="1"/>`
		case 3:
			response = `<response offset="0" total="0"/>`
		case 4:
			response = `<response offset="0" total="51"/>`
		case 5:
			response = ""
			item = ""
		case 6:
			response = `<response offset="0" total="0"/>`
			item = ""
		case 10, 12:
			response = `<response offset="0" total="51"/>`
			item = ""
			for i := 0; i < 50; i++ {
				item += fmt.Sprintf(`<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>browser-bounded-%d</guid><link>https://fixture.invalid/%d?passkey=synthetic-page-browser-key</link></item>`, i, i)
			}
		case 11:
			response = `<response offset="50" total="51"/>`
			item = `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>browser-bounded-50</guid><link>https://fixture.invalid/50?passkey=synthetic-page-browser-key</link></item>`
		case 13:
			response = `<response offset="50" total="51"/>`
			item = `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>browser-bounded-0</guid><link>https://fixture.invalid/changed?passkey=synthetic-page-browser-key</link></item>`
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<rss><channel>%s%s</channel></rss>`, response, item)
	}))
	defer stub.Close()
	key := bytes.Repeat([]byte{7}, 32)
	svc := search.NewService(postgres.NewSearchExecutionStore(a.DBPool()), a.IDGen(), crypto.NewSecretProtector(key), 0)
	svc.SetClientFactory(func(base, key string) ports.IndexerClient { return torznab.NewClient(base, key, stub.Client()) })
	if _, e = svc.CreateIndexer(ctx, "page fixture", stub.URL, "synthetic-page-browser-key", nil); e != nil {
		t.Fatal(e)
	}
	meta := tmdb.NewClient("synthetic-metadata-key", tmdb.WithBaseURL(stub.URL))
	handler := httpapi.NewServerHandler(a, as, nil, nil, nil, meta, nil, nil, svc, nil, nil, nil, key)
	server := httptest.NewServer(httpapi.NewRouter(a, handler, as, nil))
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/torznab-page-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_PAGE_BROWSER_BASE="+server.URL, "PULSE_PAGE_BROWSER_SESSION="+session)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("page browser %v: %s", e, raw)
	}
	t.Log(string(raw))
	var candidates, linked, failed, succeeded int
	var leak bool
	e = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_candidate),(SELECT count(*) FROM indexer_query WHERE state='failed'),(SELECT count(*) FROM indexer_query WHERE state='succeeded'),EXISTS(SELECT 1 FROM indexer_query WHERE error LIKE '%synthetic-page-browser-key%')`).Scan(&candidates, &linked, &failed, &succeeded, &leak)
	if e != nil || candidates != 53 || linked != 105 || failed != 4 || succeeded != 9 || leak || searches.Load() != 13 {
		t.Fatal(candidates, linked, failed, succeeded, leak, searches.Load(), e)
	}
}
