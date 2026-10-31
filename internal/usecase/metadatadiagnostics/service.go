package metadatadiagnostics

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

type Service struct {
	source ports.MetadataDiagnosticSource
}

func NewService(source ports.MetadataDiagnosticSource) *Service { return &Service{source: source} }
func Admin(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.ErrForbidden
	}
	return nil
}
func (s *Service) Run(ctx context.Context, actor *ports.Actor, target ports.MetadataDiagnosticTarget) (ports.MetadataDiagnosticResult, error) {
	if err := Admin(actor); err != nil {
		return ports.MetadataDiagnosticResult{}, err
	}
	if !target.Valid() {
		return ports.MetadataDiagnosticResult{}, ports.ErrRejected
	}
	start := time.Now()
	result := ports.MetadataDiagnosticResult{Target: target, Category: "unavailable"}
	deferResult := func() ports.MetadataDiagnosticResult {
		result.ElapsedMS = max(0, time.Since(start).Milliseconds())
		return result
	}
	if ctx.Err() != nil {
		result.Category = "cancelled"
		return deferResult(), nil
	}
	if s.source == nil {
		return deferResult(), nil
	}
	session, err := s.source.OpenDiagnostic(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			result.Category = "not_configured"
		}
		if ctx.Err() != nil {
			result.Category = "cancelled"
		}
		return deferResult(), nil
	}
	if session == nil {
		return deferResult(), nil
	}
	if err = session.Validate(ctx); err != nil {
		result.Category = "configuration_changed"
		return deferResult(), nil
	}
	result = session.Probe(ctx, target)
	// A stale response is never evidence about current configuration.
	if err = session.Validate(ctx); err != nil {
		result = ports.MetadataDiagnosticResult{Target: target, Category: "configuration_changed"}
	}
	if ctx.Err() != nil {
		result = ports.MetadataDiagnosticResult{Target: target, Category: "cancelled"}
	}
	return deferResult(), nil
}
