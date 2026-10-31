package auth

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *AccountManagementService) ReadCommand(ctx context.Context, a *ports.Actor, raw, key string) (out *ports.SecurityCommandReceipt, err error) {
	identity, e := securityIdentity(a, raw, s.secrets)
	if e != nil {
		return nil, e
	}
	err = s.repo.WithAccountManagement(ctx, identity, false, func(tx ports.AccountManagementTx) error { var e error; out, e = tx.ReadCommand(ctx, key); return e })
	return
}
