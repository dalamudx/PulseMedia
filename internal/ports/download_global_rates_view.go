package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Repository material, not HTTP responses.
type DownloadGlobalRateSample struct {
	JobID                  uuid.UUID
	ClientID               uuid.UUID
	ConfigStamp            string
	State                  string
	ObservedAt             *time.Time
	FinishedAt             *time.Time
	DownloadBytesPerSecond *int64
	UploadBytesPerSecond   *int64
	FailureCode            string
}
type DownloadGlobalRateSource struct {
	ID          uuid.UUID
	Name        string
	ClientType  string
	ConfigStamp string
	Latest      *DownloadGlobalRateSample
	LastGood    *DownloadGlobalRateSample
}
type DownloadGlobalRatesSnapshot struct {
	ObservedAt time.Time
	Sources    []DownloadGlobalRateSource
	History    []DownloadGlobalRateSample
}
type DownloadGlobalRatesOverviewReader interface {
	ReadDownloadGlobalRatesSnapshot(context.Context, *Actor) (*DownloadGlobalRatesSnapshot, error)
}
type DownloadGlobalRateValues struct {
	ObservedAt             time.Time
	DownloadBytesPerSecond *string
	UploadBytesPerSecond   *string
}
type DownloadGlobalRateClientView struct {
	ID                     uuid.UUID
	Name                   string
	ClientType             string
	State                  string
	FailureCode            string
	ObservedAt             *time.Time
	DownloadBytesPerSecond *string
	UploadBytesPerSecond   *string
	LastGood               *DownloadGlobalRateValues
}
type DownloadGlobalRatePoint struct {
	At                     time.Time
	DownloadBytesPerSecond *string
	UploadBytesPerSecond   *string
	DownloadKnownClients   int
	UploadKnownClients     int
}
type DownloadGlobalRatesOverview struct {
	Enabled                bool
	ObservedAt             time.Time
	FreshnessSeconds       int
	DownloadBytesPerSecond *string
	UploadBytesPerSecond   *string
	DownloadKnownClients   int
	UploadKnownClients     int
	Clients                []DownloadGlobalRateClientView
	Series                 []DownloadGlobalRatePoint
}
