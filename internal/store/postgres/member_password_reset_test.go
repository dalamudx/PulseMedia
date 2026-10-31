package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const resetAction = "account.member_password.reset"

type memberResetFixture struct {
	pool           *pgxpool.Pool
	login          *auth.Service
	accounts       *auth.AccountManagementService
	mfa            *auth.MFAService
	clock          *clock.MockClock
	admin, member  *ports.User
	raw, memberRaw string
}

func newMemberResetFixture(t *testing.T) *memberResetFixture {
	t.Helper()
	pool, repo, c := newAuthenticationFixture(t)
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	store := postgres.NewAccountManagementStore(pool, ids)
	f := &memberResetFixture{pool: pool, clock: c, login: auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids), accounts: auth.NewAccountManagementService(store, crypto.AuthenticationSecrets{}, c, ids, cipher), mfa: auth.NewMFAService(store, crypto.AuthenticationSecrets{}, cipher, c, ids)}
	f.login.SetMFACipher(cipher)
	var err error
	f.admin, f.raw, err = f.login.SetupAdmin(t.Context(), "Admin", "Password123!")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.accounts.Create(t.Context(), accountActor(f.admin), f.raw, ports.AccountCreate{Username: "Member", Password: "Member123!", Role: ports.RoleMember}, "member")
	if err != nil {
		t.Fatal(err)
	}
	f.member = result.User
	_, f.memberRaw, err = f.login.Login(t.Context(), "Member", "Member123!")
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func resetCommand() ports.PasswordChange {
	return ports.PasswordChange{CurrentPassword: "Password123!", NewPassword: "Changed123!"}
}
func (f *memberResetFixture) reset(ctx context.Context, c ports.PasswordChange, key string) (*ports.AccountCommandResult, error) {
	return f.accounts.ResetMemberPassword(ctx, accountActor(f.admin), f.raw, f.member.ID, c, key)
}
func (f *memberResetFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *memberResetFixture) count(t *testing.T, sql string, want int, args ...any) {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil || n != want {
		t.Fatalf("count got %d want %d: %v", n, want, err)
	}
}
func (f *memberResetFixture) enableMFA(t *testing.T, u *ports.User, raw, password, key string) []string {
	t.Helper()
	setup, err := f.mfa.Begin(t.Context(), accountActor(u), raw, password, key+"-begin")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.mfa.Confirm(t.Context(), accountActor(u), raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, f.clock.Now()), key+"-confirm")
	if err != nil {
		t.Fatal(err)
	}
	return out.RecoveryCodes
}

func TestMemberPasswordResetCredentialsPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx := t.Context()
	_, other, err := f.login.Login(ctx, "Member", "Member123!")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'member',$3,'["read"]'),($4,$5,'admin',$6,'["read"]')`, uuid.New(), f.member.ID, crypto.HashToken("member-reset-token"), uuid.New(), f.admin.ID, crypto.HashToken("admin-reset-token"))
	pending, err := f.mfa.Begin(ctx, accountActor(f.member), f.memberRaw, "Member123!", "pending")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO account_totp_recovery(user_id,code_hash) VALUES($1,$2)", f.member.ID, crypto.HashToken("pending-recovery"))
	out, err := f.reset(ctx, resetCommand(), "reset")
	if err != nil || out.User == nil || out.User.ID != f.member.ID || !out.User.IsActive || out.Replayed {
		t.Fatal("reset", out, err)
	}
	for _, raw := range []string{f.memberRaw, other, "member-reset-token"} {
		if _, err := f.login.AuthenticateToken(ctx, raw); err == nil {
			t.Fatal("target credential survived")
		}
	}
	for _, raw := range []string{f.raw, "admin-reset-token"} {
		if _, err := f.login.AuthenticateToken(ctx, raw); err != nil {
			t.Fatal("admin affected", err)
		}
	}
	if _, _, err := f.login.Login(ctx, "Member", "Member123!"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal("old password", err)
	}
	_, fresh, err := f.login.Login(ctx, "Member", "Changed123!")
	if err != nil {
		t.Fatal("new password", err)
	}
	if _, err := f.mfa.Confirm(ctx, accountActor(f.member), fresh, pending.ChallengeID, fixtureTOTP(t, pending.Secret, f.clock.Now()), "stale-confirm"); !errors.Is(err, ports.ErrAccountCommandConflict) {
		t.Fatal("pending authority survived", err)
	}
	f.count(t, "SELECT count(*) FROM account_totp WHERE user_id=$1", 0, f.member.ID)
	f.count(t, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1", 0, f.member.ID)
	replay, err := f.reset(ctx, resetCommand(), "reset")
	if err != nil || !replay.Replayed {
		t.Fatal("replay", err)
	}
	if _, err := f.login.AuthenticateToken(ctx, fresh); err != nil {
		t.Fatal("replay revoked fresh credential", err)
	}
	for _, query := range []string{"SELECT count(*) FROM account_security_command WHERE action=$1", "SELECT count(*) FROM audit_log WHERE action=$1", "SELECT count(*) FROM outbox_event WHERE event_type=$1"} {
		f.count(t, query, 1, resetAction)
	}
	receipt, err := f.accounts.ReadCommand(ctx, accountActor(f.admin), f.raw, "reset")
	if err != nil || receipt.Action != resetAction || strings.Contains(string(receipt.Result), "Password") || strings.Contains(string(receipt.Result), "Changed123!") {
		t.Fatal("unsafe receipt", err)
	}
}

func TestMemberPasswordResetTargetStatePG(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled-mfa"}[enabled], func(t *testing.T) {
			f := newMemberResetFixture(t)
			ctx := t.Context()
			var before string
			if enabled {
				f.enableMFA(t, f.member, f.memberRaw, "Member123!", "target")
				if err := f.pool.QueryRow(ctx, `SELECT row_to_json(a)::text || (SELECT jsonb_agg(to_jsonb(r) ORDER BY code_hash)::text FROM account_totp_recovery r WHERE r.user_id=a.user_id) FROM account_totp a WHERE user_id=$1`, f.member.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
			} else {
				f.exec(t, "UPDATE app_user SET is_active=false WHERE id=$1", f.member.ID)
			}
			out, err := f.reset(ctx, resetCommand(), "reset")
			if err != nil || out.User.IsActive != enabled {
				t.Fatal("target state", err)
			}
			if enabled {
				var after string
				if err := f.pool.QueryRow(ctx, `SELECT row_to_json(a)::text || (SELECT jsonb_agg(to_jsonb(r) ORDER BY code_hash)::text FROM account_totp_recovery r WHERE r.user_id=a.user_id) FROM account_totp a WHERE user_id=$1`, f.member.ID).Scan(&after); err != nil || after != before {
					t.Fatal("enabled MFA changed", err)
				}
				if _, _, err := f.login.Login(ctx, "Member", "Changed123!"); !errors.Is(err, ports.ErrInvalidCredentials) {
					t.Fatal("target MFA bypass", err)
				}
			} else {
				f.exec(t, "UPDATE app_user SET is_active=true WHERE id=$1", f.member.ID)
				if _, _, err := f.login.Login(ctx, "Member", "Changed123!"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMemberPasswordResetAdminMFAAndRollbackPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx := t.Context()
	codes := f.enableMFA(t, f.admin, f.raw, "Password123!", "admin")
	c := resetCommand()
	if _, err := f.reset(ctx, c, "no-factor"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal("missing admin factor", err)
	}
	c.MFAProof = ports.MFAProof{RecoveryCode: codes[0]}
	c.CurrentPassword = "wrong-password"
	if _, err := f.reset(ctx, c, "wrong-password"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	c.CurrentPassword = "Password123!"
	f.exec(t, `CREATE FUNCTION reject_member_reset_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='account.member_password.reset' THEN RAISE EXCEPTION 'synthetic audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_member_reset_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_member_reset_audit()`)
	if _, err := f.reset(ctx, c, "reset"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("audit failure", err)
	}
	f.count(t, "SELECT count(*) FROM account_security_command WHERE action=$1", 0, resetAction)
	f.count(t, "SELECT count(*) FROM outbox_event WHERE event_type=$1", 0, resetAction)
	status, err := f.mfa.Status(ctx, accountActor(f.admin), f.raw)
	if err != nil || status.RecoveryCodesRemaining != 10 {
		t.Fatal("rollback consumed recovery", err)
	}
	if _, err := f.login.AuthenticateToken(ctx, f.memberRaw); err != nil {
		t.Fatal("rollback revoked member", err)
	}
	if _, _, err := f.login.Login(ctx, "Member", "Member123!"); err != nil {
		t.Fatal("rollback password", err)
	}
	f.exec(t, "DROP TRIGGER reject_member_reset_audit ON audit_log")
	if _, err := f.reset(ctx, c, "reset"); err != nil {
		t.Fatal("retry", err)
	}
	if out, err := f.reset(ctx, c, "reset"); err != nil || !out.Replayed {
		t.Fatal("factor replay", err)
	}
	status, err = f.mfa.Status(ctx, accountActor(f.admin), f.raw)
	if err != nil || !status.Enabled || status.RecoveryCodesRemaining != 9 {
		t.Fatal("factor consumption", err)
	}
	if _, err := f.reset(ctx, c, "reuse-factor"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal("recovery reused", err)
	}
	f.count(t, "SELECT count(*) FROM audit_log WHERE action=$1", 1, resetAction)
}

func TestMemberPasswordResetOTPRollbackPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx := t.Context()
	setup, err := f.mfa.Begin(ctx, accountActor(f.admin), f.raw, "Password123!", "admin-begin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.mfa.Confirm(ctx, accountActor(f.admin), f.raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, f.clock.Now()), "admin-confirm"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.mfa.Begin(ctx, accountActor(f.member), f.memberRaw, "Member123!", "target-pending"); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO account_totp_recovery(user_id,code_hash) VALUES($1,$2)", f.member.ID, crypto.HashToken("pending"))
	f.exec(t, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'member',$3,'["read"]')`, uuid.New(), f.member.ID, crypto.HashToken("rollback-token"))
	var before int64
	if err = f.pool.QueryRow(ctx, "SELECT last_counter FROM account_totp WHERE user_id=$1", f.admin.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(30 * time.Second)
	c := resetCommand()
	c.MFAProof = ports.MFAProof{OTP: fixtureTOTP(t, setup.Secret, f.clock.Now())}
	f.exec(t, `CREATE FUNCTION reject_reset_otp_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='account.member_password.reset' THEN RAISE EXCEPTION 'synthetic fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_reset_otp_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_reset_otp_audit()`)
	if _, err = f.reset(ctx, c, "otp-reset"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("audit fault", err)
	}
	var after int64
	if err = f.pool.QueryRow(ctx, "SELECT last_counter FROM account_totp WHERE user_id=$1", f.admin.ID).Scan(&after); err != nil || after != before {
		t.Fatal("rollback consumed OTP", err)
	}
	f.count(t, "SELECT count(*) FROM account_totp WHERE user_id=$1 AND NOT enabled", 1, f.member.ID)
	f.count(t, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1", 1, f.member.ID)
	for _, raw := range []string{f.memberRaw, "rollback-token"} {
		if _, err = f.login.AuthenticateToken(ctx, raw); err != nil {
			t.Fatal("rollback credential", err)
		}
	}
	f.exec(t, "DROP TRIGGER reject_reset_otp_audit ON audit_log")
	if _, err = f.reset(ctx, c, "otp-reset"); err != nil {
		t.Fatal("OTP retry", err)
	}
	if out, err := f.reset(ctx, c, "otp-reset"); err != nil || !out.Replayed {
		t.Fatal("OTP replay", err)
	}
	if _, err = f.reset(ctx, c, "otp-reuse"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal("OTP reused", err)
	}
	f.count(t, "SELECT count(*) FROM account_totp WHERE user_id=$1", 0, f.member.ID)
	f.count(t, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1", 0, f.member.ID)
	f.count(t, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND used_at IS NULL", 10, f.admin.ID)
}

func TestMemberPasswordResetAuthorizationAndFingerprintPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx := t.Context()
	c := resetCommand()
	if _, err := f.accounts.ResetMemberPassword(ctx, accountActor(f.admin), f.raw, f.admin.ID, c, "self"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("self", err)
	}
	second, err := f.accounts.Create(ctx, accountActor(f.admin), f.raw, ports.AccountCreate{Username: "Second", Password: "Password123!", Role: ports.RoleAdmin}, "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.ResetMemberPassword(ctx, accountActor(f.admin), f.raw, second.User.ID, c, "admin-target"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("admin target", err)
	}
	if _, err := f.accounts.ResetMemberPassword(ctx, accountActor(f.member), f.memberRaw, f.admin.ID, c, "member-actor"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member actor", err)
	}
	if _, err := f.reset(ctx, c, "reset"); err != nil {
		t.Fatal(err)
	}
	token := *accountActor(f.admin)
	token.Scopes = []string{"read"}
	if _, err := f.accounts.ResetMemberPassword(ctx, &token, f.raw, f.member.ID, c, "reset"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("token replay", err)
	}
	for _, changed := range []ports.PasswordChange{{CurrentPassword: "other-password", NewPassword: c.NewPassword}, {CurrentPassword: c.CurrentPassword, NewPassword: "Different123!"}, {CurrentPassword: c.CurrentPassword, NewPassword: c.NewPassword, MFAProof: ports.MFAProof{OTP: "123456"}}, {CurrentPassword: c.CurrentPassword, NewPassword: c.NewPassword, MFAProof: ports.MFAProof{RecoveryCode: "changed"}}} {
		if _, err := f.reset(ctx, changed, "reset"); !errors.Is(err, ports.ErrAccountCommandConflict) {
			t.Fatal("field omitted from fingerprint", err)
		}
	}
	if _, err := f.accounts.ResetMemberPassword(ctx, accountActor(f.admin), f.raw, second.User.ID, c, "reset"); !errors.Is(err, ports.ErrAccountCommandConflict) {
		t.Fatal("target omitted from fingerprint", err)
	}
	f.exec(t, "UPDATE app_user SET role='member' WHERE id=$1", f.admin.ID)
	if _, err := f.reset(ctx, c, "reset"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("demoted replay", err)
	}
	if _, err := f.accounts.ReadCommand(ctx, accountActor(f.admin), f.raw, "reset"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("demoted receipt", err)
	}
}

func TestMemberPasswordResetConcurrentReplayPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	codes := f.enableMFA(t, f.admin, f.raw, "Password123!", "admin")
	c := resetCommand()
	c.MFAProof = ports.MFAProof{RecoveryCode: codes[0]}
	var wg sync.WaitGroup
	results := make(chan *ports.AccountCommandResult, 2)
	failures := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; out, err := f.reset(ctx, c, "same"); results <- out; failures <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	replays := 0
	for out := range results {
		if out.Replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatal("replays", replays)
	}
	f.count(t, "SELECT count(*) FROM account_security_command WHERE action=$1", 1, resetAction)
	f.count(t, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND used_at IS NOT NULL", 1, f.admin.ID)
}

func TestMemberPasswordResetCurrentStateFencesPG(t *testing.T) {
	for _, change := range []struct {
		name, sql   string
		targetAdmin bool
		want        error
	}{
		{"promotion", "UPDATE app_user SET role='admin' WHERE id=$1", false, ports.ErrForbidden},
		{"admin-demotion", "UPDATE app_user SET role='member' WHERE id=$1", true, ports.ErrForbidden},
		{"admin-disabled", "UPDATE app_user SET is_active=false WHERE id=$1", true, ports.ErrForbidden},
		{"admin-session-revoked", "DELETE FROM app_session WHERE user_id=$1", true, ports.ErrUnauthorized},
		{"admin-password", "UPDATE app_user SET password_hash='changed-under-lock' WHERE id=$1", true, ports.ErrInvalidCredentials},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newMemberResetFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			// Serialize before the reset obtains its ordered account locks.
			if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x50756c734143)); err != nil {
				t.Fatal(err)
			}
			id := f.member.ID
			if change.targetAdmin {
				id = f.admin.ID
			}
			if _, err = tx.Exec(ctx, change.sql, id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.reset(ctx, resetCommand(), "fenced"); done <- e }()
			joined := false
			defer func() {
				if !joined {
					cancel()
					_ = tx.Rollback(context.WithoutCancel(ctx))
					<-done
				}
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock%')`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					joined = true
					t.Fatalf("reset passed lock: %v", err)
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			joined = true
			if !errors.Is(err, change.want) {
				t.Fatal("stale state accepted", err)
			}
			f.count(t, "SELECT count(*) FROM account_security_command WHERE action=$1", 0, resetAction)
			f.count(t, "SELECT count(*) FROM audit_log WHERE action=$1", 0, resetAction)
		})
	}
}
