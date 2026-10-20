package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Saving configuration records intent; it never observes or submits attendance.
type SiteCheckinConfigInput struct {
	ExpectedAccountID     string `json:"expected_account_id"`
	Profile               string `json:"profile"`
	Timezone              string `json:"timezone"`
	DailyAt               string `json:"daily_at"`
	Enabled               bool   `json:"enabled"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedSourceVersion int64  `json:"expected_source_version"`
}

type SiteCheckinConfig struct {
	IndexerID           uuid.UUID `json:"indexer_id"`
	ExpectedAccountID   string    `json:"expected_account_id"`
	Profile             string    `json:"profile"`
	Timezone            string    `json:"timezone"`
	DailyAt             string    `json:"daily_at"`
	Enabled             bool      `json:"enabled"`
	Version             int64     `json:"version"`
	SourceConfigVersion int64     `json:"source_config_version"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type SiteCheckinConfigReceipt struct {
	CommandKey string            `json:"command_key"`
	Config     SiteCheckinConfig `json:"config"`
}

type SiteCheckinConfigRepository interface {
	ReadSiteCheckinConfig(context.Context, *Actor, uuid.UUID) (*SiteCheckinConfig, error)
	SaveSiteCheckinConfig(context.Context, *Actor, uuid.UUID, SiteCheckinConfigInput, string) (*SiteCheckinConfigReceipt, error)
	ReadSiteCheckinConfigReceipt(context.Context, *Actor, uuid.UUID, string) (*SiteCheckinConfigReceipt, error)
}
