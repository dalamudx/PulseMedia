package subscription_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestPersonSubscriptionAutoTracking(t *testing.T) {
	mgr := subscription.NewPersonSubscriptionManager(nil, nil)
	ctx := context.Background()

	person := subscription.PersonSubscription{
		ID:         uuid.New(),
		PersonID:   525,
		PersonName: "Christopher Nolan",
		Role:       "director",
	}

	movie := &ports.MovieMetadata{
		TMDBID: 157336,
		Title:  "Interstellar",
		Year:   2014,
	}

	actor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   "admin-123",
		Role: ports.RoleAdmin,
	}

	ok, err := mgr.AutoSubscribeNewRelease(ctx, actor, person, movie, uuid.New())
	if err != nil || !ok {
		t.Fatalf("auto subscribe person release failed: %v", err)
	}
}
