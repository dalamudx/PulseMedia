package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestUntrackedOrganizationConflictErrorContract(t *testing.T) {
	for _, item := range []struct {
		name          string
		failure, want error
	}{
		{"own", ports.ErrUntrackedOrganizationStale, ports.ErrUntrackedOrganizationStale},
		{"wrapped_own", fmt.Errorf("conflict: %w", ports.ErrUntrackedOrganizationStale), ports.ErrUntrackedOrganizationStale},
		{"review", ports.ErrUntrackedReviewStale, ports.ErrUntrackedOrganizationStale},
		{"discovery", ports.ErrUntrackedDiscoveryStale, ports.ErrUntrackedOrganizationStale},
		{"version", ports.ErrEndpointVersionConflict, ports.ErrEndpointVersionConflict},
		{"forbidden", ports.ErrForbidden, ports.ErrForbidden},
		{"unavailable", ports.ErrUnavailable, ports.ErrUnavailable},
		{"cancel", context.Canceled, context.Canceled},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"nil", nil, nil},
	} {
		t.Run(item.name, func(t *testing.T) {
			result := organizationError(t.Context(), item.failure)
			if item.want == nil {
				if result != nil {
					t.Fatalf("unexpected result: %v", result)
				}
				return
			}
			if !errors.Is(result, item.want) {
				t.Fatalf("error contract: got %v, want %v", result, item.want)
			}
		})
	}
}
