package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
)

func TestLogoutFailurePreservesCookieAndReportsFailure(t *testing.T) {
	a, svc, router := setupAuthHTTP(t)
	ctx := context.Background()
	_, token, err := svc.SetupAdmin(ctx, "logout-failure", "LogoutFailureFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	// Fail only deletion: authentication and session lookup must still succeed.
	if _, err = a.DBPool().Exec(ctx, `CREATE FUNCTION reject_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private-database-marker'; END $$; CREATE TRIGGER fail_logout BEFORE DELETE ON app_session FOR EACH ROW EXECUTE FUNCTION reject_session_delete()`); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/auth/logout", nil)
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != http.StatusInternalServerError || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "private-database-marker") {
		t.Fatalf("failed logout misreported or exposed internals: %d", w.Code)
	}
	if _, err = svc.AuthenticateToken(ctx, token); err != nil {
		t.Fatal("failed deletion lost session")
	}
	if _, err = a.DBPool().Exec(ctx, `DROP TRIGGER fail_logout ON app_session; DROP FUNCTION reject_session_delete()`); err != nil {
		t.Fatal(err)
	}
	w = request()
	if w.Code != http.StatusNoContent || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatal("confirmed logout did not clear cookie")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("logout response cacheable")
	}
}

func TestAuthDatabaseFailureDoesNotMasqueradeAsLogout(t *testing.T) {
	a, svc, router := setupAuthHTTP(t)
	ctx := context.Background()
	_, token, err := svc.SetupAdmin(ctx, "auth-db-failure", "AuthDatabaseFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	// Closing only this test's pool injects failure without terminating a backend.
	a.DBPool().Close()
	if err = svc.Logout(ctx, token); err == nil {
		t.Fatal("lookup error swallowed")
	}
	if _, err = svc.AuthenticateToken(ctx, token); err == nil {
		t.Fatal("authentication error swallowed")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "pool") {
		t.Fatalf("database failure misreported: %d", w.Code)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = svc.Logout(canceled, token); err == nil {
		t.Fatal("canceled lookup reported success")
	}
}

func TestSystemStatusRedactsPathForCurrentMemberSessionAndReadToken(t *testing.T) {
	a, svc, router := setupAuthHTTP(t)
	ctx := context.Background()
	user, session, err := svc.SetupAdmin(ctx, "status-admin", "StatusAdminFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	token := "owned-status-read-token"
	if _, err = a.DBPool().Exec(ctx, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'status test',$3,'["read"]')`, uuid.New(), user.ID, crypto.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	request := func(raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
		if raw != "" {
			r.Header.Set("Authorization", "Bearer "+raw)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request("")
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), a.Config().DataDir) {
		t.Fatal("anonymous data directory disclosure")
	}
	for _, raw := range []string{session, token} {
		if w := request(raw); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data_dir"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("admin status denied: %d", w.Code)
		}
	}
	if _, err = a.DBPool().Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{session, token} {
		if w := request(raw); w.Code != http.StatusOK || strings.Contains(w.Body.String(), a.Config().DataDir) || strings.Contains(w.Body.String(), `"data_dir"`) {
			t.Fatalf("stale admin authority: %d", w.Code)
		}
	}
	if _, err = a.DBPool().Exec(ctx, `UPDATE api_token SET scopes='[]' WHERE user_id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(token); w.Code != http.StatusForbidden {
		t.Fatal("unscoped token read system status")
	}
	if _, err = a.DBPool().Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{session, token} {
		if w := request(raw); w.Code != http.StatusUnauthorized {
			t.Fatalf("disabled identity accepted: %d", w.Code)
		}
	}
}
