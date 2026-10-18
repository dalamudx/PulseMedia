package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sort"
	"time"
)

type TVRefreshService struct {
	repo     ports.TVRefreshRepository
	provider ports.TVEpisodeProvider
}

func NewTVRefreshService(repo ports.TVRefreshRepository, provider ports.TVEpisodeProvider) *TVRefreshService {
	return &TVRefreshService{repo, provider}
}
func (s *TVRefreshService) Refresh(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.TVRefreshCommand, key string) (*ports.TVRefreshReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || c.ExpectedVersion < 1 || len(c.Seasons) < 1 || len(c.Seasons) > 5 || len(key) < 1 || len(key) > 160 {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	c.Seasons = append([]int(nil), c.Seasons...)
	sort.Ints(c.Seasons)
	for i, season := range c.Seasons {
		if season < 0 || season > 999 || i > 0 && season == c.Seasons[i-1] {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	}
	raw, _ := json.Marshal([]any{"tv.metadata.refresh", id, c})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	input, receipt, e := s.repo.ReadTVRefreshContext(ctx, a, id, c, key, hash)
	if e != nil || receipt != nil {
		return receipt, e
	}
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	network, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	observations := []ports.TVSeasonMetadata{}
	for _, season := range c.Seasons {
		observation, e := s.provider.GetTVSeasonMetadata(network, input.TMDBID, season, "zh-CN")
		if e != nil {
			if network.Err() != nil {
				return nil, network.Err()
			}
			return nil, ports.ErrUnavailable
		}
		if observation == nil || !observation.Complete || observation.SeasonNumber != season || len(observation.Episodes) == 0 {
			return nil, ports.ErrUnavailable
		}
		observations = append(observations, *observation)
	}
	return s.repo.ApplyTVRefresh(ctx, a, input, c, observations, key, hash)
}
