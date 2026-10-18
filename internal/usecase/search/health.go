package search

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

func (s *Service) ListConfiguredIndexers(ctx context.Context) ([]ports.IndexerHealthInfo, error) {
	if s.healthStore == nil {
		return nil, ports.ErrUnavailable
	}
	return s.healthStore.ListHealth(ctx)
}

type HealthService struct {
	repo  ports.IndexerHealthRepository
	probe ports.IndexerProbe
}

func NewHealthService(repo ports.IndexerHealthRepository, probe ports.IndexerProbe) *HealthService {
	return &HealthService{repo, probe}
}
func (h *HealthService) QueueDue(ctx context.Context, limit int32) (int, error) {
	return h.repo.QueueHealth(ctx, limit)
}
func (h *HealthService) ExecuteJob(ctx context.Context, j ports.IndexerHealthJob) error {
	if j.ID == uuid.Nil || j.IndexerID == uuid.Nil || j.Owner == "" || j.ConfigVersion < 1 {
		return ports.ErrBadResponse
	}
	stale, e := h.repo.CheckHealth(ctx, j)
	if e != nil {
		return e
	}
	if stale {
		return h.repo.CompleteHealth(ctx, j, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := h.probe.ProbeIndexer(bounded, j)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r == nil || r.QueryID == uuid.Nil || r.FromShared || r.State == "running" {
		if e != nil {
			return e
		}
		return ports.ErrUnavailable
	}
	return h.repo.CompleteHealth(ctx, j, &r.QueryID)
}
func (s *Service) ProbeIndexer(ctx context.Context, j ports.IndexerHealthJob) (*ports.IndexerQueryResult, error) {
	return s.execute(ctx, j.IndexerID, "", 0, uuid.Nil, j.ConfigVersion, &j, true, nil)
}
