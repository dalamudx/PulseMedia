package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ execution *Manager }

var _ ports.DownloadExecutionRepository = (*Repository)(nil)

func NewRepository(pool *pgxpool.Pool, ids ports.IDGen, key []byte) *Repository {
	return &Repository{NewManager(pool, ids, key)}
}
func (r *Repository) SetSubmissionRepository(v ports.DownloadSubmissionRepository) {
	r.execution.SetSubmissionRepository(v)
}
func (r *Repository) RegisterClient(id uuid.UUID, v ports.DownloadClient) {
	r.execution.RegisterClient(id, v)
}
func (r *Repository) SetClientFactory(v ports.DownloadClientFactory) { r.execution.SetClientFactory(v) }
func (r *Repository) DeleteClient(ctx context.Context, id uuid.UUID) error {
	return r.execution.DeleteClient(ctx, id)
}
func (r *Repository) TestClient(ctx context.Context, id uuid.UUID) (bool, string, error) {
	return r.execution.TestClient(ctx, id)
}
func taskDTO(t *db.DownloadTask, e error) (*ports.DownloadExecutionTask, error) {
	if t == nil {
		return nil, e
	}
	out := &ports.DownloadExecutionTask{ID: t.ID, OperationID: t.OperationID, ClientID: t.ClientID, CandidateID: t.CandidateID, InfoHash: t.InfoHash, State: t.State, SavePath: t.SavePath, ContentPath: t.ContentPath.String, OrganizationState: t.OrganizationState, Progress: t.Progress, TotalBytes: t.TotalBytes, DownloadedBytes: t.DownloadedBytes}
	if t.TransferPlanID.Valid {
		id := uuid.UUID(t.TransferPlanID.Bytes)
		out.TransferPlanID = &id
	}
	return out, e
}
func (r *Repository) SubmitDownload(ctx context.Context, op, client, candidate uuid.UUID, path string, authority ...ports.JobLease) (*ports.DownloadExecutionTask, error) {
	return taskDTO(r.execution.SubmitDownload(ctx, op, client, candidate, path, authority...))
}
func (r *Repository) ReconcileTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return taskDTO(r.execution.ReconcileTask(ctx, op))
}
func (r *Repository) ObserveTask(ctx context.Context, op uuid.UUID) (*ports.DownloadExecutionTask, error) {
	return taskDTO(r.execution.ObserveTask(ctx, op))
}
