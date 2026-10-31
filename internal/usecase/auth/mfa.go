package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/url"
	"strings"
	"time"
)

func validProof(p ports.MFAProof) bool {
	return len(p.OTP) <= 6 && len(p.RecoveryCode) <= 64 && (p.OTP == "" || p.RecoveryCode == "")
}

// TOTP follows RFC 6238: SHA-1, six digits, 30 seconds and one step of clock skew.
func totpCode(secret []byte, counter int64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(counter))
	h := hmac.New(sha1.New, secret)
	_, _ = h.Write(b[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	v := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}
func totpCounter(secret []byte, code string, now time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	for step := int64(1); step >= -1; step-- {
		counter := now.Unix()/30 + step
		if counter >= 0 && counter > last && subtle.ConstantTimeCompare([]byte(totpCode(secret, counter)), []byte(code)) == 1 {
			return counter, true
		}
	}
	return 0, false
}
func verifyMFA(ctx context.Context, tx ports.MFATransaction, cipher ports.SettingsCipher, secrets ports.AuthenticationSecrets, now time.Time, proof ports.MFAProof) error {
	state, e := tx.LoadMFA(ctx)
	if e != nil {
		return e
	}
	if !state.Enabled {
		return nil
	}
	if !validProof(proof) {
		return ports.ErrInvalidCredentials
	}
	if proof.RecoveryCode != "" {
		ok, e := tx.ConsumeRecoveryCode(ctx, secrets.HashToken("pulse.mfa.recovery.v1:"+strings.ToUpper(strings.TrimSpace(proof.RecoveryCode))))
		if e != nil {
			return e
		}
		if !ok {
			return ports.ErrInvalidCredentials
		}
		return nil
	}
	if cipher == nil {
		return ports.ErrUnavailable
	}
	secret, e := cipher.Decrypt(state.Secret)
	if e != nil || len(secret) != 20 {
		return ports.ErrUnavailable
	}
	defer clear(secret)
	counter, ok := totpCounter(secret, proof.OTP, now, state.LastCounter)
	if !ok {
		return ports.ErrInvalidCredentials
	}
	state.LastCounter = counter
	return tx.StoreMFA(ctx, state)
}

type MFAService struct {
	repo    ports.AccountManagementRepository
	secrets ports.AuthenticationSecrets
	cipher  ports.SecurityCipher
	clock   ports.Clock
	ids     ports.IDGen
}

func NewMFAService(repo ports.AccountManagementRepository, secrets ports.AuthenticationSecrets, cipher ports.SecurityCipher, clock ports.Clock, ids ports.IDGen) *MFAService {
	return &MFAService{repo, secrets, cipher, clock, ids}
}
func (s *MFAService) Status(ctx context.Context, a *ports.Actor, raw string) (out ports.MFAStatus, err error) {
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return out, e
	}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error { var e error; out, e = tx.MFAStatus(ctx); return e })
	return
}
func (s *MFAService) Begin(ctx context.Context, a *ports.Actor, raw, password, key string) (out *ports.MFASetupResult, err error) {
	if len(password) > 1024 {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.mfa.setup", password)
	if e != nil {
		return nil, e
	}
	out = &ports.MFASetupResult{}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error {
		found, e := tx.Receipt(ctx, key, fp, out)
		if e != nil {
			return e
		}
		if found {
			out.Replayed = true
			return nil
		}
		if !s.secrets.VerifyPassword(password, tx.Account().PasswordHash) {
			return ports.ErrInvalidCredentials
		}
		state, e := tx.LoadMFA(ctx)
		if e != nil {
			return e
		}
		if state.Enabled {
			return ports.ErrAccountCommandConflict
		}
		secret := make([]byte, 20)
		if _, e = rand.Read(secret); e != nil {
			return ports.ErrUnavailable
		}
		defer clear(secret)
		encrypted, e := s.cipher.Encrypt(secret)
		if e != nil {
			return ports.ErrUnavailable
		}
		state = ports.MFAState{Secret: encrypted, LastCounter: -1, ChallengeID: s.ids.NewUUID(), ExpiresAt: s.clock.Now().Add(10 * time.Minute)}
		if e = tx.StoreMFA(ctx, state); e != nil {
			return e
		}
		out.ChallengeID = state.ChallengeID
		out.ExpiresAt = state.ExpiresAt
		if e = securityFinish(ctx, tx, key, fp, "account.mfa.setup", tx.Account().User.ID, out); e != nil {
			return e
		}
		out.Secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
		q := url.Values{"secret": {out.Secret}, "issuer": {"PulseMedia"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
		out.OTPAuthURL = "otpauth://totp/" + url.PathEscape("PulseMedia:"+tx.Account().User.Username) + "?" + q.Encode()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *MFAService) Confirm(ctx context.Context, a *ports.Actor, raw string, challenge uuid.UUID, otp, key string) (out *ports.MFAConfirmResult, err error) {
	if challenge == uuid.Nil || len(otp) != 6 {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.mfa.confirm", []any{challenge, otp})
	if e != nil {
		return nil, e
	}
	out = &ports.MFAConfirmResult{}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error {
		found, e := tx.Receipt(ctx, key, fp, out)
		if e != nil {
			return e
		}
		if found {
			out.Replayed = true
			return nil
		}
		state, e := tx.LoadMFA(ctx)
		if e != nil {
			return e
		}
		if state.Enabled || state.ChallengeID != challenge || !state.ExpiresAt.After(s.clock.Now()) {
			return ports.ErrAccountCommandConflict
		}
		secret, e := s.cipher.Decrypt(state.Secret)
		if e != nil || len(secret) != 20 {
			return ports.ErrUnavailable
		}
		defer clear(secret)
		counter, ok := totpCounter(secret, otp, s.clock.Now(), state.LastCounter)
		if !ok {
			return ports.ErrInvalidCredentials
		}
		state.Enabled = true
		state.LastCounter = counter
		if e = tx.StoreMFA(ctx, state); e != nil {
			return e
		}
		codes := make([]string, 10)
		hashes := make([][]byte, 10)
		for i := range codes {
			b := make([]byte, 16)
			if _, e = rand.Read(b); e != nil {
				return ports.ErrUnavailable
			}
			codes[i] = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
			clear(b)
			hashes[i] = s.secrets.HashToken("pulse.mfa.recovery.v1:" + codes[i])
		}
		if e = tx.ReplaceRecoveryCodes(ctx, hashes); e != nil {
			return e
		}
		if e = tx.RevokeOtherCredentials(ctx); e != nil {
			return e
		}
		out.Enabled = true
		if e = securityFinish(ctx, tx, key, fp, "account.mfa.enabled", tx.Account().User.ID, out); e != nil {
			return e
		}
		out.RecoveryCodes = codes
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *MFAService) Disable(ctx context.Context, a *ports.Actor, raw string, c ports.MFAReauthentication, key string) (out *ports.AccountCommandResult, err error) {
	if len(c.CurrentPassword) > 1024 || !validProof(c.MFAProof) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.mfa.disable", c)
	if e != nil {
		return nil, e
	}
	out = &ports.AccountCommandResult{}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error {
		found, e := tx.Receipt(ctx, key, fp, out)
		if e != nil {
			return e
		}
		if found {
			out.Replayed = true
			return nil
		}
		if !s.secrets.VerifyPassword(c.CurrentPassword, tx.Account().PasswordHash) {
			return ports.ErrInvalidCredentials
		}
		if e = verifyMFA(ctx, tx, s.cipher, s.secrets, s.clock.Now(), c.MFAProof); e != nil {
			return e
		}
		if e = tx.DeleteMFA(ctx); e != nil {
			return e
		}
		if e = tx.RevokeOtherCredentials(ctx); e != nil {
			return e
		}
		return securityFinish(ctx, tx, key, fp, "account.mfa.disabled", tx.Account().User.ID, out)
	})
	if err != nil {
		return nil, err
	}
	return
}
