package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

const doubanBindingsBase = `{"douban_uid":"exherb","enabled":false,"expected_version":0}`
const doubanBindingIndexer = "11111111-1111-4111-8111-111111111111"
const doubanBindingDownloader = "22222222-2222-4222-8222-222222222222"
const doubanBindingPair = `{"indexer_ids":["` + doubanBindingIndexer + `"],"downloader_id":"` + doubanBindingDownloader + `"}`

// This fake persists only settings and receipts; it has no network collaborator.
type doubanBindingsRepo struct {
	doubanConfigRepo
	input ports.DoubanSyncInput
}

func (r *doubanBindingsRepo) SaveDoubanSyncConfig(_ context.Context, a *ports.Actor, in ports.DoubanSyncInput, key string) (*ports.DoubanSyncConfigReceipt, error) {
	r.saves++
	r.input = in
	bindings := in.Bindings
	if bindings == nil && r.config != nil {
		bindings = r.config.Bindings
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := ports.DoubanSyncConfig{ID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), OwnerID: uuid.MustParse(a.ID), DoubanUID: in.DoubanUID, Enabled: in.Enabled, MovieRootID: in.MovieRootID, TVRootID: in.TVRootID, Bindings: bindings, Version: in.ExpectedVersion + 1, CreatedAt: now, UpdatedAt: now}
	r.config = &c
	r.receipt = &ports.DoubanSyncConfigReceipt{CommandKey: key, Config: c}
	return r.receipt, nil
}

func doubanBindingsRouter() (http.Handler, *doubanBindingsRepo) {
	repo := &doubanBindingsRepo{}
	s := &ServerHandler{}
	s.SetDoubanSyncConfigService(watchlist.NewConfigService(repo))
	return doubanConfigTestRouter(s, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}), repo
}

func TestDoubanSyncConfigBindingsRejectZeroIO(t *testing.T) {
	body := func(b string) string { return strings.TrimSuffix(doubanBindingsBase, "}") + `,"bindings":` + b + `}` }
	tests := []struct{ name, bindings string }{
		{"null", `null`}, {"empty-object", `{}`}, {"missing-indexers", `{"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"array", `[]`}, {"string", `"x"`}, {"bool", `true`}, {"number", `1`},
		{"null-list", `{"indexer_ids":null}`}, {"null-item", `{"indexer_ids":[null],"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"null-downloader", `{"indexer_ids":[],"downloader_id":null}`},
		{"unknown", strings.TrimSuffix(doubanBindingPair, "}") + `,"secret":"private-secret"}`},
		{"duplicate-indexers", strings.TrimSuffix(doubanBindingPair, "}") + `,"indexer_ids":[]}`},
		{"escaped-duplicate", strings.TrimSuffix(doubanBindingPair, "}") + `,"indexer_\u0069ds":[]}`},
		{"duplicate-downloader", strings.TrimSuffix(doubanBindingPair, "}") + `,"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"indexer-alias", strings.Replace(doubanBindingPair, "indexer_ids", "INDEXER_IDS", 1)},
		{"downloader-alias", strings.Replace(doubanBindingPair, "downloader_id", "Downloader_ID", 1)},
		{"duplicate-uuid", `{"indexer_ids":["` + doubanBindingIndexer + `","` + doubanBindingIndexer + `"],"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"duplicate-uuid-case", `{"indexer_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"],"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"missing-downloader", `{"indexer_ids":["` + doubanBindingIndexer + `"]}`},
		{"clear-with-downloader", `{"indexer_ids":[],"downloader_id":"` + doubanBindingDownloader + `"}`},
		{"zero-indexer", strings.Replace(doubanBindingPair, doubanBindingIndexer, uuid.Nil.String(), 1)},
		{"zero-downloader", strings.Replace(doubanBindingPair, doubanBindingDownloader, uuid.Nil.String(), 1)},
		{"bad-indexer", strings.Replace(doubanBindingPair, doubanBindingIndexer, "bad", 1)},
		{"bad-downloader", strings.Replace(doubanBindingPair, doubanBindingDownloader, "bad", 1)},
		{"list-object", `{"indexer_ids":{}}`}, {"list-string", `{"indexer_ids":"x"}`},
		{"number-item", `{"indexer_ids":[1]}`}, {"object-item", `{"indexer_ids":[{}]}`},
		{"downloader-object", `{"indexer_ids":[],"downloader_id":{}}`},
	}
	ids := make([]string, 65)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct{ name, bindings string }{"over-limit", `{"indexer_ids":` + string(encoded) + `,"downloader_id":"` + doubanBindingDownloader + `"}`})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := doubanBindingsRouter()
			w := doubanConfigRequest(h, "PUT", doubanSyncConfigPath, body(tt.bindings))
			if w.Code != 400 || repo.reads+repo.saves+repo.receipts != 0 || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, repo.reads+repo.saves+repo.receipts, w.Body.String())
			}
		})
	}
	for _, tt := range []struct{ name, path, body string }{
		{"duplicate-bindings", doubanSyncConfigPath, strings.TrimSuffix(body(doubanBindingPair), "}") + `,"bindings":{"indexer_ids":[]}}`},
		{"extra-query", doubanSyncConfigPath + "?bindings=x", body(doubanBindingPair)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := doubanBindingsRouter()
			w := doubanConfigRequest(h, "PUT", tt.path, tt.body)
			if w.Code != 400 || repo.reads+repo.saves+repo.receipts != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestDoubanSyncConfigBindingsRoundTrip(t *testing.T) {
	for _, tt := range []struct{ name, bindings string }{{"omitted", ""}, {"pair", doubanBindingPair}, {"clear", `{"indexer_ids":[]}`}} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := doubanBindingsRouter()
			body := doubanBindingsBase
			if tt.bindings != "" {
				body = strings.TrimSuffix(body, "}") + `,"bindings":` + tt.bindings + `}`
			}
			w := doubanConfigRequest(h, "PUT", doubanSyncConfigPath, body)
			if w.Code != 200 || repo.saves != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
			var want *ports.DoubanSyncBindings
			if tt.bindings != "" {
				want = &ports.DoubanSyncBindings{}
				if err := json.Unmarshal([]byte(tt.bindings), want); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(repo.input.Bindings, want) {
				t.Fatalf("ports input=%+v want=%+v", repo.input.Bindings, want)
			}
			original := w.Body.String()
			assertBinding := func(raw string) {
				t.Helper()
				var envelope struct {
					Config map[string]json.RawMessage `json:"config"`
				}
				if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
					t.Fatal(err)
				}
				got, present := envelope.Config["bindings"]
				if want == nil {
					if present {
						t.Fatal(raw)
					}
					return
				}
				var decoded ports.DoubanSyncBindings
				if err := json.Unmarshal(got, &decoded); err != nil || !reflect.DeepEqual(decoded, *want) {
					t.Fatalf("binding=%s want=%+v err=%v", got, want, err)
				}
				if strings.Contains(string(got), "null") {
					t.Fatal(raw)
				}
			}
			assertBinding(original)
			w = doubanConfigRequest(h, "GET", doubanSyncConfigPath, "")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			assertBinding(w.Body.String())
			// Omission on edit preserves the whole binding returned by persistence.
			edit := strings.Replace(doubanBindingsBase, `"expected_version":0`, `"expected_version":1`, 1)
			w = doubanConfigRequest(h, "PUT", doubanSyncConfigPath, edit)
			if w.Code != 200 || repo.input.Bindings != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			assertBinding(w.Body.String())
			saved := w.Body.String()
			// A different current configuration cannot replace the historical snapshot.
			current := *repo.config
			current.Version++
			current.Bindings = nil
			repo.config = &current
			writes := repo.saves
			w = doubanConfigRequest(h, "GET", doubanSyncConfigPath+"/commands/original-key", "")
			if w.Code != 200 || w.Body.String() != saved || repo.saves != writes {
				t.Fatal(w.Code, w.Body.String())
			}
			assertBinding(w.Body.String())
		})
	}
}

func TestDoubanSyncConfigBindingsCanonicalOmissionAndLimit(t *testing.T) {
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	list, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, body, want string }{
		{"omitted", doubanBindingsBase, doubanBindingsBase},
		{"limit64", strings.TrimSuffix(doubanBindingsBase, "}") + `,"bindings":{"indexer_ids":` + string(list) + `,"downloader_id":"` + doubanBindingDownloader + `"}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if tt.want != "" && string(raw) != tt.want {
					t.Fatalf("canonical=%s want=%s", raw, tt.want)
				}
				w.WriteHeader(204)
			}))
			w := doubanConfigRequest(h, "PUT", doubanSyncConfigPath, tt.body)
			if !called || w.Code != 204 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	// The generated request also keeps its old omitted-field spelling and order.
	raw, err := json.Marshal(DoubanSyncConfigInput{DoubanUid: "exherb", Enabled: false, ExpectedVersion: 0})
	if err != nil || string(raw) != doubanBindingsBase {
		t.Fatal(string(raw), err)
	}
}

func TestDoubanSyncConfigBindingsErrorsAndSubjects(t *testing.T) {
	// Bindings must not change authentication or error redaction behavior.
	for _, tt := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401},
		{"token", &ports.Actor{Type: "api_token", ID: uuid.NewString(), Role: ports.RoleAdmin}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &doubanConfigRepo{}
			s := &ServerHandler{}
			s.SetDoubanSyncConfigService(watchlist.NewConfigService(repo))
			w := doubanConfigRequest(doubanConfigTestRouter(s, tt.actor), "PUT", doubanSyncConfigPath, strings.TrimSuffix(doubanBindingsBase, "}")+`,"bindings":`+doubanBindingPair+`}`)
			if w.Code != tt.status || w.Header().Get("Cache-Control") != "private, no-store" || repo.saves+repo.reads+repo.receipts != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	t.Run("redacted-persistence-error", func(t *testing.T) {
		repo := &doubanConfigRepo{err: errors.New("private-database-secret")}
		s := &ServerHandler{}
		s.SetDoubanSyncConfigService(watchlist.NewConfigService(repo))
		actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
		w := doubanConfigRequest(doubanConfigTestRouter(s, actor), "PUT", doubanSyncConfigPath, strings.TrimSuffix(doubanBindingsBase, "}")+`,"bindings":`+doubanBindingPair+`}`)
		if w.Code != 503 || repo.saves != 1 || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "private-database-secret") || strings.Contains(w.Body.String(), doubanBindingDownloader) {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	// The strict middleware does not need a server, persistence, or network to reject a query.
	w := httptest.NewRecorder()
	strictDoubanSyncConfigJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected downstream I/O") })).ServeHTTP(w, httptest.NewRequest("GET", doubanSyncConfigPath+"?x=y", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
