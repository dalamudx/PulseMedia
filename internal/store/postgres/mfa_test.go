package postgres_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureTOTP(t *testing.T, encoded string, now time.Time) string {
	t.Helper()
	secret, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded)
	if e != nil {
		t.Fatal(e)
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(now.Unix()/30))
	h := hmac.New(sha1.New, secret)
	_, _ = h.Write(b[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}
func TestMFALifecycleReplayRecoveryPG(t *testing.T) {
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
	status, e := svc.Status(ctx, a, raw)
	if e != nil || status.Enabled {
		t.Fatal("MFA not off by default", e)
	}
	if _, e = svc.Begin(ctx, a, raw, "wrong", "setup"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("setup missing reauthentication", e)
	}
	setup, e := svc.Begin(ctx, a, raw, "Password123!", "setup")
	if e != nil || setup.Secret == "" || !strings.HasPrefix(setup.OTPAuthURL, "otpauth://totp/") {
		t.Fatal("setup", e)
	}
	replay, e := svc.Begin(ctx, a, raw, "Password123!", "setup")
	if e != nil || !replay.Replayed || replay.Secret != "" || replay.OTPAuthURL != "" {
		t.Fatal("setup replay leaked secret", e)
	}
	var encrypted []byte
	if e = pool.QueryRow(ctx, "SELECT secret_ciphertext FROM account_totp WHERE user_id=$1", user.ID).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	plain, e := cipher.Decrypt(encrypted)
	if e != nil || base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(plain) != setup.Secret {
		t.Fatal("encryption mismatch", e)
	}
	_, old, e := login.Login(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal("unconfirmed setup enforced MFA", e)
	}
	code := fixtureTOTP(t, setup.Secret, c.Now())
	confirmed, e := svc.Confirm(ctx, a, raw, setup.ChallengeID, code, "confirm")
	if e != nil || !confirmed.Enabled || len(confirmed.RecoveryCodes) != 10 {
		t.Fatal("confirm", e)
	}
	replayConfirm, e := svc.Confirm(ctx, a, raw, setup.ChallengeID, code, "confirm")
	if e != nil || !replayConfirm.Replayed || len(replayConfirm.RecoveryCodes) != 0 {
		t.Fatal("replayed recovery codes exposed", e)
	}
	if _, e = login.AuthenticateToken(ctx, old); e == nil {
		t.Fatal("pre MFA session survived")
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("MFA omitted", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: code}); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("enrollment counter replay", e)
	}
	c.Advance(30 * time.Second)
	code = fixtureTOTP(t, setup.Secret, c.Now())
	start := make(chan struct{})
	done := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, e := login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: code})
			done <- e
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	successes, replays := 0, 0
	for e := range done {
		if e == nil {
			successes++
		} else if errors.Is(e, ports.ErrInvalidCredentials) {
			replays++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("OTP concurrent consume %d/%d", successes, replays)
	}
	c.Advance(30 * time.Second)
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]}); e != nil {
		t.Fatal("backup code login", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]}); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("recovery reused", e)
	}
	status, e = svc.Status(ctx, a, raw)
	if e != nil || status.RecoveryCodesRemaining != 9 {
		t.Fatal("remaining recovery count", e)
	}
	receipt, e := accounts.ReadCommand(ctx, a, raw, "confirm")
	if e != nil || strings.Contains(string(receipt.Result), "recovery_codes") || strings.Contains(string(receipt.Result), confirmed.RecoveryCodes[0]) {
		t.Fatal("receipt leaks codes", e)
	}
	var leaked bool
	if e = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM audit_log WHERE payload::text LIKE $1 OR payload::text LIKE $2)", "%"+setup.Secret+"%", "%"+confirmed.RecoveryCodes[0]+"%").Scan(&leaked); e != nil || leaked {
		t.Fatal("audit leaks secret", e)
	}
	if _, e = svc.Disable(ctx, a, raw, ports.MFAReauthentication{CurrentPassword: "wrong", MFAProof: ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[1]}}, "disable"); !errors.Is(e, ports.ErrInvalidCredentials) {
		t.Fatal("disable bypassed password", e)
	}
	if _, e = svc.Disable(ctx, a, raw, ports.MFAReauthentication{CurrentPassword: "Password123!", MFAProof: ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[1]}}, "disable"); e != nil {
		t.Fatal(e)
	}
	status, e = svc.Status(ctx, a, raw)
	if e != nil || status.Enabled || status.RecoveryCodesRemaining != 0 {
		t.Fatal("disable retained state", e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!"); e != nil {
		t.Fatal("disabled MFA login", e)
	}
}
func TestMFARecoveryConcurrentAndLoginRollbackPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	login.SetMFACipher(cipher)
	svc := auth.NewMFAService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, cipher, c, ids)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	a := accountActor(user)
	setup, e := svc.Begin(ctx, a, raw, "Password123!", "setup")
	if e != nil {
		t.Fatal(e)
	}
	confirmed, e := svc.Confirm(ctx, a, raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, c.Now()), "confirm")
	if e != nil {
		t.Fatal(e)
	}
	_, e = pool.Exec(ctx, `CREATE FUNCTION reject_mfa_session() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic session fault'; END $$; CREATE TRIGGER reject_mfa_session BEFORE INSERT ON app_session FOR EACH ROW EXECUTE FUNCTION reject_mfa_session()`)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]}); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("session fault", e)
	}
	status, e := svc.Status(ctx, a, raw)
	if e != nil || status.RecoveryCodesRemaining != 10 {
		t.Fatal("session failure consumed recovery", e)
	}
	c.Advance(30 * time.Second)
	otp := fixtureTOTP(t, setup.Secret, c.Now())
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: otp}); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_mfa_session ON app_session"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: otp}); e != nil {
		t.Fatal("failed session consumed counter", e)
	}
	done := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, e := login.Login(ctx, "Admin", "Password123!", ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]})
			done <- e
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	successes, replays := 0, 0
	for e := range done {
		if e == nil {
			successes++
		} else if errors.Is(e, ports.ErrInvalidCredentials) {
			replays++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatal("concurrent recovery reused", successes, replays)
	}
}
func TestMFAChallengeExpiryAndKeyFailurePG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	ids := idgen.New()
	key := make([]byte, 32)
	cipher := crypto.NewSettingsCipher(key)
	login := auth.NewService(repo, crypto.AuthenticationSecrets{}, c, ids)
	svc := auth.NewMFAService(postgres.NewAccountManagementStore(pool, ids), crypto.AuthenticationSecrets{}, cipher, c, ids)
	user, raw, e := login.SetupAdmin(ctx, "Admin", "Password123!")
	if e != nil {
		t.Fatal(e)
	}
	a := accountActor(user)
	setup, e := svc.Begin(ctx, a, raw, "Password123!", "setup")
	if e != nil {
		t.Fatal(e)
	}
	c.Advance(11 * time.Minute)
	if _, e = svc.Confirm(ctx, a, raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, c.Now()), "expired"); !errors.Is(e, ports.ErrAccountCommandConflict) {
		t.Fatal("expired challenge accepted", e)
	}
	next, e := svc.Begin(ctx, a, raw, "Password123!", "setup-next")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Confirm(ctx, a, raw, setup.ChallengeID, fixtureTOTP(t, setup.Secret, c.Now()), "stale"); !errors.Is(e, ports.ErrAccountCommandConflict) {
		t.Fatal("replaced challenge accepted", e)
	}
	if _, e = svc.Confirm(ctx, a, raw, next.ChallengeID, fixtureTOTP(t, next.Secret, c.Now()), "confirm"); e != nil {
		t.Fatal(e)
	}
	c.Advance(30 * time.Second)
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: fixtureTOTP(t, next.Secret, c.Now())}); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("missing cipher failed open", e)
	}
	wrongKey := make([]byte, 32)
	wrongKey[0] = 1
	login.SetMFACipher(crypto.NewSettingsCipher(wrongKey))
	if _, _, e = login.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: fixtureTOTP(t, next.Secret, c.Now())}); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("wrong key failed open", e)
	}
	// Recreating the service with the backed-up main key preserves encrypted identity and replay state.
	restored := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, c, ids)
	restored.SetMFACipher(crypto.NewSettingsCipher(key))
	if _, _, e = restored.Login(ctx, "Admin", "Password123!", ports.MFAProof{OTP: fixtureTOTP(t, next.Secret, c.Now())}); e != nil {
		t.Fatal("key restore failed", e)
	}
}
