package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sort"
	"time"
)

type TVRefreshService struct {
	repo           ports.TVRefreshRepository
	provider       ports.TVEpisodeProvider
	supplement     ports.TVSeasonSupplementProvider
	supplementRepo ports.TVSupplementRefreshRepository
}

func NewTVRefreshService(repo ports.TVRefreshRepository, provider ports.TVEpisodeProvider) *TVRefreshService {
	return &TVRefreshService{repo: repo, provider: provider}
}
func NewTVRefreshServiceWithSupplements(repo ports.TVSupplementRefreshRepository, provider ports.TVEpisodeProvider, supplement ports.TVSeasonSupplementProvider) *TVRefreshService {
	return &TVRefreshService{repo: repo, provider: provider, supplement: supplement, supplementRepo: repo}
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
	supplements := []ports.TVDBSeasonSupplement{}
	for _, season := range c.Seasons {
		observation, primaryErr := s.provider.GetTVSeasonMetadata(network, input.TMDBID, season, "zh-CN")
		if network.Err() != nil {
			return nil, network.Err()
		}
		if primaryErr != nil && (s.supplement == nil || !errors.Is(primaryErr, ports.ErrNotFound)) {
			return nil, ports.ErrUnavailable
		}
		if primaryErr != nil {
			observation = nil
		} else if observation == nil || !observation.Complete || observation.SeasonNumber != season {
			return nil, ports.ErrUnavailable
		}
		if s.supplement != nil {
			supplement, err := s.supplement.ReadSeasonSupplement(network, input.TMDBID, season, observation)
			if network.Err() != nil {
				return nil, network.Err()
			}
			if err != nil && !errors.Is(err, ports.ErrNotFound) {
				if errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
					return nil, ports.ErrSubscriptionBindingsConflict
				}
				return nil, ports.ErrUnavailable
			}
			if err == nil && supplement != nil && len(supplement.Added) > 0 {
				if supplement.TMDBSeriesID != input.TMDBID || supplement.TVDBSeriesID <= 0 || supplement.Metadata.Complete || supplement.Metadata.SeasonNumber != season || supplement.CredentialVersion < 1 || supplement.FallbackVersion < 1 {
					return nil, ports.ErrUnavailable
				}
				observations = append(observations, supplement.Metadata)
				supplements = append(supplements, *supplement)
				continue
			}
		}
		if observation == nil || len(observation.Episodes) == 0 {
			return nil, ports.ErrUnavailable
		}
		observations = append(observations, *observation)
	}
	if err := network.Err(); err != nil {
		return nil, err
	}
	if len(supplements) > 0 {
		if s.supplementRepo == nil {
			return nil, ports.ErrUnavailable
		}
		return s.supplementRepo.ApplyTVRefreshWithSupplements(ctx, a, input, c, observations, supplements, key, hash)
	}
	return s.repo.ApplyTVRefresh(ctx, a, input, c, observations, key, hash)
}
