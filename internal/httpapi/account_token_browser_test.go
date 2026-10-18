package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountTokenBrowserHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("owned browser fixture required")
	}
	ctx := context.Background()
	a, _, _ := setupAuthHTTP(t)
	as := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	if _, _, err := as.SetupAdmin(ctx, "token-ui-admin", "AccountTokenNativeFixture123!"); err != nil {
		t.Fatal(err)
	}
	hash, err := crypto.HashPassword("AccountTokenNativeFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	member, err := db.New(a.DBPool()).CreateUser(ctx, db.CreateUserParams{ID: a.IDGen().NewUUID(), Username: "token-ui-member", PasswordHash: hash, Role: ports.RoleMember, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := as.Login(ctx, member.Username, "AccountTokenNativeFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, a.Clock()))
	server := httptest.NewServer(httpapi.NewRouter(a, h, as, nil))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	script, err := filepath.Abs("../../web/tests/account-tokens-browser.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(browserCtx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_ACCOUNT_TOKEN_BASE="+server.URL, "PULSE_ACCOUNT_TOKEN_SESSION="+session, "PULSE_ACCOUNT_TOKEN_OWNER="+member.ID.String())
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("account browser %v: %s", err, raw)
	}
	t.Log(string(raw))
	var tokens, audits, receipts int
	if err = a.DBPool().QueryRow(ctx, `SELECT count(*) FROM api_token WHERE user_id=$1`, member.ID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err = a.DBPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor_id=$1 AND action LIKE 'auth.token.%'`, member.ID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err = a.DBPool().QueryRow(ctx, `SELECT count(*) FROM idempotency_record WHERE actor_id=$1`, member.ID.String()).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if tokens != 3 || audits != 4 || receipts != 4 {
		t.Fatalf("duplicate or missing writes: tokens=%d audits=%d receipts=%d", tokens, audits, receipts)
	}
}
