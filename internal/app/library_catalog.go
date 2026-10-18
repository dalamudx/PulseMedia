package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

// LibraryCatalogReader exposes actual mapped files in shared library roots.
func (a *App) LibraryCatalogReader() ports.LibraryCatalogReader {
	return postgres.NewLibraryCatalogStore(a.dbPool)
}
