package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

const otherSessionsAction = "account.sessions.others_revoked"

func otherSessionsCommand() ports.MFAReauthentication {
	return ports.MFAReauthentication{CurrentPassword: resetCommand().CurrentPassword}
}

func revokeOthers(t *testing.T, f *memberResetFixture, c ports.MFAReauthentication, key string) *ports.AccountCommandResult {
	t.Helper()
	out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, c, key)
	if err != nil || out == nil || out.SessionID == nil {
		t.Fatal("revoke others", out, err)
	}
	return out
}

func otherSessionsSnapshot(t *testing.T, f *memberResetFixture) string {
	t.Helper()
	return (&recoveryRegenerationFixture{memberResetFixture: f}).snapshot(t)
}

func TestOtherSessionsPreservationAndReplayPG(t *testing.T) {
	f := newMemberResetFixture(t)
	ctx := t.Context()
	var version int
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil || version != 180006 {
		t.Fatalf("requires PostgreSQL 18.6 (180006), got %d: %v", version, err)
	}
	_, other, err := f.login.Login(ctx, "Admin", otherSessionsCommand().CurrentPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'admin',$3,'["read"]'),($4,$5,'member',$6,'["read"]')`, uuid.New(), f.admin.ID, crypto.HashToken("other-sessions-admin-token"), uuid.New(), f.member.ID, crypto.HashToken("other-sessions-member-token"))
	var tokensBefore, tokensAfter string
	tokenSQL := `SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM api_token t`
	if err := f.pool.QueryRow(ctx, tokenSQL).Scan(&tokensBefore); err != nil {
		t.Fatal(err)
	}
	out := revokeOthers(t, f, otherSessionsCommand(), "revoke")
	if out.Replayed {
		t.Fatal("first result replayed")
	}
	f.count(t, `SELECT count(*) FROM app_session WHERE id=$1 AND user_id=$2 AND token_hash=$3`, 1, *out.SessionID, f.admin.ID, crypto.HashToken(f.raw))
	if _, err := f.login.AuthenticateToken(ctx, other); err == nil {
		t.Fatal("other session survived")
	}
	for _, raw := range []string{f.raw, f.memberRaw, "other-sessions-admin-token", "other-sessions-member-token"} {
		if _, err := f.login.AuthenticateToken(ctx, raw); err != nil {
			t.Fatal("preserved credential rejected", err)
		}
	}
	if err := f.pool.QueryRow(ctx, tokenSQL).Scan(&tokensAfter); err != nil || tokensBefore != tokensAfter {
		t.Fatal("tokens changed", err)
	}
	receipt, err := f.accounts.ReadCommand(ctx, accountActor(f.admin), f.raw, "revoke")
	var stored ports.AccountCommandResult
	if err != nil || receipt == nil || receipt.Action != otherSessionsAction {
		t.Fatal("receipt", receipt, err)
	}
	if err := json.Unmarshal(receipt.Result, &stored); err != nil || stored.SessionID == nil || *stored.SessionID != *out.SessionID || stored.Replayed {
		t.Fatal("stored result", stored, err)
	}
	f.count(t, `SELECT count(*) FROM account_security_command WHERE action=$1 AND target_id=$2`, 1, otherSessionsAction, *out.SessionID)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action=$1 AND target_id=$2`, 1, otherSessionsAction, out.SessionID.String())
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type=$1 AND aggregate_id=$2`, 1, otherSessionsAction, out.SessionID.String())
	_, later, err := f.login.Login(ctx, "Admin", otherSessionsCommand().CurrentPassword)
	if err != nil {
		t.Fatal(err)
	}
	before := otherSessionsSnapshot(t, f)
	// A later authenticated session must retrieve the original kept session, without executing again.
	replay, err := f.accounts.RevokeOtherSessions(ctx, accountActor(f.admin), later, otherSessionsCommand(), "revoke")
	if err != nil || replay == nil || !replay.Replayed || replay.SessionID == nil || *replay.SessionID != *out.SessionID {
		t.Fatal("later-session replay", replay, err)
	}
	changed := otherSessionsCommand()
	changed.CurrentPassword = "changed"
	if _, err := f.accounts.RevokeOtherSessions(ctx, accountActor(f.admin), later, changed, "revoke"); !errors.Is(err, ports.ErrAccountCommandConflict) {
		t.Fatal("changed body", err)
	}
	if after := otherSessionsSnapshot(t, f); before != after {
		t.Fatal("replay/conflict changed state")
	}
	if _, err := f.login.AuthenticateToken(ctx, later); err != nil {
		t.Fatal("replay revoked later login", err)
	}
}

func TestOtherSessionsZeroOthersAndMemberPG(t *testing.T) {
	f := newMemberResetFixture(t)
	// Pending enrollment is retained and does not make a second factor mandatory.
	if _, err := f.mfa.Begin(t.Context(), accountActor(f.admin), f.raw, otherSessionsCommand().CurrentPassword, "pending"); err != nil {
		t.Fatal(err)
	}
	var before, after string
	sql := `SELECT to_jsonb(t)::text FROM account_totp t WHERE user_id=$1`
	if err := f.pool.QueryRow(t.Context(), sql, f.admin.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	revokeOthers(t, f, otherSessionsCommand(), "zero")
	if err := f.pool.QueryRow(t.Context(), sql, f.admin.ID).Scan(&after); err != nil || before != after {
		t.Fatal("pending MFA changed", err)
	}
	// Member commands have the same self-only boundary, independent of administrator privileges.
	memberPassword := "other-sessions-member-password"
	hash, err := crypto.HashPassword(memberPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE app_user SET password_hash=$1 WHERE id=$2`, hash, f.member.ID)
	out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.member), f.memberRaw, ports.MFAReauthentication{CurrentPassword: memberPassword}, "member-zero")
	if err != nil || out == nil || out.SessionID == nil {
		t.Fatal("member", out, err)
	}
	f.count(t, `SELECT count(*) FROM app_session WHERE user_id=$1`, 1, f.admin.ID)
	f.count(t, `SELECT count(*) FROM app_session WHERE user_id=$1`, 1, f.member.ID)
}

func TestOtherSessionsProofRejectionsPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	before := f.snapshot(t)
	for _, tc := range []struct {
		name string
		c    ports.MFAReauthentication
		want error
	}{
		{"password", ports.MFAReauthentication{CurrentPassword: "wrong", MFAProof: ports.MFAProof{RecoveryCode: f.codes[0]}}, ports.ErrInvalidCredentials},
		{"empty-password", ports.MFAReauthentication{MFAProof: ports.MFAProof{RecoveryCode: f.codes[0]}}, ports.ErrInvalidCredentials},
		{"missing-factor", otherSessionsCommand(), ports.ErrInvalidCredentials},
		{"bad-otp", ports.MFAReauthentication{CurrentPassword: otherSessionsCommand().CurrentPassword, MFAProof: ports.MFAProof{OTP: "abcdef"}}, ports.ErrInvalidCredentials},
		{"bad-recovery", ports.MFAReauthentication{CurrentPassword: otherSessionsCommand().CurrentPassword, MFAProof: ports.MFAProof{RecoveryCode: "invalid"}}, ports.ErrInvalidCredentials},
		{"both", ports.MFAReauthentication{CurrentPassword: otherSessionsCommand().CurrentPassword, MFAProof: ports.MFAProof{OTP: "123456", RecoveryCode: f.codes[0]}}, ports.ErrInvalidAccountCommand},
		{"long-password", ports.MFAReauthentication{CurrentPassword: strings.Repeat("p", 1025)}, ports.ErrInvalidAccountCommand},
		{"long-otp", ports.MFAReauthentication{MFAProof: ports.MFAProof{OTP: "1234567"}}, ports.ErrInvalidAccountCommand},
		{"long-recovery", ports.MFAReauthentication{MFAProof: ports.MFAProof{RecoveryCode: strings.Repeat("x", 65)}}, ports.ErrInvalidAccountCommand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, tc.c, tc.name)
			if out != nil || !errors.Is(err, tc.want) {
				t.Fatal("proof rejection", out, err)
			}
			if f.snapshot(t) != before {
				t.Fatal("rejected proof changed state")
			}
		})
	}
	for _, key := range []string{"", strings.Repeat("k", 161)} {
		if out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, f.recoveryCommand(), key); out != nil || !errors.Is(err, ports.ErrInvalidAccountCommand) {
			t.Fatal("key bounds", out, err)
		}
	}
	if f.snapshot(t) != before {
		t.Fatal("invalid key consumed factor")
	}
}

func TestOtherSessionsCurrentAuthorizationPG(t *testing.T) {
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
				f := newMemberResetFixture(t)
				if replay {
					revokeOthers(t, f, otherSessionsCommand(), "auth")
				}
				f.exec(t, change.sql, f.admin.ID)
				before := otherSessionsSnapshot(t, f)
				out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, otherSessionsCommand(), "auth")
				if out != nil || !errors.Is(err, change.want) {
					t.Fatal("stale authorization", out, err)
				}
				if otherSessionsSnapshot(t, f) != before {
					t.Fatal("authorization failure changed state")
				}
			})
		}
	}
	f := newMemberResetFixture(t)
	revokeOthers(t, f, otherSessionsCommand(), "auth")
	before := otherSessionsSnapshot(t, f)
	for _, tc := range []struct {
		name string
		a    *ports.Actor
		raw  string
		want error
	}{
		{"nil", nil, f.raw, ports.ErrUnauthorized},
		{"empty-cookie", accountActor(f.admin), "", ports.ErrUnauthorized},
		{"mismatched-cookie", accountActor(f.admin), f.memberRaw, ports.ErrUnauthorized},
		{"unknown-cookie", accountActor(f.admin), "unknown", ports.ErrUnauthorized},
		{"empty-scopes", &ports.Actor{Type: ports.ActorTypeUser, ID: f.admin.ID.String(), Role: ports.RoleAdmin, Scopes: []string{}}, f.raw, ports.ErrForbidden},
		{"read-token", &ports.Actor{Type: ports.ActorTypeUser, ID: f.admin.ID.String(), Role: ports.RoleAdmin, Scopes: []string{"read"}}, f.raw, ports.ErrForbidden},
		{"write-token", &ports.Actor{Type: ports.ActorTypeUser, ID: f.admin.ID.String(), Role: ports.RoleAdmin, Scopes: []string{"write", "*"}}, f.raw, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, ID: f.admin.ID.String(), Role: ports.RoleAdmin}, f.raw, ports.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := f.accounts.RevokeOtherSessions(t.Context(), tc.a, tc.raw, otherSessionsCommand(), "auth")
			if out != nil || !errors.Is(err, tc.want) {
				t.Fatal("identity rejection", out, err)
			}
			if otherSessionsSnapshot(t, f) != before {
				t.Fatal("identity rejection wrote state")
			}
		})
	}
}

func TestOtherSessionsFactorsAndRollbackPG(t *testing.T) {
	for _, factor := range []string{"otp", "recovery"} {
		t.Run(factor, func(t *testing.T) {
			f := newRecoveryRegenerationFixture(t)
			other := f.otherCredentials(t)
			command := f.recoveryCommand()
			if factor == "otp" {
				f.clock.Advance(30 * time.Second)
				command.MFAProof = ports.MFAProof{OTP: fixtureTOTP(t, f.secret, f.clock.Now())}
			}
			f.exec(t, `CREATE FUNCTION reject_other_sessions_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='account.sessions.others_revoked' THEN RAISE EXCEPTION 'synthetic audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_other_sessions_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_other_sessions_audit()`)
			before := f.snapshot(t)
			out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, command, "factor")
			if out != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("audit fault", out, err)
			}
			if f.snapshot(t) != before {
				t.Fatal("rollback did not restore factor/sessions/tokens/audit/outbox/receipt")
			}
			f.exec(t, `DROP TRIGGER reject_other_sessions_audit ON audit_log`)
			revokeOthers(t, f.memberResetFixture, command, "factor")
			if _, err := f.login.AuthenticateToken(t.Context(), other); err == nil {
				t.Fatal("other session survived")
			}
			if _, err := f.login.AuthenticateToken(t.Context(), "regeneration-other-token"); err != nil {
				t.Fatal("token revoked", err)
			}
			before = f.snapshot(t)
			replay := revokeOthers(t, f.memberResetFixture, command, "factor")
			if !replay.Replayed {
				t.Fatal("factor receipt not replayed")
			}
			out, err = f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, command, "factor-reused")
			if out != nil || !errors.Is(err, ports.ErrInvalidCredentials) {
				t.Fatal("factor reused", out, err)
			}
			if f.snapshot(t) != before {
				t.Fatal("factor replay/rejection wrote state")
			}
		})
	}
}

func TestOtherSessionsConcurrentCommandPG(t *testing.T) {
	f := newRecoveryRegenerationFixture(t)
	command := f.recoveryCommand()
	start := make(chan struct{})
	type result struct {
		out *ports.AccountCommandResult
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := f.accounts.RevokeOtherSessions(t.Context(), accountActor(f.admin), f.raw, command, "concurrent")
			results <- result{out, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	replays := 0
	for r := range results {
		if r.err != nil || r.out == nil {
			t.Fatal("concurrent", r.err)
		}
		if r.out.Replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatal("expected exactly one execution", replays)
	}
	f.count(t, `SELECT count(*) FROM account_security_command WHERE action=$1`, 1, otherSessionsAction)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action=$1`, 1, otherSessionsAction)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type=$1`, 1, otherSessionsAction)
	f.count(t, `SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND used_at IS NOT NULL`, 1, f.admin.ID)
}

func waitOtherSessionsBlocked(t *testing.T, f *memberResetFixture, minimum int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= minimum {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("account operations did not block on existing transaction")
		case <-ticker.C:
		}
	}
}

func TestOtherSessionsLoginSerializationPG(t *testing.T) {
	for _, loginFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("login-first-%t", loginFirst), func(t *testing.T) {
			f := newMemberResetFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ids := idgen.New()
			release := make(chan struct{})
			entered := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var wg sync.WaitGroup
			defer func() { unblock(); cancel(); wg.Wait() }()
			accounts := f.accounts
			if !loginFirst {
				accounts = auth.NewAccountManagementService(postgres.NewAccountManagementStore(f.pool, ids), authTestSecrets{afterVerify: func() {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}}, f.clock, ids, crypto.NewSettingsCipher(make([]byte, 32)))
			}
			type loginResult struct {
				raw string
				err error
			}
			logins := make(chan loginResult, 1)
			commands := make(chan error, 1)
			startLogin := func() {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, raw, err := f.login.Login(ctx, "Admin", otherSessionsCommand().CurrentPassword)
					logins <- loginResult{raw, err}
				}()
			}
			startCommand := func() {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := accounts.RevokeOtherSessions(ctx, accountActor(f.admin), f.raw, otherSessionsCommand(), "serialized")
					commands <- err
				}()
			}
			if loginFirst {
				conn, err := f.pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Release()
				if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(160016)`); err != nil {
					t.Fatal(err)
				}
				defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(160016)`) }()
				f.exec(t, `CREATE FUNCTION pause_other_sessions_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(160016); RETURN NEW; END $$; CREATE TRIGGER pause_other_sessions_login BEFORE INSERT ON app_session FOR EACH ROW EXECUTE FUNCTION pause_other_sessions_login()`)
				startLogin()
				waitOtherSessionsBlocked(t, f, 1)
				startCommand()
				waitOtherSessionsBlocked(t, f, 2)
				if _, err = conn.Exec(ctx, `SELECT pg_advisory_unlock(160016)`); err != nil {
					t.Fatal(err)
				}
			} else {
				startCommand()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("command did not reach password verification")
				}
				startLogin()
				waitOtherSessionsBlocked(t, f, 1)
				unblock()
			}
			if err := <-commands; err != nil {
				t.Fatal("serialized command", err)
			}
			login := <-logins
			if login.err != nil {
				t.Fatal("serialized login", login.err)
			}
			_, err := f.login.AuthenticateToken(ctx, login.raw)
			if loginFirst && err == nil {
				t.Fatal("preceding login survived revocation")
			}
			if !loginFirst && err != nil {
				t.Fatal("subsequent login revoked", err)
			}
			if _, err := f.login.AuthenticateToken(ctx, f.raw); err != nil {
				t.Fatal("current session revoked", err)
			}
		})
	}
}
