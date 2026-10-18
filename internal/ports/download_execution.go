package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrDownloadPathMismatch = errors.New("torrent save path does not match expected destination")
var ErrDownloadReconcilePending = errors.New("task is still unknown, pending reconciliation")
var ErrDownloadClientInUse = errors.New("download client has tasks or subscriptions")

type DownloadExecutionTask struct {
	ID, OperationID, ClientID, CandidateID                    uuid.UUID
	InfoHash, State, SavePath, ContentPath, OrganizationState string
	Progress                                                  float32
	TotalBytes, DownloadedBytes                               int64
	TransferPlanID                                            *uuid.UUID
}
type DownloadClientFactory func(clientType, baseURL, username, password string) (DownloadClient, error)
type DownloadTaskExecution interface {
	SubmitDownload(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, ...JobLease) (*DownloadExecutionTask, error)
	ReconcileTask(context.Context, uuid.UUID) (*DownloadExecutionTask, error)
	ObserveTask(context.Context, uuid.UUID) (*DownloadExecutionTask, error)
}
type DownloadClientAdministration interface {
	DeleteClient(context.Context, uuid.UUID) error
	TestClient(context.Context, uuid.UUID) (bool, string, error)
}
type DownloadRuntimeConfiguration interface {
	SetSubmissionRepository(DownloadSubmissionRepository)
	RegisterClient(uuid.UUID, DownloadClient)
	SetClientFactory(DownloadClientFactory)
}
type DownloadExecutionRepository interface {
	DownloadTaskExecution
	DownloadClientAdministration
	DownloadRuntimeConfiguration
}
