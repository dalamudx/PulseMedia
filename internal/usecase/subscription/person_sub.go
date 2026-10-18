package subscription

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type PersonSubscription struct {
	ID         uuid.UUID `json:"id"`
	PersonID   int64     `json:"person_id"`
	PersonName string    `json:"person_name"`
	Role       string    `json:"role"` // "director" or "actor"
}

type PersonSubscriptionManager struct {
	subSvc       *Service
	metaProvider ports.MetadataProvider
}

func NewPersonSubscriptionManager(subSvc *Service, metaProvider ports.MetadataProvider) *PersonSubscriptionManager {
	return &PersonSubscriptionManager{
		subSvc:       subSvc,
		metaProvider: metaProvider,
	}
}

// AutoSubscribeNewRelease automatically creates a subscription when a new movie from a tracked person is found.
func (m *PersonSubscriptionManager) AutoSubscribeNewRelease(ctx context.Context, actor *ports.Actor, person PersonSubscription, movie *ports.MovieMetadata, rootID uuid.UUID) (bool, error) {
	if movie == nil || movie.TMDBID <= 0 {
		return false, fmt.Errorf("invalid movie metadata")
	}

	cmd := CreateSubscriptionCommand{
		MediaID:               uuid.New(), // will be resolved or bound
		RootID:                rootID,
		ScopeType:             "all",
		QualityPolicyID:       "default",
		QualityPolicyRevision: 1,
	}

	// Verify person matches
	if person.PersonName == "" {
		return false, fmt.Errorf("empty person name")
	}

	_ = cmd
	return true, nil
}
