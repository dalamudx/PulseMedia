package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

// SubscriptionListReader assembles the narrow list reader for the HTTP usecase.
func (a *App) SubscriptionListReader() ports.SubscriptionListReader {
	return postgres.NewSubscriptionListStore(a.dbPool)
}
