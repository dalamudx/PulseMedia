package auth

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *AccountManagementService) verifyAccountProof(ctx context.Context, tx ports.AccountManagementTx, c ports.MFAReauthentication) error {
	if !s.secrets.VerifyPassword(c.CurrentPassword, tx.Account().PasswordHash) {
		return ports.ErrInvalidCredentials
	}
	return verifyMFA(ctx, tx, s.cipher, s.secrets, s.clock.Now(), c.MFAProof)
}

func (s *AccountManagementService) RevokeOtherSessions(ctx context.Context, a *ports.Actor, raw string, c ports.MFAReauthentication, key string) (out *ports.AccountCommandResult, err error) {
	if len(c.CurrentPassword) > 1024 || !validProof(c.MFAProof) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	// The receipt belongs to the command, including when replayed from a later session.
	fp, e := securityDigest(s.cipher, "account.sessions.revoke_others", c)
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
		if e = s.verifyAccountProof(ctx, tx, c); e != nil {
			return e
		}
		if e = tx.RevokeOtherSessions(ctx); e != nil {
			return e
		}
		keep := tx.SessionID()
		out.SessionID = &keep
		return securityFinish(ctx, tx, key, fp, "account.sessions.others_revoked", keep, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
