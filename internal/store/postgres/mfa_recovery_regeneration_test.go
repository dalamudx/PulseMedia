package postgres_test

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const recoveryRegenerationAction = "account.mfa.recovery.regenerated"

type recoveryRegenerationFixture struct {
	*memberResetFixture
	secret string
	codes  []string
}

func newRecoveryRegenerationFixture(t *testing.T) *recoveryRegenerationFixture {
	t.Helper()
	f := &recoveryRegenerationFixture{memberResetFixture: newMemberResetFixture(t)}
	setup, err := f.mfa.Begin(t.Context(), accountActor(f.admin), f.raw, "Password123!", "setup")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.mfa.Confirm(t.Context(), accountActor(f.admin), f.raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, f.clock.Now()), "confirm")
	if err != nil {
		t.Fatal(err)
	}
	f.secret, f.codes = setup.Secret, out.RecoveryCodes
	return f
}

func (f *recoveryRegenerationFixture) regenerate(ctx context.Context, c ports.MFAReauthentication, key string) (*ports.MFAConfirmResult, error) {
	return f.mfa.RegenerateRecoveryCodes(ctx, accountActor(f.admin), f.raw, c, key)
}

func (f *recoveryRegenerationFixture) recoveryCommand() ports.MFAReauthentication {
	return ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{RecoveryCode: f.codes[0]}}
}

func (f *recoveryRegenerationFixture) binding(t *testing.T) (string, int64) {
	t.Helper()
	var binding string
	var counter int64
	if err := f.pool.QueryRow(t.Context(), `SELECT (to_jsonb(t)-'updated_at'-'last_counter')::text,last_counter FROM account_totp t WHERE user_id=$1`, f.admin.ID).Scan(&binding, &counter); err != nil {
		t.Fatal(err)
	}
	return binding, counter
}

// Includes used codes and credential rows so rollback comparisons detect partial effects.
func (f *recoveryRegenerationFixture) snapshot(t *testing.T) string {
	t.Helper()
	var snapshot string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY code_hash) FROM account_totp_recovery t WHERE user_id=$1),
		(SELECT to_jsonb(t) FROM account_totp t WHERE user_id=$1),
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM app_session t WHERE user_id=$1),
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM api_token t WHERE user_id=$1),
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY command_key) FROM account_security_command t WHERE owner_id=$1),
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM audit_log t),
		(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM outbox_event t))::text`, f.admin.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f *recoveryRegenerationFixture) otherCredentials(t *testing.T) string {
	t.Helper()
	_, raw, err := f.login.Login(t.Context(), "Admin", "Password123!", ports.MFAProof{RecoveryCode: f.codes[9]})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'recovery-regeneration',$3,'["read"]')`, uuid.New(), f.admin.ID, crypto.HashToken("regeneration-other-token"))
	return raw
}

func (f *recoveryRegenerationFixture) assertReceipt(t *testing.T, key string, codes []string) {
	t.Helper()
	receipt, err := f.accounts.ReadCommand(t.Context(), accountActor(f.admin), f.raw, key)
	if err != nil || receipt == nil || receipt.Action != recoveryRegenerationAction || string(receipt.Result) != `{"enabled": true, "replayed": false}` {
		t.Fatal("unsafe receipt", receipt, err)
	}
	f.count(t, `SELECT count(*) FROM audit_log WHERE action=$1`, 1, recoveryRegenerationAction)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type=$1`, 1, recoveryRegenerationAction)
	for _, code := range codes {
		f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, 1, f.admin.ID, crypto.HashToken("pulse.mfa.recovery.v1:"+code))
		f.count(t, `SELECT count(*) FROM audit_log WHERE payload::text LIKE $1`, 0, "%"+code+"%")
		f.count(t, `SELECT count(*) FROM outbox_event WHERE payload::text LIKE $1`, 0, "%"+code+"%")
	}
}

func TestMFARecoveryRegenerationReplacementPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	ctx := t.Context()
	other := f.otherCredentials(t)
	binding, counter := f.binding(t)
	command := f.recoveryCommand()
	out, err := f.regenerate(ctx, command, "regenerate")
	if err != nil || out == nil || !out.Enabled || out.Replayed || len(out.RecoveryCodes) != 10 {
		t.Fatal("regenerate", out, err)
	}
	seen := map[string]bool{}
	for _, code := range out.RecoveryCodes {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(code)
		if err != nil || len(decoded) != 16 || seen[code] {
			t.Fatal("invalid random code")
		}
		seen[code] = true
	}
	if next, nextCounter := f.binding(t); next != binding || nextCounter != counter {
		t.Fatal("recovery proof changed TOTP binding")
	}
	f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1`, 10, f.admin.ID)
	for _, code := range f.codes {
		f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND code_hash=$2`, 0, f.admin.ID, crypto.HashToken("pulse.mfa.recovery.v1:"+code))
		if _, _, err := f.login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: code}); !errors.Is(err, ports.ErrInvalidCredentials) {
			t.Fatal("old code accepted", err)
		}
	}
	for _, raw := range []string{other, "regeneration-other-token"} {
		if _, err := f.login.AuthenticateToken(ctx, raw); err == nil {
			t.Fatal("other credential survived")
		}
	}
	if _, err := f.login.AuthenticateToken(ctx, f.raw); err != nil {
		t.Fatal("current session revoked", err)
	}
	f.assertReceipt(t, "regenerate", out.RecoveryCodes)
	before := f.snapshot(t)
	replay, err := f.regenerate(ctx, command, "regenerate")
	if err != nil || replay == nil || !replay.Enabled || !replay.Replayed || len(replay.RecoveryCodes) != 0 {
		t.Fatal("unsafe replay", replay, err)
	}
	changed := command
	changed.RecoveryCode = f.codes[1]
	if _, err := f.regenerate(ctx, changed, "regenerate"); !errors.Is(err, ports.ErrAccountCommandConflict) {
		t.Fatal("key input rebound", err)
	}
	if after := f.snapshot(t); after != before {
		t.Fatal("replay/conflict wrote state")
	}
	status, err := f.mfa.Status(ctx, accountActor(f.admin), f.raw)
	if err != nil || !status.Enabled || status.RecoveryCodesRemaining != 10 {
		t.Fatal("status", status, err)
	}
	for _, code := range out.RecoveryCodes {
		if _, _, err := f.login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: strings.ToLower(code)}); err != nil {
			t.Fatal("new code rejected", err)
		}
		if _, _, err := f.login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: code}); !errors.Is(err, ports.ErrInvalidCredentials) {
			t.Fatal("new code reused", err)
		}
	}
	status, err = f.mfa.Status(ctx, accountActor(f.admin), f.raw)
	if err != nil || status.RecoveryCodesRemaining != 0 {
		t.Fatal("new codes not consumed", status, err)
	}
	// Historical success remains recoverable even after MFA has been explicitly disabled.
	f.clock.Advance(30 * time.Second)
	if _, err := f.mfa.Disable(ctx, accountActor(f.admin), f.raw, ports.MFAReauthentication{CurrentPassword: command.CurrentPassword, MFAProof: ports.MFAProof{OTP: fixtureTOTP(t, f.secret, f.clock.Now())}}, "disable"); err != nil {
		t.Fatal(err)
	}
	replay, err = f.regenerate(ctx, command, "regenerate")
	if err != nil || replay == nil || !replay.Enabled || !replay.Replayed || len(replay.RecoveryCodes) != 0 {
		t.Fatal("historical replay after disable", replay, err)
	}
	if _, err := f.regenerate(ctx, command, "new-after-disable"); !errors.Is(err, ports.ErrAccountCommandConflict) {
		t.Fatal("new command accepted disabled MFA", err)
	}
}

func TestMFARecoveryRegenerationOTPPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	binding, counter := f.binding(t)
	f.clock.Advance(30 * time.Second)
	command := ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{OTP: fixtureTOTP(t, f.secret, f.clock.Now())}}
	out, err := f.regenerate(t.Context(), command, "otp")
	if err != nil || out == nil || len(out.RecoveryCodes) != 10 {
		t.Fatal("OTP proof", out, err)
	}
	if next, nextCounter := f.binding(t); next != binding || nextCounter <= counter || nextCounter != f.clock.Now().Unix()/30 {
		t.Fatal("TOTP binding/counter changed incorrectly")
	}
	if _, err := f.regenerate(t.Context(), command, "otp-reuse"); !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatal("OTP reused", err)
	}
	replay, err := f.regenerate(t.Context(), command, "otp")
	if err != nil || replay == nil || !replay.Replayed || len(replay.RecoveryCodes) != 0 {
		t.Fatal("OTP replay", replay, err)
	}
}

func TestMFARecoveryRegenerationRejectionsPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	before := f.snapshot(t)
	for _, tc := range []struct {
		name    string
		command ports.MFAReauthentication
		want    error
	}{
		{"password", ports.MFAReauthentication{CurrentPassword: "wrong", MFAProof: ports.MFAProof{RecoveryCode: f.codes[0]}}, ports.ErrInvalidCredentials},
		{"both", ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{OTP: "123456", RecoveryCode: f.codes[0]}}, ports.ErrInvalidAccountCommand},
		{"neither", ports.MFAReauthentication{CurrentPassword: "Password123!"}, ports.ErrInvalidCredentials},
		{"invalid-otp", ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{OTP: "abcdef"}}, ports.ErrInvalidCredentials},
		{"invalid-recovery", ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{RecoveryCode: "invalid"}}, ports.ErrInvalidCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := f.regenerate(t.Context(), tc.command, tc.name); out != nil || !errors.Is(err, tc.want) {
				t.Fatal("rejection", out, err)
			}
			if after := f.snapshot(t); after != before {
				t.Fatal("rejection mutated state")
			}
		})
	}
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-enabled-MFA/pending-%t", pending), func(t *testing.T) {
			if pending {
				if _, err := f.mfa.Begin(t.Context(), accountActor(f.member), f.memberRaw, "Member123!", "member-pending"); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.mfa.RegenerateRecoveryCodes(t.Context(), accountActor(f.member), f.memberRaw, ports.MFAReauthentication{CurrentPassword: "Member123!"}, "member-regenerate")
			if out != nil || !errors.Is(err, ports.ErrAccountCommandConflict) {
				t.Fatal("no enabled MFA accepted", out, err)
			}
			f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1`, 0, f.member.ID)
		})
	}
}

func TestMFARecoveryRegenerationConcurrentPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	command := f.recoveryCommand()
	start := make(chan struct{})
	type result struct {
		out *ports.MFAConfirmResult
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := f.regenerate(t.Context(), command, "concurrent")
			results <- result{out, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	first, replays := 0, 0
	for r := range results {
		if r.err != nil || r.out == nil || !r.out.Enabled {
			t.Fatal("concurrent command", r.err)
		}
		if r.out.Replayed {
			replays++
			if len(r.out.RecoveryCodes) != 0 {
				t.Fatal("concurrent replay leaked")
			}
		} else {
			first++
			if len(r.out.RecoveryCodes) != 10 {
				t.Fatal("missing first codes")
			}
			f.assertReceipt(t, "concurrent", r.out.RecoveryCodes)
		}
	}
	if first != 1 || replays != 1 {
		t.Fatal("multiple plaintext responses", first, replays)
	}
	f.count(t, `SELECT count(*) FROM account_security_command WHERE action=$1`, 1, recoveryRegenerationAction)
	f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1`, 10, f.admin.ID)
}

func TestMFARecoveryRegenerationRollbackPG(t *testing.T) {
	for _, factor := range []string{"recovery", "otp"} {
		t.Run(factor, func(t *testing.T) {
			f := newRecoveryRegenerationFixture(t)
			other := f.otherCredentials(t)
			command := f.recoveryCommand()
			if factor == "otp" {
				f.clock.Advance(30 * time.Second)
				command.MFAProof = ports.MFAProof{OTP: fixtureTOTP(t, f.secret, f.clock.Now())}
			}
			f.exec(t, `CREATE FUNCTION reject_regeneration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='account.mfa.recovery.regenerated' THEN RAISE EXCEPTION 'synthetic audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_regeneration_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_regeneration_audit()`)
			before := f.snapshot(t)
			if out, err := f.regenerate(t.Context(), command, "rollback"); out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("audit failure", out, err)
			}
			if after := f.snapshot(t); after != before {
				t.Fatal("audit failure changed codes/factor/credentials/receipt/outbox")
			}
			for _, raw := range []string{f.raw, other, "regeneration-other-token"} {
				if _, err := f.login.AuthenticateToken(t.Context(), raw); err != nil {
					t.Fatal("rollback revoked credentials", err)
				}
			}
			f.exec(t, `DROP TRIGGER reject_regeneration_audit ON audit_log`)
			out, err := f.regenerate(t.Context(), command, "rollback")
			if err != nil || out == nil || out.Replayed || len(out.RecoveryCodes) != 10 {
				t.Fatal("factor not restored by rollback", out, err)
			}
		})
	}
}

func TestMFARecoveryRegenerationCurrentAuthorizationPG(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, change := range []struct {
			name, sql string
			want      error
		}{
			{"disabled", `UPDATE app_user SET is_active=false WHERE id=$1`, ports.ErrForbidden},
			{"expired", `UPDATE app_session SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, ports.ErrUnauthorized},
			{"revoked", `DELETE FROM app_session WHERE user_id=$1`, ports.ErrUnauthorized},
		} {
			t.Run(fmt.Sprintf("%s/replay-%t", change.name, replay), func(t *testing.T) {
				f := newRecoveryRegenerationFixture(t)
				command := f.recoveryCommand()
				if replay {
					if _, err := f.regenerate(t.Context(), command, "authorization"); err != nil {
						t.Fatal(err)
					}
				}
				f.exec(t, change.sql, f.admin.ID)
				before := f.snapshot(t)
				if out, err := f.regenerate(t.Context(), command, "authorization"); out != nil || !errors.Is(err, change.want) {
					t.Fatal("stale authorization", out, err)
				}
				if replay {
					if receipt, err := f.accounts.ReadCommand(t.Context(), accountActor(f.admin), f.raw, "authorization"); receipt != nil || !errors.Is(err, change.want) {
						t.Fatal("GET bypassed authorization", receipt, err)
					}
				}
				if after := f.snapshot(t); after != before {
					t.Fatal("rejected authorization changed state")
				}
			})
		}
	}
}
