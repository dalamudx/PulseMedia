package ports

import "github.com/google/uuid"

// SeasonMetadataUnit binds one canonical coordinate to the metadata used by a complete provider response.
type SeasonMetadataUnit struct {
	UnitID        uuid.UUID `json:"unit_id"`
	UnitVersion   int64     `json:"unit_version"`
	TMDBEpisodeID int64     `json:"tmdb_episode_id"`
	EpisodeNumber int       `json:"episode_number"`
}

// CompleteSeasonMetadata is metadata evidence, not candidate coverage or acquisition permission.
// It is current only while its media version and entire active unit set still match.
type CompleteSeasonMetadata struct {
	Season       int                  `json:"season"`
	SourceTMDBID int64                `json:"source_tmdb_id"`
	MediaVersion int64                `json:"media_version"`
	ContentHash  string               `json:"content_hash"`
	Units        []SeasonMetadataUnit `json:"units"`
}
