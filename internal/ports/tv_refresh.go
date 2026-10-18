package ports

import (
	"context"
	"github.com/google/uuid"
)

type TVRefreshCommand struct {
	ExpectedVersion int64 `json:"expected_version"`
	Seasons         []int `json:"seasons"`
}
type TVRefreshContext struct {
	SubscriptionID, MediaID             uuid.UUID
	IntentVersion, MediaVersion, TMDBID int64
	Seasons                             []int
}
type TVRefreshReceipt struct {
	SubscriptionID   uuid.UUID `json:"subscription_id"`
	MediaID          uuid.UUID `json:"media_id"`
	MediaVersion     int64     `json:"media_version"`
	ObservedEpisodes int       `json:"observed_episodes"`
}
type TVRefreshRepository interface {
	ReadTVRefreshContext(context.Context, *Actor, uuid.UUID, TVRefreshCommand, string, string) (TVRefreshContext, *TVRefreshReceipt, error)
	ApplyTVRefresh(context.Context, *Actor, TVRefreshContext, TVRefreshCommand, []TVSeasonMetadata, string, string) (*TVRefreshReceipt, error)
}
