package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	searchcase "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestEndpointURLHTTPRoundtripPreservesSecretsAndFencesVersions(t *testing.T) {
	a, svc, _ := setupAuthHTTP(t)
	ctx := context.Background()
	_, session, err := svc.SetupAdmin(ctx, "endpoint-editor", "EndpointEditorFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{37}, 32)
	search := searchcase.NewService(postgres.NewSearchExecutionStore(a.DBPool()), a.IDGen(), crypto.NewSecretProtector(key))
	search.SetHealthRepository(postgres.NewIndexerHealthStore(a.DBPool(), a.IDGen()))
	handler := httpapi.NewServerHandler(a, svc, nil, nil, nil, nil, nil, nil, search, nil, nil, nil, key)
	router := httpapi.NewRouter(a, handler, svc, nil)
	for _, kind := range []struct{ path, table string }{{"indexers", "indexer"}, {"download-clients", "download_client"}, {"media-servers", "media_server"}} {
		t.Run(kind.path, func(t *testing.T) {
			raw := "https://legacy-user:secret-first@example.test/base?category=movie&token=secret-first#secret-fragment"
			payload := func(endpoint, name string, version any) map[string]any {
				body := map[string]any{"name": name, "url": endpoint, "api_key": "fixture-key", "client_type": "qbittorrent", "username": "configured-user", "password": "fixture-password", "server_type": "jellyfin"}
				if version != nil {
					if kind.table == "indexer" {
						body["expected_version"] = version
					} else {
						body["expected_updated_at"] = version
					}
				}
				return body
			}
			call := func(method, path string, body map[string]any) *httptest.ResponseRecorder {
				b, _ := json.Marshal(body)
				req := httptest.NewRequest(method, path, bytes.NewReader(b))
				req.Header.Set("Authorization", "Bearer "+session)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			decode := func(w *httptest.ResponseRecorder, want int) map[string]any {
				t.Helper()
				if w.Code != want {
					t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "secret-") || strings.Contains(w.Body.String(), "legacy-user") {
					t.Fatal("response exposed stored endpoint secret")
				}
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				return body
			}
			revision := func(body map[string]any) any {
				t.Helper()
				field := "updated_at"
				if kind.table == "indexer" {
					field = "config_version"
				}
				value := body[field]
				if value == nil || value == "0001-01-01T00:00:00Z" {
					t.Fatalf("missing response version: %s", field)
				}
				return value
			}
			base := "/api/v1/" + kind.path
			created := decode(call(http.MethodPost, base, payload(raw, "created", nil)), http.StatusCreated)
			object := base + "/" + created["id"].(string)
			display := created["url"].(string)
			if display == raw || !strings.Contains(display, "category=movie") {
				t.Fatal("bad create projection")
			}
			v1 := revision(created)
			listed := decode(call(http.MethodGet, base, nil), http.StatusOK)["items"].([]any)[0].(map[string]any)
			if listed["url"] != display || revision(listed) != v1 {
				t.Fatal("list and create versions disagree")
			}
			checkStored := func(wantURL, wantName string) {
				t.Helper()
				var gotURL, gotName string
				// table is a fixed fixture identifier, never external input.
				if err := a.DBPool().QueryRow(ctx, "SELECT url,name FROM "+kind.table+" WHERE id=$1", created["id"]).Scan(&gotURL, &gotName); err != nil {
					t.Fatal(err)
				}
				if gotURL != wantURL || gotName != wantName {
					t.Fatal("stored endpoint or configuration changed unexpectedly")
				}
			}
			decode(call(http.MethodPut, object, payload(display, "missing-version", nil)), http.StatusConflict)
			checkStored(raw, "created")
			preserved := decode(call(http.MethodPut, object, payload(display, "renamed", v1)), http.StatusOK)
			v2 := revision(preserved)
			if v2 == v1 || preserved["url"] != display {
				t.Fatal("update failed to advance version or changed projection")
			}
			checkStored(raw, "renamed")
			decode(call(http.MethodPut, object, payload(display, "stale", v1)), http.StatusConflict)
			checkStored(raw, "renamed")
			decode(call(http.MethodPut, object, payload(strings.Replace(display, "/base", "/changed", 1), "changed-mask", v2)), http.StatusBadRequest)
			checkStored(raw, "renamed")
			// Secret rotation has the same public projection; the version distinguishes it.
			rotated := strings.ReplaceAll(raw, "secret-first", "secret-next")
			rotation := decode(call(http.MethodPut, object, payload(rotated, "rotated", v2)), http.StatusOK)
			if rotation["url"] != display {
				t.Fatal("rotation should keep the same safe projection")
			}
			decode(call(http.MethodPut, object, payload(display, "stale-secret", v2)), http.StatusConflict)
			checkStored(rotated, "rotated")
			// Existing callers may still explicitly replace a full URL without a version.
			replacement := "http://127.0.0.1:8080/new?category=tv&token=secret-replacement"
			replaced := decode(call(http.MethodPut, object, payload(replacement, "replaced", nil)), http.StatusOK)
			checkStored(replacement, "replaced")
			decode(call(http.MethodPut, object, payload("https://example.test/new?token=***", "masked-replacement", revision(replaced))), http.StatusBadRequest)
			checkStored(replacement, "replaced")
			decode(call(http.MethodPost, base, payload(display, "masked-create", nil)), http.StatusBadRequest)
			var count int
			if err := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM "+kind.table).Scan(&count); err != nil || count != 1 {
				t.Fatal("masked create inserted an endpoint")
			}
			// Concurrent edits from one snapshot: exactly one can commit.
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for _, name := range []string{"concurrent-a", "concurrent-b"} {
				wg.Add(1)
				go func(name string) {
					defer wg.Done()
					<-start
					results <- call(http.MethodPut, object, payload(replaced["url"].(string), name, revision(replaced)))
				}(name)
			}
			close(start)
			wg.Wait()
			close(results)
			statuses := map[int]int{}
			for w := range results {
				statuses[w.Code]++
				decode(w, w.Code)
			}
			if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
				t.Fatalf("concurrent results: %v", statuses)
			}
			var finalURL string
			if err := a.DBPool().QueryRow(ctx, "SELECT url FROM "+kind.table+" WHERE id=$1", created["id"]).Scan(&finalURL); err != nil || finalURL != replacement {
				t.Fatal("concurrent update corrupted original endpoint")
			}
		})
	}
}
