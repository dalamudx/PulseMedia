package metadatadiagnostics

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

type countingSource struct{ calls int }

func (s *countingSource) OpenDiagnostic(context.Context) (ports.MetadataDiagnosticSession, error) {
	s.calls++
	return nil, errors.New("https://private.invalid?api_key=secret")
}
func TestMetadataDiagnosticAuthorizationAndTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		actor  *ports.Actor
		target ports.MetadataDiagnosticTarget
		want   error
	}{
		{"anonymous", nil, ports.DiagnosticTMDBAPI, ports.ErrUnauthorized},
		{"member", &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleMember}, ports.DiagnosticTMDBAPI, ports.ErrForbidden},
		{"token", &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleAdmin, Scopes: []string{}}, ports.DiagnosticTMDBAPI, ports.ErrForbidden},
		{"invalid target", &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleAdmin}, "https://private.invalid", ports.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &countingSource{}
			_, err := NewService(source).Run(context.Background(), tc.actor, tc.target)
			if !errors.Is(err, tc.want) || source.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, source.calls)
			}
		})
	}
	source := &countingSource{}
	result, err := NewService(source).Run(context.Background(), &ports.Actor{Type: ports.ActorTypeUser, ID: "11111111-1111-4111-8111-111111111111", Role: ports.RoleAdmin}, ports.DiagnosticTMDBAPI)
	if err != nil || result.Category != "unavailable" || result.HostResponded || source.calls != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
