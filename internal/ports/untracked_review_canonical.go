package ports

import (
	"context"
	"github.com/google/uuid"
	"math"
)

// CanonicalSelectionValid keeps absent selection byte-compatible with old receipts.
func (in UntrackedReviewInput) CanonicalSelectionValid() bool {
	if in.CanonicalUnitID == "" {
		return in.MediaMetadataVersion == 0 && in.UnitMetadataVersion == 0 && in.Season == 0 && in.Episode == 0 && in.TMDBEpisodeID == 0
	}
	id, err := uuid.Parse(in.CanonicalUnitID)
	return err == nil && id != uuid.Nil && id.String() == in.CanonicalUnitID && in.MediaType == "tv" && in.TMDBID > 0 && in.MediaMetadataVersion > 0 && in.MediaMetadataVersion < math.MaxInt64 && in.UnitMetadataVersion > 0 && in.UnitMetadataVersion < math.MaxInt64 && in.Season > 0 && in.Season <= 99 && in.Episode > 0 && in.Episode <= 9999 && in.TMDBEpisodeID > 0
}

func (in UntrackedReviewInput) CanonicalCandidateMatches(c *UntrackedReviewCandidate) bool {
	if in.CanonicalUnitID == "" {
		return true
	}
	return in.CanonicalSelectionValid() && c != nil && c.Namespace == "tmdb.tv" && c.TMDBID == in.TMDBID && len(c.Units) == 1 && c.Units[0].Season == in.Season && c.Units[0].Episode == in.Episode && c.Units[0].TMDBEpisodeID == in.TMDBEpisodeID
}

// Options are current local TMDB identities, never generated from episode counts.
type UntrackedReviewEpisodeOption struct {
	CanonicalUnitID      string `json:"canonical_unit_id"`
	MediaMetadataVersion int64  `json:"media_metadata_version"`
	UnitMetadataVersion  int64  `json:"unit_metadata_version"`
	Season               int    `json:"season"`
	Episode              int    `json:"episode"`
	TMDBEpisodeID        int64  `json:"tmdb_episode_id"`
	UnitKey              string `json:"unit_key"`
	Title                string `json:"title"`
}

type UntrackedReviewEpisodeRepository interface {
	ReadUntrackedReviewEpisodeOptions(context.Context, *Actor, uuid.UUID, uuid.UUID, int64, int) ([]UntrackedReviewEpisodeOption, error)
}
