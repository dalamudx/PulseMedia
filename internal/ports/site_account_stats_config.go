package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Configuration records intent; saving it does not certify a collected sample.
type SiteAccountStatsConfigInput struct {
	ExpectedAccountID     string `json:"expected_account_id"`
	Profile               string `json:"profile"`
	Enabled               bool   `json:"enabled"`
	IntervalSeconds       int32  `json:"interval_seconds"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedSourceVersion int64  `json:"expected_source_version"`
}

type SiteAccountStatsConfig struct {
	IndexerID           uuid.UUID `json:"indexer_id"`
	ExpectedAccountID   string    `json:"expected_account_id"`
	Profile             string    `json:"profile"`
	Enabled             bool      `json:"enabled"`
	IntervalSeconds     int32     `json:"interval_seconds"`
	Version             int64     `json:"version"`
	SourceConfigVersion int64     `json:"source_config_version"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type SiteAccountStatsConfigReceipt struct {
	CommandKey string                 `json:"command_key"`
	Config     SiteAccountStatsConfig `json:"config"`
}

type SiteAccountStatsConfigRepository interface {
	ReadSiteAccountStatsConfig(context.Context, *Actor, uuid.UUID) (*SiteAccountStatsConfig, error)
	SaveSiteAccountStatsConfig(context.Context, *Actor, uuid.UUID, SiteAccountStatsConfigInput, string) (*SiteAccountStatsConfigReceipt, error)
	ReadSiteAccountStatsConfigReceipt(context.Context, *Actor, uuid.UUID, string) (*SiteAccountStatsConfigReceipt, error)
}
