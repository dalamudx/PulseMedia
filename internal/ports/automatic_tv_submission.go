package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrAutomaticTVSubmissionReview = errors.New("automatic TV submission inputs require review; protection retained")
	ErrAutomaticTVNeedsObservation = errors.New("automatic TV submission requires observation of the original operation")
	ErrAutomaticTVResultStale      = errors.New("automatic TV submission result authority changed; protection retained")
)

type AutomaticTVSendRequest struct {
	Lease       JobLease  `json:"-"`
	OperationID uuid.UUID `json:"-"`
}

// AutomaticTVSendWork is transient, coherently authorized input for one send.
// Every secret and path is excluded from serialization. Prepare reads the
// original authority and current facts again after network metainfo inspection.
type AutomaticTVSendWork struct {
	Request                 AutomaticTVSendRequest        `json:"-"`
	Authority               AutomaticTVExecutionAuthority `json:"-"`
	RootPath                string                        `json:"-"`
	SavePath                string                        `json:"-"`
	ClientType              string                        `json:"-"`
	ClientURL               string                        `json:"-"`
	ClientUsername          string                        `json:"-"`
	ClientPasswordEncrypted []byte                        `json:"-"`
	SourceEndpointURL       string                        `json:"-"`
	SourceAPIKeyEncrypted   []byte                        `json:"-"`
	DownloadURLEncrypted    []byte                        `json:"-"`
}

type PrepareAutomaticTVSubmissionCommand struct {
	Request           AutomaticTVSendRequest
	TaskID            uuid.UUID
	PhysicalAttemptID uuid.UUID
	Metainfo          *AutomaticTVMetainfoProof `json:"-"`
}

type AutomaticTVSubmissionReceipt struct {
	TaskID         uuid.UUID `json:"task_id"`
	State          string    `json:"state"`
	AlreadyExisted bool      `json:"already_existed"`
}

// Record is observation of the original physical invocation. It does not grant
// another Add. Only an affirmative acknowledgement without error means tracking;
// cancellation, timeout and uncertain responses preserve unknown protection.
type AutomaticTVSubmissionResultCommand struct {
	Request           AutomaticTVSendRequest
	TaskID            uuid.UUID
	PhysicalAttemptID uuid.UUID
	Metainfo          *AutomaticTVMetainfoProof `json:"-"`
	Acknowledged      bool
	AddError          error `json:"-"`
}

type AutomaticTVSubmissionResultReceipt struct {
	TaskID uuid.UUID `json:"task_id"`
	State  string    `json:"state"`
}

type AutomaticTVSubmissionRepository interface {
	LoadAutomaticTVSendWork(context.Context, AutomaticTVSendRequest) (*AutomaticTVSendWork, error)
	PrepareAutomaticTVSubmission(context.Context, PrepareAutomaticTVSubmissionCommand) (*AutomaticTVSubmissionReceipt, error)
	RecordAutomaticTVSubmissionResult(context.Context, AutomaticTVSubmissionResultCommand) (*AutomaticTVSubmissionResultReceipt, error)
}

type AutomaticTVSendJobExecutor interface {
	ExecuteAutomaticTVSendJob(context.Context, AutomaticTVSendRequest) error
}

// AutomaticTVTorrentSource is ephemeral and accepted only by the configured
// source fetcher. It never enters jobs, audit payloads or public JSON.
type AutomaticTVTorrentSource struct {
	EndpointURL string `json:"-"`
	APIKey      string `json:"-"`
	DownloadURL string `json:"-"`
}

type AutomaticTVTorrentFetcher interface {
	FetchAutomaticTVTorrent(context.Context, AutomaticTVTorrentSource) ([]byte, error)
}
