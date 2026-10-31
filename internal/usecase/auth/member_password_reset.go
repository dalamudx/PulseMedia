package auth

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const memberPasswordResetAction = "account.member_password.reset"

func (s *AccountManagementService) ResetMemberPassword(ctx context.Context, a *ports.Actor, raw string, target uuid.UUID, c ports.PasswordChange, key string) (out *ports.AccountCommandResult, err error) {
	if target == uuid.Nil || !validPassword(c.NewPassword) || len(c.CurrentPassword) > 1024 || !validProof(c.MFAProof) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	if a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	fp, e := securityDigest(s.cipher, memberPasswordResetAction, []any{target, c})
	if e != nil {
		return nil, e
	}
	hash, e := s.secrets.HashPassword(c.NewPassword)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out = &ports.AccountCommandResult{}
	err = s.repo.WithAccountManagement(ctx, identity, true, func(tx ports.AccountManagementTx) error {
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
		u, e := tx.ResetMemberPassword(ctx, target, hash)
		if e != nil {
			return e
		}
		out.User = &u
		return securityFinish(ctx, tx, key, fp, memberPasswordResetAction, target, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
