package ports

import (
	"context"
	"time"
)

// NexusPHP1103CheckinProfile requires the native 1.10.3 attendance structure.
const NexusPHP1103CheckinProfile = "nexusphp_1103_attendance_v1"

type CheckinMark string

const (
	CheckinMarkPending  CheckinMark = "pending"
	CheckinMarkAttended CheckinMark = "attended"
)

// CheckinObservation contains no calendar date: the native welcome line has none.
// Its marker denotes the site's own today, not the injected clock's date.
type CheckinObservation struct {
	AccountID   string      `json:"account_id"`
	Mark        CheckinMark `json:"mark"`
	RequestedAt time.Time   `json:"requested_at"`
	ObservedAt  time.Time   `json:"observed_at"`
}

type CheckinSubmissionState string

const (
	CheckinSubmissionNotSent  CheckinSubmissionState = "not_sent"
	CheckinSubmissionUnknown  CheckinSubmissionState = "unknown"
	CheckinSubmissionAttended CheckinSubmissionState = "attended"
)

// Attended confirms only the native final state, never attribution to this call.
type CheckinSubmission struct {
	State       CheckinSubmissionState `json:"state"`
	Observation *CheckinObservation    `json:"observation,omitempty"`
}

// SiteCheckinNativeClient has separate observation and side-effect operations.
// A SubmitCheckin error after request dispatch must preserve an unknown outcome.
// Neither method receives an arbitrary request URL or a caller-supplied success flag.
type SiteCheckinNativeClient interface {
	ObserveCheckin(ctx context.Context, expectedAccountID string) (CheckinObservation, error)
	SubmitCheckin(ctx context.Context, expectedAccountID string) (CheckinSubmission, error)
}
