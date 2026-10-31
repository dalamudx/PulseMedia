package app

import (
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/librarymaintenance"
)

func (a *App) configureLibraryMaintenance(handler *httpapi.ServerHandler) error {
	repo, err := postgres.NewLibraryMaintenanceStore(a.dbPool, rootfs.NewFilesystem(), a.idgen)
	if err != nil {
		return err
	}
	handler.SetLibraryMaintenanceService(librarymaintenance.NewService(repo))
	return nil
}
