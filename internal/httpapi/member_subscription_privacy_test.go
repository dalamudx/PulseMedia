package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestMemberSubscriptionReadTokenObjectAndPathPrivacy(t *testing.T) {
	a, adminSession, memberSession, rootID, unitID, _, router, _ := setupOperationHTTP(t)
	ctx := context.Background()
	q := a.Queries()
	pool := a.DBPool()
	owner, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(memberSession))
	if err != nil {
		t.Fatal(err)
	}
	var mediaID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", unitID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(httpapi.CreateSubscriptionRequest{MediaId: &mediaID, RootId: &rootID})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewReader(body))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Authorization", "Bearer "+memberSession)
	create.Header.Set("Idempotency-Key", uuid.NewString())
	result := httptest.NewRecorder()
	router.ServeHTTP(result, create)
	if result.Code != http.StatusCreated {
		t.Fatalf("create failed %d %s", result.Code, result.Body.String())
	}
	var sub httpapi.SubscriptionDetails
	if err = json.Unmarshal(result.Body.Bytes(), &sub); err != nil {
		t.Fatal(err)
	}
	root, err := q.GetLibraryRootByID(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	strangerID := uuid.New()
	if _, err = q.CreateUser(ctx, db.CreateUserParams{ID: strangerID, Username: "owned-stranger", PasswordHash: "not used", Role: ports.RoleMember, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	strangerSession := "owned-stranger-session-" + uuid.NewString()
	if _, err = q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: strangerID, TokenHash: crypto.HashToken(strangerSession), CsrfTokenHash: []byte("unused"), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	addToken := func(user uuid.UUID, scopes string) string {
		t.Helper()
		raw := "owned-read-token-" + uuid.NewString()
		if _, e := pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'owned object read',$3,$4::jsonb)", uuid.New(), user, crypto.HashToken(raw), scopes); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	ownerToken := addToken(owner.UserID, `["read","admin","system"]`)
	strangerToken := addToken(strangerID, `["read","admin","system"]`)
	admin, err := q.GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	adminToken := addToken(admin.UserID, `["read"]`)
	get := func(raw, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	ownPath := "/api/v1/subscriptions/" + sub.Id.String()
	for _, raw := range []string{memberSession, ownerToken} {
		for _, path := range []string{"/api/v1/subscriptions", ownPath} {
			rec := get(raw, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("owner read %s failed %d %s", path, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), root.Path) || strings.Contains(rec.Body.String(), `"root_path"`) {
				t.Errorf("member response exposed raw root path at %s", path)
			}
			if !strings.Contains(rec.Body.String(), sub.Id.String()) || !strings.Contains(rec.Body.String(), root.Name) {
				t.Error("safe object identity or root name missing")
			}
		}
		rec := get(raw, ownPath+"/status")
		if rec.Code != http.StatusOK {
			t.Fatalf("owner status %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), root.Path) {
			t.Error("status leaked root path")
		}
		if rec = get(raw, "/api/v1/settings"); rec.Code != http.StatusForbidden {
			t.Error("member role expanded by scope")
		}
	}
	for _, raw := range []string{strangerSession, strangerToken} {
		for _, path := range []string{ownPath, ownPath + "/status"} {
			rec := get(raw, path)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("unowned object accessible %d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), root.Path) || strings.Contains(rec.Body.String(), root.Name) {
				t.Error("object denial disclosed root metadata")
			}
		}
		rec := get(raw, "/api/v1/subscriptions")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), sub.Id.String()) {
			t.Error("list included another user's subscription")
		}
	}
	for key, value := range map[string][]byte{"owned.list.visible": []byte(`{"enabled":true}`), "tmdb.api_key": []byte(`{"ciphertext":"owned-inert-secret"}`)} {
		if _, err := q.SetSetting(ctx, db.SetSettingParams{Key: key, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{adminSession, adminToken} {
		rec := get(raw, "/api/v1/settings")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "owned.list.visible") || !strings.Contains(rec.Body.String(), `"configured":true`) {
			t.Fatalf("real admin configuration missing %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "owned-inert-secret") || strings.Contains(rec.Body.String(), "ciphertext") {
			t.Error("setting list exposed stored secret")
		}
		for _, path := range []string{"/api/v1/subscriptions", ownPath} {
			rec := get(raw, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("admin read %d %s", rec.Code, rec.Body.String())
			}
			var decoded any
			if err = json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(decoded)
			escaped, _ := json.Marshal(root.Path)
			if !bytes.Contains(encoded, escaped) {
				t.Error("administrator root path missing")
			}
		}
	}
	if _, err = pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", admin.UserID); err != nil {
		t.Fatal(err)
	}
	if rec := get(adminToken, ownPath); rec.Code != http.StatusForbidden {
		t.Error("old administrator token retained object authority")
	}
	current, err := q.GetLibraryRootByID(ctx, rootID)
	if err != nil || current.Path != root.Path {
		t.Error("presentation changed persisted root")
	}
	if os.Getenv("PULSE_UI_FIXTURE") == "1" {
		server := httptest.NewServer(router)
		defer server.Close()
		script, err := filepath.Abs("../../web/tests/member-subscription-privacy-browser.mjs")
		if err != nil {
			t.Fatal(err)
		}
		browserCtx, cancel := context.WithTimeout(ctx, 80*time.Second)
		defer cancel()
		cmd := exec.CommandContext(browserCtx, "node", script)
		cmd.Env = append(os.Environ(), "PULSE_MEMBER_PRIVACY_BASE="+server.URL, "PULSE_MEMBER_PRIVACY_SESSION="+memberSession, "PULSE_MEMBER_PRIVACY_ID="+sub.Id.String(), "PULSE_MEMBER_PRIVACY_ROOT_NAME="+root.Name, "PULSE_MEMBER_PRIVACY_ROOT_PATH="+root.Path)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("member browser %v %s", err, raw)
		}
		t.Log(string(raw))
	}
}
