package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeTorznabFailureAndSameNameRetryKeepSecretsEncrypted(t *testing.T) {
	f := newMovieLinkFixture(t)
	pool := f.pool
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var healthy atomic.Bool
	var requests atomic.Int32
	const apiKey = "synthetic-native-indexer-key"
	const linkSecret = "synthetic-native-link-passkey"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("apikey") != apiKey || r.URL.Query().Get("q") != "Fight Club" {
			w.WriteHeader(401)
			return
		}
		if !healthy.Load() {
			w.WriteHeader(503)
			_, _ = fmt.Fprint(w, apiKey+" "+linkSecret)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<rss><channel><item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>native-stable-guid</guid><enclosure url="%s/download?passkey=%s" length="1000"/></item></channel></rss>`, server.URL, linkSecret)
	}))
	defer server.Close()
	ids := idgen.New()
	key := bytes.Repeat([]byte{7}, 32)
	encrypted, e := crypto.Encrypt(key, []byte(apiKey))
	if e != nil {
		t.Fatal(e)
	}
	q := postgres.Queries(pool)
	indexer, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: ids.NewUUID(), Name: "Native HTTP indexer", Url: server.URL, ApiKeyEncrypted: encrypted, Categories: []byte("[]")})
	if e != nil {
		t.Fatal(e)
	}
	svc := search.NewService(postgres.NewSearchExecutionStore(pool), ids, crypto.NewSecretProtector(key), 0)
	svc.RegisterClient(indexer.ID, torznab.NewClient(server.URL, apiKey, nil))
	failed, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 0)
	if !errors.Is(e, ports.ErrUnavailable) || failed == nil || failed.State != "failed" {
		t.Fatal(failed, e)
	}
	var state, message string
	var count int
	if e = pool.QueryRow(ctx, "SELECT state,error FROM indexer_query WHERE id=$1", failed.QueryID).Scan(&state, &message); e != nil {
		t.Fatal(e)
	}
	if state != "failed" || message != "indexer search failed" || strings.Contains(message, apiKey) || strings.Contains(message, linkSecret) {
		t.Fatal("unsafe failed query state")
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM release_candidate WHERE indexer_id=$1", indexer.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	healthy.Store(true)
	success, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 0)
	if e != nil || success == nil || success.State != "succeeded" || success.QueryID == failed.QueryID || success.FromShared || success.ResultsCount != 1 {
		t.Fatal(success, e)
	}
	var ciphertext []byte
	if e = pool.QueryRow(ctx, "SELECT link_encrypted FROM release_candidate WHERE indexer_id=$1", indexer.ID).Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	plain, e := crypto.Decrypt(key, ciphertext)
	if e != nil || string(plain) != server.URL+"/download?passkey="+linkSecret || bytes.Contains(ciphertext, []byte(linkSecret)) {
		t.Fatal("invalid encrypted download reference")
	}
	if requests.Load() != 2 {
		t.Fatal(requests.Load())
	}
}
