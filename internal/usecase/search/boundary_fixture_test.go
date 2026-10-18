package search_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestSubscriptionService(pool *pgxpool.Pool, ids ports.IDGen) *subscription.Service {
	intents := postgres.NewSubscriptionIntentStore(pool, ids)
	return subscription.NewService(intents, intents, postgres.NewSubscriptionControlStore(pool, ids), intents)
}
