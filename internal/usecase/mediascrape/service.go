// Package mediascrape queues explicit library-wide sidecar checks through the existing protected worker.
package mediascrape

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type Service struct {
	repo  ports.MediaScrapeBatchRepository
	idgen ports.IDGen
}

func NewService(repo ports.MediaScrapeBatchRepository, idgen ports.IDGen) *Service {
	return &Service{repo: repo, idgen: idgen}
}
func (s *Service) Preview(ctx context.Context, actor *ports.Actor) (ports.MediaScrapePreview, error) {
	var p ports.MediaScrapePreview
	if err := authorizeScrape(actor); err != nil {
		return p, err
	}
	if s == nil || s.repo == nil {
		return p, ports.ErrUnavailable
	}
	rows, err := s.repo.ListMediaScrapeCandidates(ctx, actor)
	if err != nil {
		return p, err
	}
	p.Total = len(rows)
	for _, r := range rows {
		switch r.Status {
		case ports.MediaScrapeMissing:
			p.Missing++
		case ports.MediaScrapeQueued:
			p.Queued++
		case ports.MediaScrapeRunning:
			p.Running++
		case ports.MediaScrapeComplete:
			p.Complete++
		case ports.MediaScrapeReview:
			p.Review++
		}
	}
	return p, nil
}
func (s *Service) Start(ctx context.Context, actor *ports.Actor, key string) (ports.MediaScrapeProgress, bool, error) {
	if err := authorizeScrape(actor); err != nil {
		return ports.MediaScrapeProgress{}, false, err
	}
	if !validCommandKey(key) {
		return ports.MediaScrapeProgress{}, false, ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.idgen == nil {
		return ports.MediaScrapeProgress{}, false, ports.ErrUnavailable
	}
	return s.repo.StartMediaScrape(ctx, actor, key, s.idgen.NewUUID)
}
func (s *Service) Status(ctx context.Context, actor *ports.Actor, key string) (*ports.MediaScrapeProgress, error) {
	if err := authorizeScrape(actor); err != nil {
		return nil, err
	}
	if key != "" && !validCommandKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ReadMediaScrapeBatch(ctx, actor, key)
}
func authorizeScrape(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || actor.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func validCommandKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if !strings.ContainsRune("-_.:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", r) {
			return false
		}
	}
	return true
}
