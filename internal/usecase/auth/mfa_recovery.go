package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// RegenerateRecoveryCodes replaces all recovery codes without changing the TOTP binding.
func (s *MFAService) RegenerateRecoveryCodes(ctx context.Context, a *ports.Actor, raw string, c ports.MFAReauthentication, key string) (out *ports.MFAConfirmResult, err error) {
	if len(c.CurrentPassword) > 1024 || !validProof(c.MFAProof) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.mfa.recovery.regenerate", c)
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
		if !state.Enabled {
			return ports.ErrAccountCommandConflict
		}
		if !s.secrets.VerifyPassword(c.CurrentPassword, tx.Account().PasswordHash) {
			return ports.ErrInvalidCredentials
		}
		if e = verifyMFA(ctx, tx, s.cipher, s.secrets, s.clock.Now(), c.MFAProof); e != nil {
			return e
		}
		codes := make([]string, 10)
		hashes := make([][]byte, len(codes))
		for i := range codes {
			var b [16]byte
			if _, e = rand.Read(b[:]); e != nil {
				return ports.ErrUnavailable
			}
			codes[i] = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
			clear(b[:])
			hashes[i] = s.secrets.HashToken("pulse.mfa.recovery.v1:" + codes[i])
		}
		if e = tx.ReplaceRecoveryCodes(ctx, hashes); e != nil {
			return e
		}
		if e = tx.RevokeOtherCredentials(ctx); e != nil {
			return e
		}
		out.Enabled = true
		if e = securityFinish(ctx, tx, key, fp, "account.mfa.recovery.regenerated", tx.Account().User.ID, out); e != nil {
			return e
		}
		out.RecoveryCodes = codes
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
