package auth

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"unicode"
	"unicode/utf8"
)

type AccountManagementService struct {
	repo    ports.AccountManagementRepository
	secrets ports.AuthenticationSecrets
	clock   ports.Clock
	ids     ports.IDGen
	cipher  ports.SecurityCipher
}

func NewAccountManagementService(repo ports.AccountManagementRepository, secrets ports.AuthenticationSecrets, clock ports.Clock, ids ports.IDGen, cipher ports.SecurityCipher) *AccountManagementService {
	return &AccountManagementService{repo, secrets, clock, ids, cipher}
}
func securityIdentity(a *ports.Actor, raw string, secrets ports.AuthenticationSecrets) (ports.SecurityIdentity, error) {
	if e := tokenSession(a); e != nil {
		return ports.SecurityIdentity{}, e
	}
	if raw == "" {
		return ports.SecurityIdentity{}, ports.ErrUnauthorized
	}
	return ports.SecurityIdentity{Actor: a, SessionHash: secrets.HashToken(raw)}, nil
}
func securityDigest(cipher ports.SecurityCipher, action string, body any) (string, error) {
	if cipher == nil {
		return "", ports.ErrUnavailable
	}
	raw, e := json.Marshal([]any{action, body})
	if e != nil {
		return "", ports.ErrInvalidAccountCommand
	}
	return cipher.CommandDigest(raw), nil
}
func securityFinish(ctx context.Context, tx ports.AccountManagementTx, key, fp, action string, target uuid.UUID, out any) error {
	raw, e := json.Marshal(out)
	if e != nil {
		return ports.ErrUnavailable
	}
	return tx.Finish(ctx, key, fp, action, target, raw)
}
func validRole(role string) bool  { return role == ports.RoleAdmin || role == ports.RoleMember }
func validPassword(p string) bool { return utf8.ValidString(p) && len(p) >= 8 && len(p) <= 1024 }
func validUsername(p string) bool {
	return utf8.ValidString(p) && len(p) >= 3 && len(p) <= 80 && strings.TrimSpace(p) == p && strings.IndexFunc(p, unicode.IsControl) < 0
}
func (s *AccountManagementService) List(ctx context.Context, a *ports.Actor, raw string) (out []ports.User, err error) {
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	err = s.repo.WithAccountManagement(ctx, identity, true, func(tx ports.AccountManagementTx) error { var e error; out, e = tx.ListAccounts(ctx); return e })
	return
}
func (s *AccountManagementService) Create(ctx context.Context, a *ports.Actor, raw string, c ports.AccountCreate, key string) (out *ports.AccountCommandResult, err error) {
	if !validUsername(c.Username) || !validPassword(c.Password) || !validRole(c.Role) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.create", c)
	if e != nil {
		return nil, e
	}
	hash, e := s.secrets.HashPassword(c.Password)
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
		u, e := tx.CreateAccount(ctx, s.ids.NewUUID(), c.Username, hash, c.Role)
		if e != nil {
			return e
		}
		out.User = &u
		return securityFinish(ctx, tx, key, fp, "account.created", u.ID, out)
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *AccountManagementService) Update(ctx context.Context, a *ports.Actor, raw string, id uuid.UUID, c ports.AccountUpdate, key string) (out *ports.AccountCommandResult, err error) {
	if id == uuid.Nil || !validRole(c.Role) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.update", []any{id, c})
	if e != nil {
		return nil, e
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
		u, e := tx.UpdateAccount(ctx, id, c)
		if e != nil {
			return e
		}
		out.User = &u
		return securityFinish(ctx, tx, key, fp, "account.updated", id, out)
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *AccountManagementService) Sessions(ctx context.Context, a *ports.Actor, raw string) (out []ports.ManagedSession, err error) {
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error { var e error; out, e = tx.ListSessions(ctx); return e })
	return
}
func (s *AccountManagementService) RevokeSession(ctx context.Context, a *ports.Actor, raw string, id uuid.UUID, key string) (out *ports.AccountCommandResult, err error) {
	if id == uuid.Nil {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.session.revoke", id)
	if e != nil {
		return nil, e
	}
	out = &ports.AccountCommandResult{SessionID: &id}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error {
		found, e := tx.Receipt(ctx, key, fp, out)
		if e != nil {
			return e
		}
		if found {
			out.Replayed = true
			return nil
		}
		if e = tx.RevokeSession(ctx, id); e != nil {
			return e
		}
		return securityFinish(ctx, tx, key, fp, "account.session.revoked", id, out)
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *AccountManagementService) ChangePassword(ctx context.Context, a *ports.Actor, raw string, c ports.PasswordChange, key string) (out *ports.AccountCommandResult, err error) {
	if !validPassword(c.NewPassword) || len(c.CurrentPassword) > 1024 || !validProof(c.MFAProof) {
		return nil, ports.ErrInvalidAccountCommand
	}
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	fp, e := securityDigest(s.cipher, "account.password.change", c)
	if e != nil {
		return nil, e
	}
	hash, e := s.secrets.HashPassword(c.NewPassword)
	if e != nil {
		return nil, ports.ErrUnavailable
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
		state, e := tx.LoadMFA(ctx)
		if e != nil {
			return e
		}
		if !state.Enabled {
			if e = tx.DeleteMFA(ctx); e != nil {
				return e
			}
		}
		if e = tx.ChangePassword(ctx, hash); e != nil {
			return e
		}
		if e = tx.RevokeOtherCredentials(ctx); e != nil {
			return e
		}
		return securityFinish(ctx, tx, key, fp, "account.password.changed", tx.Account().User.ID, out)
	})
	if err != nil {
		return nil, err
	}
	return
}
