package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

type doubanConfigRepo struct {
	config                 *ports.DoubanSyncConfig
	receipt                *ports.DoubanSyncConfigReceipt
	err                    error
	reads, saves, receipts int
	owner                  string
}

func (r *doubanConfigRepo) ReadDoubanSyncConfig(_ context.Context, a *ports.Actor) (*ports.DoubanSyncConfig, error) {
	r.reads++
	r.owner = a.ID
	return r.config, r.err
}
func (r *doubanConfigRepo) SaveDoubanSyncConfig(_ context.Context, a *ports.Actor, in ports.DoubanSyncInput, key string) (*ports.DoubanSyncConfigReceipt, error) {
	r.saves++
	r.owner = a.ID
	return r.receipt, r.err
}
func (r *doubanConfigRepo) ReadDoubanSyncConfigReceipt(_ context.Context, a *ports.Actor, key string) (*ports.DoubanSyncConfigReceipt, error) {
	r.receipts++
	r.owner = a.ID
	return r.receipt, r.err
}

func doubanConfigTestRouter(s *ServerHandler, actor *ports.Actor) http.Handler {
	h := HandlerWithOptions(NewStrictHandler(s, nil), StdHTTPServerOptions{Middlewares: []MiddlewareFunc{strictDoubanSyncConfigJSON}})
	return doubanSyncConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), actorKey, actor))
		}
		h.ServeHTTP(w, r)
	}))
}
func doubanConfigRequest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "original-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDoubanSyncConfigSubjectsBeforeAvailability(t *testing.T) {
	owner := uuid.NewString()
	for _, tt := range []struct {
		name   string
		actor  *ports.Actor
		status int
	}{
		{"anonymous", nil, 401},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: owner, Role: ports.RoleMember}, 503},
		{"admin", &ports.Actor{Type: ports.ActorTypeUser, ID: owner, Role: ports.RoleAdmin}, 503},
		{"system", &ports.Actor{Type: "system", ID: owner, Role: ports.RoleAdmin}, 403},
		{"token", &ports.Actor{Type: "api_token", ID: owner, Role: ports.RoleAdmin}, 403},
		{"scoped-user", &ports.Actor{Type: ports.ActorTypeUser, ID: owner, Role: ports.RoleAdmin, Scopes: []string{}}, 403},
		{"invalid-id", &ports.Actor{Type: ports.ActorTypeUser, ID: "bad", Role: ports.RoleMember}, 403},
		{"wrong-role", &ports.Actor{Type: ports.ActorTypeUser, ID: owner, Role: "viewer"}, 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := doubanConfigTestRouter(&ServerHandler{}, tt.actor)
			for _, req := range []struct{ method, path, body string }{
				{"GET", doubanSyncConfigPath, ""}, {"PUT", doubanSyncConfigPath, `{"douban_uid":"exherb","enabled":false,"expected_version":0}`}, {"GET", doubanSyncConfigPath + "/commands/original-key", ""},
			} {
				w := doubanConfigRequest(h, req.method, req.path, req.body)
				if w.Code != tt.status || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
					t.Fatalf("%s: %d %s", req.method, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestDoubanSyncConfigPersistenceAndReadOnlyReceipt(t *testing.T) {
	owner := uuid.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	config := ports.DoubanSyncConfig{ID: uuid.New(), OwnerID: owner, DoubanUID: "exherb", Enabled: false, Version: 1, CreatedAt: now, UpdatedAt: now}
	repo := &doubanConfigRepo{}
	s := &ServerHandler{}
	s.SetDoubanSyncConfigService(watchlist.NewConfigService(repo))
	for _, role := range []string{ports.RoleMember, ports.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			h := doubanConfigTestRouter(s, &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: role})
			repo.config = nil
			w := doubanConfigRequest(h, "GET", doubanSyncConfigPath, "")
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"config":null}` {
				t.Fatal(w.Code, w.Body.String())
			}
			repo.config = &config
			repo.receipt = &ports.DoubanSyncConfigReceipt{CommandKey: "original-key", Config: config}
			w = doubanConfigRequest(h, "PUT", doubanSyncConfigPath, `{"douban_uid":"exherb","enabled":false,"expected_version":0}`)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			original := w.Body.String()
			var decoded struct {
				CommandKey string         `json:"command_key"`
				Config     map[string]any `json:"config"`
			}
			if json.Unmarshal(w.Body.Bytes(), &decoded) != nil || decoded.Config["enabled"] != false || decoded.Config["owner_id"] != owner.String() || decoded.Config["version"] != float64(1) {
				t.Fatal(original)
			}
			for _, field := range []string{"id", "created_at", "updated_at"} {
				if decoded.Config[field] == nil {
					t.Fatal("missing", field)
				}
			}
			if strings.Contains(original, "last_synced") || strings.Contains(original, "subscription") || strings.Contains(original, "movie_root_id") {
				t.Fatal(original)
			}
			writes := repo.saves
			// A later current version cannot replace the original command receipt.
			current := config
			current.Version = 2
			current.DoubanUID = "other"
			repo.config = &current
			w = doubanConfigRequest(h, "GET", doubanSyncConfigPath+"/commands/original-key", "")
			if w.Code != 200 || w.Body.String() != original || repo.saves != writes {
				t.Fatal(w.Code, w.Body.String(), repo.saves)
			}
			w = doubanConfigRequest(h, "GET", doubanSyncConfigPath, "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":2`) || repo.saves != writes || repo.owner != owner.String() {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestDoubanSyncConfigErrorsAndValidationZeroCalls(t *testing.T) {
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	repo := &doubanConfigRepo{}
	s := &ServerHandler{}
	s.SetDoubanSyncConfigService(watchlist.NewConfigService(repo))
	h := doubanConfigTestRouter(s, actor)
	for _, tt := range []struct {
		err    error
		status int
	}{
		{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrRejected, 400}, {ports.ErrNotFound, 404}, {ports.ErrSubscriptionBindingsConflict, 409}, {ports.ErrUnavailable, 503}, {errors.New("private-database-secret"), 503},
	} {
		repo.err = tt.err
		for _, req := range []struct{ method, path, body string }{
			{"GET", doubanSyncConfigPath, ""}, {"PUT", doubanSyncConfigPath, `{"douban_uid":"exherb","enabled":false,"expected_version":0}`}, {"GET", doubanSyncConfigPath + "/commands/original-key", ""},
		} {
			w := doubanConfigRequest(h, req.method, req.path, req.body)
			if w.Code != tt.status || strings.Contains(w.Body.String(), "private-database-secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	repo.reads, repo.saves, repo.receipts = 0, 0, 0
	for _, body := range []string{`{"douban_uid":"x","enabled":false}`, `{"douban_uid":"x","enabled":"false","expected_version":0}`, `{"douban_uid":"x","enabled":false,"expected_version":"0"}`, `{"douban_uid":"x","enabled":true,"expected_version":0}`} {
		w := doubanConfigRequest(h, "PUT", doubanSyncConfigPath, body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if repo.reads+repo.saves+repo.receipts != 0 {
		t.Fatal("invalid request reached repository")
	}
	// The legacy POST remains unavailable even with a working configuration service.
	w := doubanConfigRequest(h, "POST", "/api/v1/sync/douban", `{"douban_uid":"x"}`)
	if w.Code != 503 || repo.reads+repo.saves+repo.receipts != 0 {
		t.Fatal(w.Code, "legacy route touched configuration")
	}
}
