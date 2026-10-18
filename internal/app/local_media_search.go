package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func (a *App) LocalMediaSearchReader() ports.LocalMediaSearchReader {
	return postgres.NewLocalMediaSearchStore(a.DBPool())
}
