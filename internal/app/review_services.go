package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/subtitle/shooter"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	sidecarstore "github.com/dalamudx/PulseMedia/internal/store/mediasidecar"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/classification"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/seerr"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/subtitle"
)

func (a *App) configureReviewServices(handler *httpapi.ServerHandler, authentication *auth.Service, control *download.ControlService, inspection *download.SeedingInspectionService, masterKey []byte, metadata ports.MetadataProvider) {
	if a.dbPool == nil || len(masterKey) != 32 {
		return
	}
	cipher := crypto.NewSettingsCipher(masterKey)
	accounts := postgres.NewAccountManagementStore(a.dbPool, a.idgen)
	handler.SetAccountManagementService(auth.NewAccountManagementService(accounts, crypto.AuthenticationSecrets{}, a.clock, a.idgen, cipher))
	handler.SetMFAService(auth.NewMFAService(accounts, crypto.AuthenticationSecrets{}, cipher, a.clock, a.idgen))
	if authentication != nil {
		authentication.SetMFACipher(cipher)
	}
	handler.SetSubscriptionExecutionService(subscription.NewExecutionService(postgres.NewSubscriptionExecutionStore(a.dbPool)))
	handler.SetClassificationService(classification.NewServiceWithMetadata(postgres.NewClassificationStore(a.dbPool, a.idgen), metadata))
	protector := crypto.NewSecretProtector(masterKey)
	handler.SetSeerrService(seerr.NewService(postgres.NewSeerrStore(a.dbPool, a.idgen, a.clock, protector)))
	handler.SetSubtitleCandidatesService(subtitle.NewCandidateService(sidecarstore.NewCandidateRepository(a.dbPool, a.idgen, protector), rootfs.NewFilesystem(), shooter.NewClient("", a.metadataHTTPClient)))
	if control == nil || inspection == nil {
		repo := downloadstore.NewRepositoryWithDownloadRates(a.dbPool, a.idgen, masterKey)
		repo.SetIndexerConfigurationDecoder(a.indexerDecoder)
		repo.SetClientFactory(NativeDownloadClient)
		if control == nil {
			control = download.NewControlService(repo)
		}
		if inspection == nil {
			inspection = download.NewSeedingInspectionService(repo, a.clock)
		}
	}
	handler.SetDownloadControlService(control)
	handler.SetSeedingInspectionService(inspection)
}
