package ports

import (
	"errors"
	"time"
)

var ErrDownloadTelemetryStale = errors.New("download telemetry identity or source changed")

// Telemetry is read-only display data, never ownership or execution evidence.
type OperationTelemetry struct {
	Progress                   *float64
	ProgressSource             string
	ProgressObservedAt         *time.Time
	DownloadRateBytesPerSecond *string
	RateObservedAt             *time.Time
	RateStatus                 string
}
