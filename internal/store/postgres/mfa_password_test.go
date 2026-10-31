package postgres_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"testing"
)

func TestMFAPasswordReauthenticationAndPendingSetupPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	login.SetMFACipher(cipher)
	store := postgres.NewAccountManagementStore(pool, ids)
	svc := auth.NewMFAService(store, crypto.AuthenticationSecrets{}, cipher, c, ids)
	accounts := auth.NewAccountManagementService(store, crypto.AuthenticationSecrets{}, c, ids, cipher)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	a := accountActor(user)
	setup, e := svc.Begin(ctx, a, raw, "Password123!", "setup")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = accounts.ChangePassword(ctx, a, raw, ports.PasswordChange{CurrentPassword: "Password123!", NewPassword: "Changed123!"}, "password-before-confirm"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Confirm(ctx, a, raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, c.Now()), "old-challenge"); !errors.Is(e, ports.ErrAccountCommandConflict) {
		t.Fatal("password change retained old setup authority", e)
	}
	setup, e = svc.Begin(ctx, a, raw, "Changed123!", "setup-new")
	if e != nil {
		t.Fatal(e)
	}
	confirmed, e := svc.Confirm(ctx, a, raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, c.Now()), "confirm")
	if e != nil {
		t.Fatal(e)
	}
	change := ports.PasswordChange{CurrentPassword: "Changed123!", NewPassword: "PasswordThird123!"}
	if _, e = accounts.ChangePassword(ctx, a, raw, change, "password-without-factor"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("MFA password change did not require factor", e)
	}
	change.MFAProof = ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]}
	if _, e = pool.Exec(ctx, `CREATE FUNCTION reject_mfa_password_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='account.password.changed' THEN RAISE EXCEPTION 'synthetic receipt fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_mfa_password_receipt BEFORE INSERT ON account_security_command FOR EACH ROW EXECUTE FUNCTION reject_mfa_password_receipt()`); e != nil {
		t.Fatal(e)
	}
	if _, e = accounts.ChangePassword(ctx, a, raw, change, "password-with-factor"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("faulted receipt", e)
	}
	status, e := svc.Status(ctx, a, raw)
	if e != nil || status.RecoveryCodesRemaining != 10 {
		t.Fatal("faulted command consumed recovery", e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_mfa_password_receipt ON account_security_command"); e != nil {
		t.Fatal(e)
	}
	if _, e = accounts.ChangePassword(ctx, a, raw, change, "password-with-factor"); e != nil {
		t.Fatal("recovery retry after rollback", e)
	}
	status, e = svc.Status(ctx, a, raw)
	if e != nil || !status.Enabled || status.RecoveryCodesRemaining != 9 {
		t.Fatal("password change lost MFA state", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "PasswordThird123!"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("password change disabled MFA", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "PasswordThird123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]}); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("password change recovery reused", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "PasswordThird123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[1]}); e != nil {
		t.Fatal("new password and unused recovery", e)
	}
}
