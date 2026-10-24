package subscription_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestPersonSubscriptionAutoTracking(t *testing.T) {
	manager := subscription.NewPersonSubscriptionManager(nil, nil)
	person := subscription.PersonSubscription{ID: uuid.New(), PersonID: 525, PersonName: "Christopher Nolan", Role: "director"}
	movie := &ports.MovieMetadata{TMDBID: 157336, Title: "Interstellar", Year: 2014}
	ok, err := manager.AutoSubscribeNewRelease(t.Context(), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}, person, movie, uuid.New())
	if ok || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("uncommitted legacy person subscription reported success: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ok, err = manager.AutoSubscribeNewRelease(ctx, nil, person, movie, uuid.New())
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
