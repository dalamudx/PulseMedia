package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	SiteCheckinJobKind            = "site_checkin"
	SiteCheckinObservationJobKind = "site_checkin_observe"
)

// Calendar bounds and Now come from PostgreSQL, not the application clock.
type SiteCheckinWindow struct {
	Now      time.Time
	DayStart time.Time
	DayEnd   time.Time
}

// Private material must never be serialized into jobs, events, or HTTP responses.
type SiteCheckinExecution struct {
	DayID           uuid.UUID         `json:"-"`
	ExecutionID     uuid.UUID         `json:"-"`
	Config          SiteCheckinConfig `json:"-"`
	CalendarDay     string            `json:"-"`
	ObservationOnly bool              `json:"-"`
	SourceURL       string            `json:"-"`
	Cookie          string            `json:"-"`
}

// The preparation is durable before this local, single-use send grant is returned.
type SiteCheckinSendPermission struct {
	ID          uuid.UUID
	DayID       uuid.UUID
	ExecutionID uuid.UUID
	Window      SiteCheckinWindow
	ValidUntil  time.Time
}

// Results carry native evidence; there is no caller-supplied success flag.
type SiteCheckinOutcome struct {
	Observation *CheckinObservation
	Submission  *CheckinSubmission
	Permission  *SiteCheckinSendPermission
	Failure     error
}

type SiteCheckinClientBuilder interface {
	NewSiteCheckinClient(SiteCheckinExecution) (SiteCheckinNativeClient, error)
}

// Close is called only after native I/O actually returns, including cancellation.
// RateLimitError from a reservation is a database budget wait, not permission to
// repeat an HTTP request. PrepareSubmit errors never authorize a POST.
type SiteCheckinPermit interface {
	Snapshot() SiteCheckinExecution
	Check(context.Context) (SiteCheckinWindow, error)
	ReserveObserve(context.Context) (SiteCheckinWindow, error)
	PrepareSubmit(context.Context, CheckinObservation) (SiteCheckinSendPermission, error)
	Finish(context.Context, SiteCheckinOutcome) error
	Close()
}

type SiteCheckinRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (SiteCheckinPermit, error)
}
