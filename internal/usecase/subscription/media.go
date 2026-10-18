package subscription

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type SeasonEpisodes = ports.SeasonEpisodes

// subscriptionMediaUnits validates trusted metadata and defines canonical units.
func subscriptionMediaUnits(in ports.SubscriptionMediaInput) ([]ports.SubscriptionMediaUnitSpec, error) {
	if in.Namespace == "" || in.ExternalID == "" || in.Title == "" {
		return nil, ErrInvalidScope
	}
	switch in.MediaType {
	case "movie":
		return []ports.SubscriptionMediaUnitSpec{{UnitKey: media.FormatMovieUnitKey()}}, nil
	case "tv":
		units := []ports.SubscriptionMediaUnitSpec{}
		seen := map[int]bool{}
		for _, season := range in.Seasons {
			if season.Season < 0 || season.Season > 999 || season.Count < 0 || season.Count > 5000 || seen[season.Season] {
				return nil, ErrInvalidScope
			}
			seen[season.Season] = true
			for ep := 1; ep <= season.Count; ep++ {
				sn, en := int32(season.Season), int32(ep)
				units = append(units, ports.SubscriptionMediaUnitSpec{UnitKey: media.FormatEpisodeUnitKey(season.Season, ep), SeasonNumber: &sn, EpisodeNumber: &en})
			}
		}
		if len(units) == 0 {
			return nil, ErrInvalidScope
		}
		return units, nil
	default:
		return nil, ErrInvalidScope
	}
}

// EnsureMedia independently commits identity and units. CreateSubscription uses
// its own intent transaction so failed creation never publishes partial media.
func (s *Service) EnsureMedia(ctx context.Context, namespace, externalID, title, mediaType string, year *int32, seasons []SeasonEpisodes) (uuid.UUID, error) {
	in := ports.SubscriptionMediaInput{Namespace: namespace, ExternalID: externalID, Title: title, MediaType: mediaType, Year: year, Seasons: seasons}
	units, e := subscriptionMediaUnits(in)
	if e != nil {
		return uuid.Nil, e
	}
	return s.media.EnsureSubscriptionMedia(ctx, in, units)
}
