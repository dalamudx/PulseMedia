package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

const DownloadGlobalRatesJobKind = "download_global_rates"

// DownloadGlobalRatesSource is private executable material issued by its
// repository permit. It is never a public response or a command receipt.
type DownloadGlobalRatesSource struct {
	ClientID    uuid.UUID `json:"-"`
	ConfigStamp string    `json:"-"`
	ClientType  string    `json:"-"`
	URL         string    `json:"-"`
	Username    string    `json:"-"`
	Password    string    `json:"-"`
}

type DownloadGlobalRatesFailure string

const (
	DownloadGlobalRatesOK          DownloadGlobalRatesFailure = ""
	DownloadGlobalRatesUnavailable DownloadGlobalRatesFailure = "unavailable"
	DownloadGlobalRatesAuth        DownloadGlobalRatesFailure = "auth_required"
	DownloadGlobalRatesTimeout     DownloadGlobalRatesFailure = "timeout"
	DownloadGlobalRatesBlocked     DownloadGlobalRatesFailure = "blocked"
	DownloadGlobalRatesBadResponse DownloadGlobalRatesFailure = "bad_response"
	DownloadGlobalRatesLimited     DownloadGlobalRatesFailure = "rate_limited"
)

// ObservedAt records native return time, not a possibly delayed commit time.
// Failure observations contain no rates. Last-good values are stored separately
// and cannot be presented as the current successful observation.
type DownloadGlobalRatesObservation struct {
	ObservedAt time.Time
	Rates      *DownloadGlobalRates
	Failure    DownloadGlobalRatesFailure
	RetryAfter time.Duration
}

type DownloadGlobalRatesBuilder interface {
	NewDownloadGlobalRatesReader(DownloadGlobalRatesSource) (DownloadGlobalRatesReader, error)
}

// Close releases source exclusion only after native I/O has actually returned.
// Finish atomically rechecks configuration, job state, owner/token and lease.
type DownloadGlobalRatesPermit interface {
	Snapshot() DownloadGlobalRatesSource
	Check(context.Context) error
	Finish(context.Context, DownloadGlobalRatesObservation) error
	Close()
}

type DownloadGlobalRatesRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (DownloadGlobalRatesPermit, error)
}
