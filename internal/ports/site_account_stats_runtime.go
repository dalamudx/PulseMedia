package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const SiteAccountStatsJobKind = "site_account_stats_collect"

// Execution material is private and bound to the versioned configuration.
type SiteAccountStatsExecution struct {
	Config    SiteAccountStatsConfig `json:"-"`
	SourceURL string                 `json:"-"`
	Cookie    string                 `json:"-"`
}

type SiteAccountStatsView struct {
	Config              SiteAccountStatsConfig `json:"config"`
	Stats               *SiteAccountStats      `json:"stats"`
	ObservedAt          *time.Time             `json:"observed_at"`
	SampleConfigVersion int64                  `json:"sample_config_version"`
	SampleSourceVersion int64                  `json:"sample_source_version"`
	LastAttemptAt       *time.Time             `json:"last_attempt_at"`
	CollectionState     string                 `json:"collection_state"`
	FailureCode         string                 `json:"failure_code,omitempty"`
	Stale               bool                   `json:"stale"`
}

type SiteAccountStatsClientBuilder interface {
	NewSiteAccountStatsClient(SiteAccountStatsExecution) (SiteAccountStatsClient, error)
}

// A permit retains source exclusion until Close, including after cancellation.
type SiteAccountStatsPermit interface {
	Snapshot() SiteAccountStatsExecution
	Reserve(context.Context) error
	Check(context.Context) error
	Finish(context.Context, *SiteAccountStats, error) error
	Close()
}

type SiteAccountStatsRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (SiteAccountStatsPermit, error)
	ReadSiteAccountStats(context.Context, *Actor, uuid.UUID) (*SiteAccountStatsView, error)
}
