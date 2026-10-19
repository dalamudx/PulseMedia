package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	searchcase "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

func TestConfiguredEndpointURLsRedactedInHTTPWithoutStorageMutation(t *testing.T) {
	a, svc, _ := setupAuthHTTP(t)
	ctx := context.Background()
	user, session, err := svc.SetupAdmin(ctx, "endpoint-reader", "EndpointReaderFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	codec := crypto.NewSecretProtector(key)
	sealed, err := codec.EncryptSecret([]byte("fixture-credential"))
	if err != nil {
		t.Fatal(err)
	}
	search := searchcase.NewService(postgres.NewSearchExecutionStore(a.DBPool()), a.IDGen(), codec)
	search.SetHealthRepository(postgres.NewIndexerHealthStore(a.DBPool(), a.IDGen()))
	handler := httpapi.NewServerHandler(a, svc, nil, nil, nil, nil, nil, nil, search, nil, nil, nil, key)
	handler.SetIndexerManagementServices(searchcase.NewIndexerManagementService(postgres.NewIndexerManagementStore(a.DBPool(), a.IDGen(), codec), codec), nil)
	router := httpapi.NewRouter(a, handler, svc, nil)
	raw := "https://legacy-user:private-password@example.test/base?category=movie&page=2&api_key=private-api&passkey=private-passkey&token=private-token&cookie=private-cookie&download_url=private-download#private-fragment"
	rows := []struct{ table, path, sql string }{
		{"indexer", "indexers", `INSERT INTO indexer(id,name,url,api_key_encrypted,categories) VALUES($1,'redaction',$2,$3,'[]')`},
		{"download_client", "download-clients", `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'redaction','qbittorrent',$2,'configured-user',$3,'{}')`},
		{"media_server", "media-servers", `INSERT INTO media_server(id,name,server_type,url,api_key_encrypted,path_mapping) VALUES($1,'redaction','jellyfin',$2,$3,'{}')`},
	}
	for _, row := range rows {
		endpointURL := raw
		if row.table == "indexer" {
			// Sources reject fragments before encryption; other endpoints still exercise their redaction.
			endpointURL, _, _ = strings.Cut(raw, "#")
		}
		if _, err = a.DBPool().Exec(ctx, row.sql, uuid.New(), endpointURL, sealed); err != nil {
			t.Fatal(err)
		}
	}
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, a.DBPool(), codec, searchcase.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatal(err)
	}
	stored := make(map[string]string)
	for _, row := range rows {
		var value string
		if err = a.DBPool().QueryRow(ctx, "SELECT url FROM "+row.table+" WHERE name='redaction'").Scan(&value); err != nil {
			t.Fatal(err)
		}
		stored[row.table] = value
	}
	token := "owned-endpoint-read-token"
	if _, err = a.DBPool().Exec(ctx, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'endpoint read',$3,'["read"]')`, uuid.New(), user.ID, crypto.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "member"} {
		if _, err = a.DBPool().Exec(ctx, `UPDATE app_user SET role=$1 WHERE id=$2`, role, user.ID); err != nil {
			t.Fatal(err)
		}
		for _, credential := range []string{session, token} {
			for _, row := range rows {
				r := httptest.NewRequest(http.MethodGet, "/api/v1/"+row.path, nil)
				r.Header.Set("Authorization", "Bearer "+credential)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if row.table == "indexer" && (role != "admin" || credential != session) {
					if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "legacy-user") {
						t.Fatalf("source configuration requires an administrator session: status=%d", w.Code)
					}
					continue
				}
				if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "legacy-user") {
					t.Fatalf("%s %s unsafe response status=%d", role, row.path, w.Code)
				}
				var body struct {
					Items []struct {
						URL      string `json:"url"`
						Username string `json:"username"`
					}
				}
				if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
					t.Fatal("missing endpoint projection")
				}
				parsed, err := url.Parse(body.Items[0].URL)
				if err != nil || parsed.User != nil {
					t.Fatal("unsafe endpoint projection")
				}
				if row.table == "indexer" {
					if parsed.String() != "https://example.test" {
						t.Fatal("source list must expose only its origin")
					}
				} else if parsed.Path != "/base" || parsed.Query().Get("category") != "movie" || parsed.Query().Get("page") != "2" || parsed.Query().Get("api_key") != "***" {
					t.Fatal("benign endpoint configuration lost")
				}
				if row.table == "download_client" && body.Items[0].Username != "configured-user" {
					t.Fatal("existing username contract changed")
				}
			}
		}
	}
	for _, row := range rows {
		var persisted string
		// Table identifiers come exclusively from the fixed fixtures above.
		if err = a.DBPool().QueryRow(ctx, "SELECT url FROM "+row.table+" WHERE name='redaction'").Scan(&persisted); err != nil || persisted != stored[row.table] {
			t.Fatal("response projection changed stored endpoint")
		}
	}
}
