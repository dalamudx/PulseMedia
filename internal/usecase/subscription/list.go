package subscription

import (
	"context"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// ListService enriches a complete authorized database snapshot after it is closed.
type ListService struct {
	reader   ports.SubscriptionListReader
	metadata ports.MetadataProvider
}

func NewListService(reader ports.SubscriptionListReader, metadata ports.MetadataProvider) *ListService {
	return &ListService{reader: reader, metadata: metadata}
}

type ListEntry struct {
	ports.SubscriptionListEntry
	PosterURL                        *string
	TotalEpisodes, SatisfiedEpisodes int
}

func (s *ListService) List(ctx context.Context, actor *ports.Actor) ([]ListEntry, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	rows, err := s.reader.ReadSubscriptionList(ctx, actor)
	if err != nil {
		return nil, err
	}
	items := make([]ListEntry, len(rows))
	// Request-local memoization preserves provider behavior for each distinct media.
	posters := make(map[uuid.UUID]*string)
	for i, row := range rows {
		item := ListEntry{SubscriptionListEntry: row}
		for _, wanted := range row.WantedItems {
			if !wanted.IsSkipped {
				item.TotalEpisodes++
				if wanted.IsSatisfied {
					item.SatisfiedEpisodes++
				}
			}
		}
		if item.TotalEpisodes == 0 && len(row.WantedItems) > 0 {
			item.TotalEpisodes = len(row.WantedItems)
		}
		poster, seen := posters[row.MediaID]
		if !seen {
			poster = s.poster(ctx, row)
			posters[row.MediaID] = poster
		}
		item.PosterURL = poster
		items[i] = item
	}
	return items, nil
}

func (s *ListService) poster(ctx context.Context, row ports.SubscriptionListEntry) *string {
	id, _ := strconv.ParseInt(row.TMDBExternalID, 10, 64)
	if id <= 0 || s.metadata == nil {
		return nil
	}
	if row.MediaType == "tv" {
		if tv, err := s.metadata.GetTVDetails(ctx, id, "zh-CN"); err == nil && tv != nil && tv.PosterURL != "" {
			return &tv.PosterURL
		}
	} else {
		if movie, err := s.metadata.GetMovieDetails(ctx, id, "zh-CN"); err == nil && movie != nil && movie.PosterURL != "" {
			return &movie.PosterURL
		}
	}
	return nil
}
