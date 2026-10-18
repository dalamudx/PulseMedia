package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrRSSConfigConflict = errors.New("RSS configuration or command version changed")

type IndexerQueryResult struct {
	QueryID      uuid.UUID
	State        string
	ResultsCount int
	FromShared   bool
}
type RSSFetcher interface {
	ExecuteRSS(context.Context, uuid.UUID, uuid.UUID, int64) (*IndexerQueryResult, error)
}
type SecretProtector interface {
	EncryptSecret([]byte) ([]byte, error)
	DigestSecret([]byte) (string, error)
}
type RSSInfo struct {
	IndexerID          uuid.UUID  `json:"indexer_id"`
	Enabled            bool       `json:"enabled"`
	Kind               string     `json:"kind"`
	IntervalSeconds    int32      `json:"interval_seconds"`
	ConfigVersion      int64      `json:"config_version"`
	NextPollAt         time.Time  `json:"next_poll_at"`
	LastQueryID        *uuid.UUID `json:"last_query_id,omitempty"`
	LastSuccessAt      *time.Time `json:"last_success_at,omitempty"`
	LastCandidateCount *int64     `json:"last_candidate_count,omitempty"`
}
type RSSSettings struct {
	Enabled         bool
	Kind            string
	IntervalSeconds int32
	ExpectedVersion int64
}
type PreparedRSSConfig struct {
	Actor                       *Actor
	IndexerID                   uuid.UUID
	Settings                    RSSSettings
	EncryptedURL                []byte
	IdempotencyKey, RequestHash string
}
type RSSPollJob struct {
	ID            uuid.UUID
	Owner         string
	Token         int64
	IndexerID     uuid.UUID `json:"indexer_id"`
	ConfigVersion int64     `json:"config_version"`
}

// Every mutator is a narrow transaction. No transaction or generated SQL type escapes.
type RSSRepository interface {
	GetRSS(context.Context, uuid.UUID) (*RSSInfo, error)
	ConfigureRSS(context.Context, PreparedRSSConfig) (*RSSInfo, error)
	QueueDue(context.Context, int32) (int, error)
	CheckRSS(context.Context, RSSPollJob) (bool, error)
	CompleteRSS(context.Context, RSSPollJob, *uuid.UUID) error
	BindRSSQuery(context.Context, RSSPollJob, uuid.UUID) error
	RetryRSSDelay(context.Context, uuid.UUID) time.Duration
}
