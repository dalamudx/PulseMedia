package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var ErrPathMismatch = ports.ErrDownloadPathMismatch
var ErrReconcilePending = ports.ErrDownloadReconcilePending
var ErrClientInUse = ports.ErrDownloadClientInUse

type ClientFactory = ports.DownloadClientFactory
type Manager struct {
	repo ports.DownloadExecutionRepository
}

func NewManager(repo ports.DownloadExecutionRepository) *Manager { return &Manager{repo: repo} }
func (m *Manager) SetSubmissionRepository(r ports.DownloadSubmissionRepository) {
	m.repo.SetSubmissionRepository(r)
}
func (m *Manager) RegisterClient(id uuid.UUID, c ports.DownloadClient) { m.repo.RegisterClient(id, c) }
func (m *Manager) SetClientFactory(f ClientFactory)                    { m.repo.SetClientFactory(f) }
func (m *Manager) DeleteClient(ctx context.Context, id uuid.UUID) error {
	return m.repo.DeleteClient(ctx, id)
}
func (m *Manager) TestClient(ctx context.Context, id uuid.UUID) (bool, string, error) {
	return m.repo.TestClient(ctx, id)
}
func (m *Manager) SubmitDownload(ctx context.Context, op, client, candidate uuid.UUID, path string, authority ...ports.JobLease) (*ports.DownloadExecutionTask, error) {
	return m.repo.SubmitDownload(ctx, op, client, candidate, path, authority...)
}
func (m *Manager) ReconcileTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return m.repo.ReconcileTask(ctx, op)
}
func (m *Manager) ObserveTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return m.repo.ObserveTask(ctx, op)
}
func TranslateRemotePath(mappings map[string]string, path string) string {
	return naming.TranslateRemotePath(mappings, path)
}
