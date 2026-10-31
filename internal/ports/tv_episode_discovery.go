package ports

import (
 "context"
 "time"
 "github.com/google/uuid"
)

type TVEpisodeDiscoveryUnit struct { SubscriptionID uuid.UUID; UnitID uuid.UUID }
type TVEpisodeDiscoveryStatus struct {
 SubscriptionID uuid.UUID `json:"subscription_id"`
 UnitID uuid.UUID `json:"unit_id"`
 SearchID *uuid.UUID `json:"search_id,omitempty"`
 CurrentFileID *uuid.UUID `json:"current_file_id,omitempty"`
 NextDiscoveryAt *time.Time `json:"next_discovery_at,omitempty"`
 State string `json:"state"`
 ExecutionAllowed bool `json:"execution_allowed"`
}
type TVEpisodeDiscoveryRepository interface {
 ListDueTVEpisodeDiscoveries(context.Context,int32) ([]TVEpisodeDiscoveryUnit,error)
 ScheduleTVEpisodeDiscovery(context.Context,TVEpisodeDiscoveryUnit) (bool,error)
 ReadTVEpisodeDiscovery(context.Context,*Actor,TVEpisodeDiscoveryUnit) (*TVEpisodeDiscoveryStatus,error)
}
