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

var _ ports.DownloadTaskExecution = (*Manager)(nil)

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
	return SubmitDownload(ctx, m.repo, op, client, candidate, path, authority...)
}

// SubmitDownload owns external sending and completion, retaining the physical
// budget through both. An uncertain response never authorizes another Add.
func SubmitDownload(ctx context.Context, repo ports.DownloadSubmissionPreparer, op, client, candidate uuid.UUID, path string, authority ...ports.JobLease) (*ports.DownloadExecutionTask, error) {
	prepared, err := repo.PrepareDownload(ctx, op, client, candidate, path, authority...)
	if prepared == nil {
		if err == nil {
			err = ports.ErrUnavailable
		}
		return nil, err
	}
	if prepared.Release != nil {
		defer prepared.Release()
	}
	if err != nil {
		return prepared.Task, err
	}
	if prepared.Client == nil {
		if prepared.Task == nil || prepared.Complete != nil || prepared.Release != nil {
			return prepared.Task, ports.ErrUnavailable
		}
		return prepared.Task, nil
	}
	if prepared.Complete == nil || prepared.Release == nil || prepared.Task == nil {
		return prepared.Task, ports.ErrUnavailable
	}
	addErr := ctx.Err()
	if addErr == nil {
		addErr = prepared.Client.AddTorrent(ctx, prepared.Params)
	}
	return prepared.Complete(ctx, addErr)
}
func (m *Manager) ReconcileTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return ReconcileTask(ctx, m.repo, op)
}
func (m *Manager) ObserveTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return ObserveTask(ctx, m.repo, op)
}
func TranslateRemotePath(mappings map[string]string, path string) string {
	return naming.TranslateRemotePath(mappings, path)
}
