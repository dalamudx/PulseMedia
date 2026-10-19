package app

import (
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

// EndpointConfigurationService assembles the management boundary once per HTTP
// handler. The native testers retain ownership of network calls outside its UoW.
func (a *App) EndpointConfigurationService(masterKey []byte, download ports.DownloadEndpointTester, media ports.MediaEndpointTester) *settings.EndpointConfigurationService {
	repo := postgres.NewEndpointConfigurationStore(a.DBPool(), a.IDGen(), crypto.NewSecretProtector(masterKey))
	return settings.NewEndpointConfigurationService(repo, download, media)
}
