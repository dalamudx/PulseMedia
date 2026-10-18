package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrEpisodeSourceConflict = errors.New("episode source inputs changed")

type EpisodeSourceUnit struct {
	ID                 uuid.UUID
	EpisodeID, Version int64
	Key                string
	Season, Episode    int
	Retired            bool
}
type EpisodeSourceContext struct {
	SubscriptionID, MediaID             uuid.UUID
	TMDBID, MediaVersion, IntentVersion int64
	MappingRevision                     int64
	InputStamp                          string
	Units                               []EpisodeSourceUnit
	CompleteSeasons                     []CompleteSeasonMetadata `json:",omitempty"`
}
type EpisodeSourceRepository interface {
	ReadEpisodeSourceContext(context.Context, *Actor, uuid.UUID) (*EpisodeSourceContext, error)
}
type EpisodeGroupList struct {
	MediaID    uuid.UUID               `json:"media_id"`
	InputStamp string                  `json:"input_stamp"`
	Groups     []TVEpisodeGroupSummary `json:"groups"`
}
type EpisodeGroupMappingRow struct {
	SourceNumber int       `json:"source_number"`
	EpisodeID    int64     `json:"episode_id"`
	UnitID       uuid.UUID `json:"unit_id"`
	UnitKey      string    `json:"unit_key"`
	UnitVersion  int64     `json:"unit_version"`
}
type EpisodeGroupPreview struct {
	MediaID        uuid.UUID                `json:"media_id"`
	GroupID        string                   `json:"group_id"`
	PartID         string                   `json:"part_id"`
	InputStamp     string                   `json:"input_stamp"`
	SourceHash     string                   `json:"source_hash"`
	CanMap         bool                     `json:"can_map"`
	CanUseAbsolute bool                     `json:"can_use_absolute"`
	HoldReason     string                   `json:"hold_reason"`
	Rows           []EpisodeGroupMappingRow `json:"rows"`
	Source         TVEpisodeGroupSnapshot   `json:"source"`
}
