package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"strconv"
	"time"
)

type DoubanWishItem struct {
	ID, Title string
	Year      int
	MediaType string
}
type DoubanSyncService struct {
	subSvc       *Service
	metaProvider ports.MetadataProvider
	httpClient   *http.Client
}

func NewDoubanSyncService(subSvc *Service, metaProvider ports.MetadataProvider, httpClient *http.Client) *DoubanSyncService {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &DoubanSyncService{subSvc, metaProvider, httpClient}
}
func (s *DoubanSyncService) FetchUserWishlist(ctx context.Context, doubanUID string) ([]DoubanWishItem, error) {
	if doubanUID == "" {
		return nil, fmt.Errorf("douban user ID cannot be empty")
	}
	reqURL := fmt.Sprintf("https://frodo.douban.com/api/v2/user/%s/interests?type=movie&status=wish&count=50", doubanUID)
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "api-client/1.0.0 com.douban.frodo")
	req.Header.Set("Accept", "application/json")
	resp, e := s.httpClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douban api status %d", resp.StatusCode)
	}
	var data struct {
		Interests []struct {
			Subject struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Year  string `json:"year"`
				Type  string `json:"type"`
			} `json:"subject"`
		} `json:"interests"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&data); e != nil {
		return nil, e
	}
	items := make([]DoubanWishItem, 0, len(data.Interests))
	for _, interest := range data.Interests {
		sb := interest.Subject
		year, _ := strconv.Atoi(sb.Year)
		kind := sb.Type
		if kind != "tv" {
			kind = "movie"
		}
		items = append(items, DoubanWishItem{ID: sb.ID, Title: sb.Title, Year: year, MediaType: kind})
	}
	return items, nil
}

// SyncUserWishlist retains the legacy explicit operation. Metadata I/O precedes
// the single atomic CreateSubscription call; it does not register a runtime job.
func (s *DoubanSyncService) SyncUserWishlist(ctx context.Context, doubanUID string, actor *ports.Actor, rootID uuid.UUID) (int, error) {
	if e := intentActor(actor); e != nil {
		return 0, e
	}
	items, e := s.FetchUserWishlist(ctx, doubanUID)
	if e != nil {
		return 0, fmt.Errorf("failed to fetch douban wishlist: %w", e)
	}
	count := 0
	for _, item := range items {
		if s.metaProvider == nil {
			continue
		}
		matches, e := s.metaProvider.SearchMedia(ctx, item.Title, item.MediaType, item.Year)
		if e != nil || len(matches) == 0 || matches[0] == nil {
			continue
		}
		best := matches[0]
		kind := item.MediaType
		if best.MediaType != "" {
			kind = best.MediaType
		}
		if kind != item.MediaType || best.TMDBID <= 0 {
			continue
		}
		input := &SubscriptionMediaInput{Namespace: "tmdb." + kind, ExternalID: strconv.FormatInt(best.TMDBID, 10), Title: best.Title, MediaType: kind}
		if best.Year > 0 {
			year := int32(best.Year)
			input.Year = &year
		}
		if kind == "tv" {
			details, e := s.metaProvider.GetTVDetails(ctx, best.TMDBID, "")
			if e != nil || details == nil || details.TMDBID != best.TMDBID {
				continue
			}
			for _, season := range details.Seasons {
				input.Seasons = append(input.Seasons, SeasonEpisodes{Season: season.SeasonNumber, Count: season.EpisodeCount})
			}
			if _, e = subscriptionMediaUnits(*input); e != nil {
				continue
			}
		}
		_, _, e = s.subSvc.CreateSubscription(ctx, actor, CreateSubscriptionCommand{MediaInput: input, RootID: rootID, ScopeType: "all"})
		if e == nil {
			count++
		}
	}
	return count, nil
}
