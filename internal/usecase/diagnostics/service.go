package diagnostics

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Service struct{ reader ports.RuntimeMetricsReader }

func NewService(reader ports.RuntimeMetricsReader) *Service { return &Service{reader: reader} }
func (s *Service) Read(ctx context.Context, actor *ports.Actor) (ports.RuntimeMetrics, error) {
	if actor == nil {
		return ports.RuntimeMetrics{}, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || !actor.IsAdmin() {
		return ports.RuntimeMetrics{}, ports.ErrForbidden
	}
	if actor.Scopes != nil {
		read := false
		for _, scope := range actor.Scopes {
			if scope == "read" {
				read = true
			}
		}
		if !read {
			return ports.RuntimeMetrics{}, ports.ErrForbidden
		}
	}
	if s.reader == nil {
		return ports.RuntimeMetrics{}, ports.ErrUnavailable
	}
	return s.reader.ReadRuntimeMetrics(ctx)
}
