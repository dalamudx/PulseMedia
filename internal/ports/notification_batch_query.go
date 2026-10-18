package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type NotificationBatchSummary struct {
	ID               uuid.UUID  `json:"id"`
	MediaID          uuid.UUID  `json:"media_id"`
	ChannelID        uuid.UUID  `json:"channel_id"`
	RecipientName    string     `json:"recipient_name"`
	RecipientEnabled bool       `json:"recipient_enabled"`
	State            string     `json:"state"`
	Deadline         time.Time  `json:"deadline"`
	EventID          *uuid.UUID `json:"event_id,omitempty"`
	MemberCount      int        `json:"member_count"`
	UnitCount        int        `json:"unit_count"`
	FileCount        int        `json:"file_count"`
	RootCount        int        `json:"root_count"`
}
type NotificationBatchQueryRepository interface {
	ListNotificationBatches(context.Context, *Actor) ([]NotificationBatchSummary, error)
}
