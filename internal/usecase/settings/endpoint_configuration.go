package settings

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// EndpointConfigurationService coordinates configuration and read-only connectivity probes.
type EndpointConfigurationService struct {
	repo     ports.EndpointConfigurationRepository
	download ports.DownloadEndpointTester
	media    ports.MediaEndpointTester
}

func NewEndpointConfigurationService(repo ports.EndpointConfigurationRepository, download ports.DownloadEndpointTester, media ports.MediaEndpointTester) *EndpointConfigurationService {
	return &EndpointConfigurationService{repo: repo, download: download, media: media}
}

func (s *EndpointConfigurationService) authorize(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, write bool) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || (actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	if write && (actor.Role != ports.RoleAdmin || actor.Scopes != nil) {
		return ports.ErrForbidden
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil {
		return ports.ErrInvalidEndpointConfiguration
	}
	if family != ports.DownloadClientEndpoint && family != ports.MediaServerEndpoint {
		return ports.ErrInvalidEndpointConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.repo == nil {
		return ports.ErrUnavailable
	}
	return nil
}

func (s *EndpointConfigurationService) List(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily) ([]ports.EndpointConfiguration, error) {
	if err := s.authorize(ctx, actor, family, false); err != nil {
		return nil, err
	}
	return s.repo.ListEndpointConfigurations(ctx, actor, family)
}

func (s *EndpointConfigurationService) Create(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, input ports.EndpointConfigurationInput) (*ports.EndpointConfiguration, error) {
	if err := s.authorize(ctx, actor, family, true); err != nil {
		return nil, err
	}
	if !input.ExpectedUpdatedAt.IsZero() {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	return s.repo.CreateEndpointConfiguration(ctx, actor, family, input)
}

func (s *EndpointConfigurationService) Update(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID, input ports.EndpointConfigurationInput) (*ports.EndpointConfiguration, error) {
	if err := s.authorize(ctx, actor, family, true); err != nil {
		return nil, err
	}
	if id == uuid.Nil || input.ExpectedUpdatedAt.IsZero() || len(input.Mapping) > 0 {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	return s.repo.UpdateEndpointConfiguration(ctx, actor, family, id, input)
}

func (s *EndpointConfigurationService) Delete(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID) error {
	if err := s.authorize(ctx, actor, family, true); err != nil {
		return err
	}
	if id == uuid.Nil {
		return ports.ErrInvalidEndpointConfiguration
	}
	return s.repo.DeleteEndpointConfiguration(ctx, actor, family, id)
}

func (s *EndpointConfigurationService) Probe(ctx context.Context, actor *ports.Actor, family ports.EndpointFamily, id uuid.UUID) (*ports.EndpointProbeResult, error) {
	if err := s.authorize(ctx, actor, family, true); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	authorized, err := s.repo.AuthorizeEndpointProbe(ctx, actor, family, id)
	if err != nil {
		return nil, err
	}
	if authorized == nil || authorized.Family != family || authorized.ID != id || authorized.UpdatedAt.IsZero() || authorized.ConfigStamp == "" {
		return nil, ports.ErrUnavailable
	}
	// Keep the exact authorized observation for the post-I/O version check.
	scope := *authorized
	var test func(context.Context, uuid.UUID) (bool, string, error)
	switch family {
	case ports.DownloadClientEndpoint:
		if s.download != nil {
			test = s.download.TestClient
		}
	case ports.MediaServerEndpoint:
		if s.media != nil {
			test = s.media.TestServer
		}
	}
	if test == nil {
		return nil, ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	// Call synchronously so cancellation cannot outlive the physical probe.
	alive, _, probeErr := test(probeCtx, id)
	budgetErr := probeCtx.Err()
	cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if probeErr == nil && budgetErr != nil {
		return nil, ports.ErrUnavailable
	}
	switch {
	case probeErr == nil:
	case errors.Is(probeErr, ports.ErrNotFound):
		return nil, ports.ErrNotFound
	case errors.Is(probeErr, ports.ErrUnavailable):
		return nil, ports.ErrUnavailable
	case errors.Is(probeErr, ports.ErrEndpointProbeFailed):
		alive = false
	default:
		return nil, ports.ErrUnavailable
	}
	if err := s.repo.CompleteEndpointProbe(ctx, actor, scope, alive); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	message := "endpoint unavailable"
	if alive {
		message = "ok"
	}
	return &ports.EndpointProbeResult{Alive: alive, Message: message}, nil
}
