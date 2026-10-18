package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// SearchSecrets uses the same master-key envelope as the credential decryptors.
type SearchSecrets interface {
	SecretProtector
	CredentialDecoder
}

type SearchIndexer struct {
	ID                                                  uuid.UUID
	Name                                                string
	Url                                                 string `json:"-"`
	Protocol                                            IndexerProtocol
	LegacyV1                                            bool
	ApiKeyEncrypted                                     []byte `json:"-"`
	Categories                                          []byte
	ConfigVersion                                       int64
	Health                                              string
	RateLimitUntil                                      *time.Time
	CreatedAt, UpdatedAt                                time.Time
	NextQueryAt                                         *time.Time
	RssEnabled                                          bool
	RssKind                                             string
	RssUrlEncrypted                                     []byte `json:"-"`
	RssIntervalSeconds                                  int32
	RssNextPollAt                                       *time.Time
	RssLastQueryID                                      *uuid.UUID
	RssLastSuccessAt, HealthCheckedAt, HealthObservedAt *time.Time
	HealthQueryID                                       *uuid.UUID
	HealthNextCheckAt                                   *time.Time
	HealthFailures                                      int32
	HealthReason                                        string
}
type SearchCandidate struct {
	ID, IndexerID                 uuid.UUID
	Title, Guid                   string
	LinkEncrypted                 []byte `json:"-"`
	InfoHash                      *string
	SizeBytes                     int64
	Seeders, Leechers             int32
	PublishDate                   *time.Time
	QualityProfile                []byte
	MatchedMediaID, MatchedUnitID *uuid.UUID
	CreatedAt                     time.Time
	SourceConfigVersion           int64
}

// SearchQuerySpec freezes the entire query identity before acquiring a permit.
type SearchQuerySpec struct {
	ID, IndexerID   uuid.UUID
	Keyword         string
	Page            int32
	ConfigVersion   int64
	RSSJobID        uuid.UUID
	HealthJob       *IndexerHealthJob
	Actor           *Actor `json:"-"`
	ProbeOnly       bool
	MinimumInterval time.Duration
}
type SearchQueryAdmission struct {
	Indexer SearchIndexer
	Shared  *IndexerQueryResult
}
type SearchPageEvidence struct {
	QueryID              uuid.UUID
	PageOffset           int64
	PageLimit, ItemCount int32
	OffsetVerified       bool
	Total                *int64
}
type SearchQueryCommit struct {
	Candidates []SearchCandidate
	Page       *SearchPageEvidence
}

// Failure is a closed safe category; implementations never persist caller text.
type SearchQueryFailure string

const (
	SearchFailureUnavailable   SearchQueryFailure = "unavailable"
	SearchFailureAuth          SearchQueryFailure = "auth_required"
	SearchFailureTimeout       SearchQueryFailure = "timeout"
	SearchFailureBadResponse   SearchQueryFailure = "bad_response"
	SearchFailureUnsupported   SearchQueryFailure = "unsupported"
	SearchFailureRateLimited   SearchQueryFailure = "rate_limited"
	SearchFailureInvalidResult SearchQueryFailure = "invalid_result"
)

// A permit owns one frozen query and the original dedicated advisory-lock session.
// A shared admission returns no permit and therefore grants no write authority.
type SearchExecutionPermit interface {
	BeginQuery(context.Context) (*IndexerQueryResult, error)
	ReserveHealthSearch(context.Context) error
	CommitQuery(context.Context, SearchQueryCommit) (*IndexerQueryResult, error)
	FailQuery(context.Context, SearchQueryFailure, time.Duration) error
	Close()
}
type SearchRepository interface {
	GetIndexer(context.Context, uuid.UUID) (SearchIndexer, error)
	ListIndexers(context.Context) ([]SearchIndexer, error)
	CreateIndexer(context.Context, SearchIndexer) (SearchIndexer, error)
	DeleteIndexer(context.Context, uuid.UUID) error
	AdmitQuery(context.Context, SearchQuerySpec) (SearchQueryAdmission, SearchExecutionPermit, error)
	ReadPage(context.Context, uuid.UUID) (SearchPageEvidence, error)
	ReadQueryCandidates(context.Context, uuid.UUID) ([]SearchCandidate, error)
	ContinuationDelay(context.Context, uuid.UUID, int64) (time.Duration, error)
	ReadAggregateSnapshot(context.Context, []uuid.UUID, bool) ([]SearchCandidate, bool, error)
}
