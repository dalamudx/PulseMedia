package command

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func TestAPICredentialRejectedBeforeReceiptOrExecutorAccess(t *testing.T) {
	for _, scopes := range [][]string{{}, {"read"}, {"write"}, {"read", "write"}, {"admin", "system", "*"}} {
		a := &ports.Actor{Type: ports.ActorTypeUser, ID: "owned-user", Role: ports.RoleAdmin, Scopes: scopes}
		if !errors.Is(AuthorizeSession(a, ports.RoleAdmin), ports.ErrForbidden) {
			t.Fatal("API credential given command authority")
		}
	}
}
