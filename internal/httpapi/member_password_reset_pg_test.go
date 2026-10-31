package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

func TestMemberPasswordResetActualRouterPG(t *testing.T) {
	a, _, _ := setupAuthHTTP(t)
	ctx := t.Context()
	secrets := crypto.AuthenticationSecrets{}
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	authService := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), secrets, a.Clock(), a.IDGen())
	authService.SetMFACipher(cipher)
	const adminPassword = "MemberResetAdminFixture123!"
	const oldPassword = "MemberResetOldFixture123!"
	const newPassword = "MemberResetNewFixture123!"
	admin, adminSession, err := authService.SetupAdmin(ctx, "ResetAdmin", adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	adminActor, err := authService.AuthenticateToken(ctx, adminSession)
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.NewAccountManagementService(postgres.NewAccountManagementStore(a.DBPool(), a.IDGen()), secrets, a.Clock(), a.IDGen(), cipher)
	member, err := accounts.Create(ctx, adminActor, adminSession, ports.AccountCreate{Username: "ResetMember", Password: oldPassword, Role: ports.RoleMember}, "fixture-member")
	if err != nil {
		t.Fatal(err)
	}
	_, memberSession, err := authService.Login(ctx, "ResetMember", oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	memberActor, err := authService.AuthenticateToken(ctx, memberSession)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock())
	_, memberToken, err := tokens.Create(ctx, memberActor, ports.TokenCommand{Name: "member fixture"}, "member-token")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := tokens.Create(ctx, adminActor, ports.TokenCommand{Name: "admin fixture"}, "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetAccountManagementService(accounts)
	router := httpapi.NewRouter(a, handler, authService, nil)
	path := "/api/v1/accounts/" + member.User.ID.String() + "/password"
	body := `{"current_password":"` + adminPassword + `","new_password":"` + newPassword + `"}`
	send := func(method, endpoint, key, input, credential, origin string, bearer bool, alter func(*http.Request)) *httptest.ResponseRecorder {
		a.Clock().(*clock.MockClock).Advance(6 * time.Second)
		r := httptest.NewRequest(method, "http://example.com"+endpoint, strings.NewReader(input))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if credential != "" {
			if bearer {
				r.Header.Set("Authorization", "Bearer "+credential)
			} else {
				r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: credential})
			}
		}
		if alter != nil {
			alter(r)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("account response cacheable", w.Code)
		}
		for _, secret := range []string{adminPassword, oldPassword, newPassword, adminSession, memberSession, memberToken, adminToken} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("credential leaked in reset response")
			}
		}
		return w
	}
	for _, tc := range []struct {
		name, credential, origin string
		bearer                   bool
		status                   int
	}{
		{"anonymous", "", "http://example.com", false, http.StatusUnauthorized},
		{"member", memberSession, "http://example.com", false, http.StatusForbidden},
		{"session bearer", adminSession, "http://example.com", true, http.StatusForbidden},
		{"read token", adminToken, "http://example.com", true, http.StatusForbidden},
		{"cross origin", adminSession, "https://foreign.example", false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := send(http.MethodPost, path, "denied", body, tc.credential, tc.origin, tc.bearer, nil); w.Code != tc.status {
				t.Fatal("admission", w.Code)
			}
		})
	}
	for _, tc := range []struct{ name, body string }{
		{"duplicate", `{"current_password":"x","new_password":"abcdefgh","new_password":"ijklmnop"}`},
		{"escaped duplicate", `{"current_password":"x","new_password":"abcdefgh","new\u005fpassword":"ijklmnop"}`},
		{"case alias", `{"current_password":"x","New_Password":"abcdefgh"}`},
		{"null", `{"current_password":"x","new_password":null}`},
		{"nonstring", `{"current_password":"x","new_password":[]}`},
		{"unknown authority", `{"current_password":"x","new_password":"abcdefgh","is_active":true}`},
		{"wrong factor type", `{"current_password":"x","new_password":"abcdefgh","otp":123456}`},
		{"both factors", `{"current_password":"x","new_password":"abcdefgh","otp":"123456","recovery_code":"abcdef"}`},
		{"missing current", `{"new_password":"abcdefgh"}`},
		{"trailing", body + ` {}`},
		{"target override", body[:len(body)-1] + `,"target_id":"` + admin.ID.String() + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := send(http.MethodPost, path, "invalid", tc.body, adminSession, "http://example.com", false, nil); w.Code != http.StatusBadRequest {
				t.Fatal("strict reset body", w.Code)
			}
		})
	}
	for _, alter := range []func(*http.Request){
		func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") },
		func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: adminSession}) },
		func(r *http.Request) { r.Header.Add("Content-Type", "application/json") },
	} {
		if w := send(http.MethodPost, path, "duplicate", body, adminSession, "http://example.com", false, alter); w.Code != http.StatusBadRequest {
			t.Fatal("duplicate admission", w.Code)
		}
	}
	if w := send(http.MethodPost, path, "wrong-admin", strings.Replace(body, adminPassword, "wrong", 1), adminSession, "http://example.com", false, nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "REAUTHENTICATION_FAILED") {
		t.Fatal("reauthentication", w.Code)
	}
	if w := send(http.MethodPost, "/api/v1/accounts/"+admin.ID.String()+"/password", "self", body, adminSession, "http://example.com", false, nil); w.Code != http.StatusForbidden {
		t.Fatal("self reset", w.Code)
	}
	var prior int
	if err = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM account_security_command WHERE action='account.member_password.reset'").Scan(&prior); err != nil || prior != 0 {
		t.Fatal("rejected commands wrote receipts", err, prior)
	}
	w := send(http.MethodPost, path, "reset-original", body, adminSession, "http://example.com", false, nil)
	if w.Code != http.StatusOK {
		t.Fatal("reset", w.Code, w.Body.String())
	}
	var result ports.AccountCommandResult
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.User == nil || result.User.ID != member.User.ID || result.Replayed {
		t.Fatal("reset receipt", err)
	}
	for _, old := range []string{memberSession, memberToken} {
		if _, err = authService.AuthenticateToken(ctx, old); err == nil {
			t.Fatal("target credential survived reset")
		}
	}
	for _, retained := range []string{adminSession, adminToken} {
		if _, err = authService.AuthenticateToken(ctx, retained); err != nil {
			t.Fatal("acting admin credential revoked", err)
		}
	}
	if _, _, err = authService.Login(ctx, "ResetMember", oldPassword); err == nil {
		t.Fatal("old member password accepted")
	}
	if _, _, err = authService.Login(ctx, "ResetMember", newPassword); err != nil {
		t.Fatal("new member password rejected", err)
	}
	w = send(http.MethodPost, path, "reset-original", body, adminSession, "http://example.com", false, nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Replayed {
		t.Fatal("original reset replay", w.Code)
	}
	if w = send(http.MethodPost, path, "reset-original", strings.Replace(body, newPassword, "ChangedResetFixture123!", 1), adminSession, "http://example.com", false, nil); w.Code != http.StatusConflict {
		t.Fatal("changed reset input", w.Code)
	}
	if w = send(http.MethodGet, "/api/v1/auth/security/commands/reset-original", "", "", adminSession, "", false, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "account.member_password.reset") {
		t.Fatal("safe reset recovery", w.Code)
	}
	var receipts, audits, events int
	err = a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM account_security_command WHERE action='account.member_password.reset'),(SELECT count(*) FROM audit_log WHERE action='account.member_password.reset'),(SELECT count(*) FROM outbox_event WHERE event_type='account.member_password.reset')`).Scan(&receipts, &audits, &events)
	if err != nil || receipts != 1 || audits != 1 || events != 1 {
		t.Fatal("reset transaction counts", err, receipts, audits, events)
	}
}
