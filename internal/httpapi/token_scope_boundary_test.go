package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestAPITokenScopeCannotWriteOrReplaySettings(t *testing.T) {
	a, _, _ := setupAuthHTTP(t)
	ctx := context.Background()
	pool := a.DBPool()
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, clock.NewMock(now), a.IDGen())
	user, session, err := svc.SetupAdmin(ctx, "owned-token-scope", "OwnedTokenScopeFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.ID.String(), Role: ports.RoleAdmin}
	settingsSvc := settings.NewService(postgres.NewSettingsStore(pool, a.IDGen()))
	req := settings.UpdateSettingRequest{Value: map[string]any{"flag": "original"}}
	if _, err = settingsSvc.UpdateSetting(ctx, actor, "owned.scope.baseline", req, "owned-scope-original"); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(a, svc, settingsSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(a, handler, svc, nil)
	q := postgres.Queries(pool)
	request := func(method, path, token, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"value":{"flag":"original"},"expected_version":0}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	var beforeAudit, beforeReceipts int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log),(SELECT count(*) FROM idempotency_record)").Scan(&beforeAudit, &beforeReceipts); err != nil {
		t.Fatal(err)
	}
	for i, scopes := range [][]string{{"read"}, {}, {"write"}, {"admin", "system", "*"}} {
		raw := "owned-api-scope-" + uuid.NewString()
		encoded, _ := json.Marshal(scopes)
		id := uuid.New()
		if _, err := pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'owned scope fixture',$3,$4)", id, user.ID, crypto.HashToken(raw), encoded); err != nil {
			t.Fatal(err)
		}
		tokenActor, err := svc.AuthenticateToken(ctx, raw)
		if err != nil || tokenActor == nil || tokenActor.Type != ports.ActorTypeUser || tokenActor.Scopes == nil {
			t.Fatal("API credential lost its user/limited-scope identity")
		}
		if w := request(http.MethodPut, "/api/v1/settings/owned.scope.baseline", raw, "owned-scope-original"); w.Code != http.StatusForbidden {
			t.Errorf("scope case %d: cached HTTP write got %d", i, w.Code)
		}
		fresh := "owned.scope.fresh." + id.String()
		if w := request(http.MethodPut, "/api/v1/settings/"+fresh, raw, "owned-fresh-"+id.String()); w.Code != http.StatusForbidden {
			t.Errorf("scope case %d: new HTTP write got %d", i, w.Code)
		}
		if _, err := q.GetSetting(ctx, fresh); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("scope case %d created a setting", i)
		}
		if _, err := settingsSvc.UpdateSetting(ctx, tokenActor, "owned.scope.baseline", req, "owned-scope-original"); !errors.Is(err, ports.ErrForbidden) {
			t.Errorf("scope case %d: direct command replay bypassed credential restriction", i)
		}
		wantRead := http.StatusForbidden
		if i == 0 {
			wantRead = http.StatusOK
		}
		if w := request(http.MethodGet, "/api/v1/settings", raw, ""); w.Code != wantRead {
			t.Errorf("scope case %d: business read got %d, want %d", i, w.Code, wantRead)
		}
		if w := request(http.MethodGet, "/api/v1/auth/me", raw, ""); w.Code != http.StatusOK {
			t.Error("own credential introspection no longer works")
		}
		var stored []byte
		if err := pool.QueryRow(ctx, "SELECT scopes FROM api_token WHERE id=$1", id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var unchanged []string
		if err := json.Unmarshal(stored, &unchanged); err != nil || !reflect.DeepEqual(unchanged, scopes) {
			t.Error("scopes rewritten during denial")
		}
	}
	var afterAudit, afterReceipts int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log),(SELECT count(*) FROM idempotency_record)").Scan(&afterAudit, &afterReceipts); err != nil {
		t.Fatal(err)
	}
	if beforeAudit != afterAudit || beforeReceipts != afterReceipts {
		t.Error("denial added audit or successful command receipts")
	}
	// Every request re-resolves current scopes, user role, revocation and activity.
	raw := "owned-fresh-scope-" + uuid.NewString()
	id := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'owned freshness',$3,'[\"read\"]')", id, user.ID, crypto.HashToken(raw)); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/settings", raw, ""); w.Code != http.StatusOK {
		t.Fatal("read scope denied")
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET scopes='[]' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/settings", raw, ""); w.Code != http.StatusForbidden {
		t.Error("removed scope retained read authority")
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	current, err := svc.AuthenticateToken(ctx, raw)
	if err != nil || current == nil || current.Type != ports.ActorTypeUser || current.Role != ports.RoleMember {
		t.Error("scope/role text overrode current user role")
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET role='admin' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET revoked_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/auth/me", raw, ""); w.Code != http.StatusUnauthorized {
		t.Error("revoked credential still authenticates")
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET revoked_at=NULL WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/auth/me", raw, ""); w.Code != http.StatusUnauthorized {
		t.Error("disabled user retained API access")
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	// Session authorization retains normal writes, including replay.
	if w := request(http.MethodPut, "/api/v1/settings/owned.scope.baseline", session, "owned-scope-original"); w.Code != http.StatusOK {
		t.Error("session command replay regressed")
	}
	if w := request(http.MethodPut, "/api/v1/settings/owned.scope.session", session, "owned-session-new"); w.Code != http.StatusOK {
		t.Error("normal session write regressed")
	}
}
