package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func accountActor(u *ports.User) *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: u.ID.String(), Name: u.Username, Role: u.Role}
}
func TestAccountManagementLifecyclePG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewAccountManagementService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, c, ids, cipher)
	admin, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	a := accountActor(admin)
	create := ports.AccountCreate{Username: "Family", Password: "Family123!", Role: ports.RoleMember}
	member, e := svc.Create(ctx, a, raw, create, "create-family")
	if e != nil {
		t.Fatal(e)
	}
	replay, e := svc.Create(ctx, a, raw, create, "create-family")
	if e != nil || !replay.Replayed || replay.User.ID != member.User.ID {
		t.Fatal("create replay", e)
	}
	create.Role = ports.RoleAdmin
	if _, e = svc.Create(ctx, a, raw, create, "create-family"); !errors.Is(e, ports.ErrAccountCommandConflict) {
		t.Fatal("changed original command accepted", e)
	}
	family, familyRaw, e := login.Login(ctx, "family", "Family123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.List(ctx, accountActor(family), familyRaw); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("member listed accounts", e)
	}
	tokenActor := *a
	tokenActor.Scopes = []string{"read"}
	if _, e = svc.List(ctx, &tokenActor, raw); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("token elevated", e)
	}
	forged := *accountActor(family)
	forged.Role = ports.RoleAdmin
	if _, e = svc.List(ctx, &forged, familyRaw); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("forged role elevated", e)
	}
	if _, e = svc.List(ctx, a, familyRaw); !errors.Is(e, ports.ErrUnauthorized) {
		t.Fatal("wrong session owner accepted", e)
	}
	if _, e = svc.Update(ctx, a, raw, admin.ID, ports.AccountUpdate{Role: ports.RoleMember, IsActive: true}, "demote-last"); !errors.Is(e, ports.ErrLastActiveAdmin) {
		t.Fatal("last admin demoted", e)
	}
	if _, e = svc.Update(ctx, a, raw, family.ID, ports.AccountUpdate{Role: ports.RoleMember, IsActive: false}, "disable-family"); e != nil {
		t.Fatal(e)
	}
	if _, e = login.AuthenticateToken(ctx, familyRaw); e == nil {
		t.Fatal("disabled session survived")
	}
	if _, _, e = login.Login(ctx, "Family", "Family123!"); !errors.Is(e, ports.ErrUserDisabled) {
		t.Fatal("disabled login", e)
	}
	if _, e = svc.Update(ctx, a, raw, family.ID, ports.AccountUpdate{Role: ports.RoleMember, IsActive: true}, "enable-family"); e != nil {
		t.Fatal(e)
	}
	if _, e = login.AuthenticateToken(ctx, familyRaw); e == nil {
		t.Fatal("reactivation resurrected session")
	}
	if _, _, e = login.Login(ctx, "family", "Family123!"); e != nil {
		t.Fatal(e)
	}
	receipt, e := svc.ReadCommand(ctx, a, raw, "create-family")
	if e != nil || receipt.Action != "account.created" {
		t.Fatal("safe receipt", e)
	}
	var audits, receipts int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM account_security_command").Scan(&receipts); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action IN ('account.created','account.updated')").Scan(&audits); e != nil || audits != receipts || receipts != 3 {
		t.Fatalf("atomic receipts/audits %d/%d %v", receipts, audits, e)
	}
}
func TestAccountLastAdminConcurrentRevocationPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewAccountManagementService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, c, ids, cipher)
	first, firstRaw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	secondResult, e := svc.Create(ctx, accountActor(first), firstRaw, ports.AccountCreate{Username: "Second", Password: "Password123!", Role: ports.RoleAdmin}, "second-admin")
	if e != nil {
		t.Fatal(e)
	}
	second, secondRaw, e := login.Login(ctx, secondResult.User.Username, "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	var wg sync.WaitGroup
	for _, v := range []struct {
		user *ports.User
		raw  string
	}{{first, firstRaw}, {second, secondRaw}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := svc.Update(ctx, accountActor(v.user), v.raw, v.user.ID, ports.AccountUpdate{Role: ports.RoleMember, IsActive: true}, uuid.NewString())
			done <- e
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	passed, rejected := 0, 0
	for e := range done {
		if e == nil {
			passed++
		} else if errors.Is(e, ports.ErrLastActiveAdmin) {
			rejected++
		} else {
			t.Fatal(e)
		}
	}
	if passed != 1 || rejected != 1 {
		t.Fatalf("concurrent demotions %d/%d", passed, rejected)
	}
	var active int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM app_user WHERE is_active AND role='admin'").Scan(&active); e != nil || active != 1 {
		t.Fatal("last admin lost", e)
	}
}
func TestAccountPasswordAndSessionRevocationPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewAccountManagementService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, c, ids, cipher)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	a := accountActor(user)
	_, other, e := login.Login(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	sessions, e := svc.Sessions(ctx, a, raw)
	if e != nil || len(sessions) != 2 {
		t.Fatal("list sessions", e)
	}
	for _, session := range sessions {
		if !session.Current {
			if _, e = svc.RevokeSession(ctx, a, raw, session.ID, "revoke-other"); e != nil {
				t.Fatal(e)
			}
			if _, e = svc.RevokeSession(ctx, a, raw, session.ID, "revoke-other"); e != nil {
				t.Fatal("revoke replay", e)
			}
		}
	}
	if _, e = login.AuthenticateToken(ctx, other); e == nil {
		t.Fatal("revoked session accepted")
	}
	_, other, e = login.Login(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'old',$3,'[\"read\"]')", uuid.New(), user.ID, crypto.HashToken("old-api-token"))
	if e != nil {
		t.Fatal(e)
	}
	change := ports.PasswordChange{CurrentPassword: "wrong-password", NewPassword: "Changed123!"}
	if _, e = svc.ChangePassword(ctx, a, raw, change, "change"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("wrong current password", e)
	}
	change.CurrentPassword = "Password123!"
	if _, e = svc.ChangePassword(ctx, a, raw, change, "change"); e != nil {
		t.Fatal(e)
	}
	if _, e = login.AuthenticateToken(ctx, raw); e != nil {
		t.Fatal("current session revoked", e)
	}
	if _, e = login.AuthenticateToken(ctx, other); e == nil {
		t.Fatal("other session retained")
	}
	if _, e = login.AuthenticateToken(ctx, "old-api-token"); e == nil {
		t.Fatal("old API token retained")
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("old password accepted", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Changed123!"); e != nil {
		t.Fatal(e)
	}
	replay, e := svc.ChangePassword(ctx, a, raw, change, "change")
	if e != nil || !replay.Replayed {
		t.Fatal("password change receipt", e)
	}
	sessions, e = svc.Sessions(ctx, a, raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, session := range sessions {
		if session.Current {
			if _, e = svc.RevokeSession(ctx, a, raw, session.ID, "self-revoke"); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = svc.Sessions(ctx, a, raw); !errors.Is(e, ports.ErrUnauthorized) {
		t.Fatal("revoked current session got data", e)
	}
}
func TestAccountSecurityAuditFailureRollsBackPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewAccountManagementService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, c, ids, cipher)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, `CREATE FUNCTION reject_account_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit fault'; END $$; CREATE TRIGGER reject_account_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_account_audit()`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ChangePassword(ctx, accountActor(user), raw, ports.PasswordChange{CurrentPassword: "Password123!", NewPassword: "Changed123!"}, "faulted-change"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!"); e != nil {
		t.Fatal("password mutated without audit", e)
	}
	var n int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM account_security_command").Scan(&n); e != nil || n != 0 {
		t.Fatal("partial receipt", e)
	}
}
func TestAccountCurrentCredentialLockPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewAccountManagementService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, c, ids, cipher)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET password_hash='changed-under-lock' WHERE id=$1", user.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := svc.ChangePassword(ctx, accountActor(user), raw, ports.PasswordChange{CurrentPassword: "Password123!", NewPassword: "Changed123!"}, "stale-password")
		done <- e
	}()
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("stale password verified", e)
	}
}
