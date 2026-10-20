package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// SiteCheckinHistoryCursor is a read position, never execution authority.
type SiteCheckinHistoryCursor struct {
	CalendarDay string
	DayID       uuid.UUID
}

type SiteCheckinDayView struct {
	ID                  uuid.UUID  `json:"id"`
	IndexerID           uuid.UUID  `json:"indexer_id"`
	ExpectedAccountID   string     `json:"expected_account_id"`
	Profile             string     `json:"profile"`
	Timezone            string     `json:"timezone"`
	CalendarDay         string     `json:"calendar_day"`
	DayStart            time.Time  `json:"day_start"`
	DayEnd              time.Time  `json:"day_end"`
	State               string     `json:"state"`
	SafeFailureCode     string     `json:"safe_failure_code"`
	ObservedAt          *time.Time `json:"observed_at"`
	Prepared            bool       `json:"prepared"`
	ConfigVersion       int64      `json:"config_version"`
	SourceVersion       int64      `json:"source_version"`
	RestoreAcknowledged bool       `json:"restore_acknowledged"`
}

type SiteCheckinHistoryPage struct {
	Config *SiteCheckinConfig
	Rows   []SiteCheckinDayView
	Next   *SiteCheckinHistoryCursor
}

type SiteCheckinHistoryReader interface {
	ReadSiteCheckinHistory(context.Context, *Actor, uuid.UUID, int32, *SiteCheckinHistoryCursor) (*SiteCheckinHistoryPage, error)
}
