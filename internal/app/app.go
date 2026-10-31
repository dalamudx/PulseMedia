package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	cookiecloudadapter "github.com/dalamudx/PulseMedia/internal/adapter/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/universal_rss"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/bangumi"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/adapter/subtitle/shooter"
	"github.com/dalamudx/PulseMedia/internal/adapter/tmdbartwork"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	scrapestore "github.com/dalamudx/PulseMedia/internal/store/mediascrape"
	sidecarstore "github.com/dalamudx/PulseMedia/internal/store/mediasidecar"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	cookiecloudusecase "github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	seedingusecase "github.com/dalamudx/PulseMedia/internal/usecase/seeding"

	calendarusecase "github.com/dalamudx/PulseMedia/internal/usecase/calendar"
	"github.com/dalamudx/PulseMedia/internal/usecase/downloadmetrics"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	metadatausecase "github.com/dalamudx/PulseMedia/internal/usecase/metadata"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatadiagnostics"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/ranking"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	siteusecase "github.com/dalamudx/PulseMedia/internal/usecase/site"

	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/subtitle"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
)

// App is the composition root managing subsystem lifecycles.
type App struct {
	cfg                    *config.Config
	logger                 *slog.Logger
	clock                  ports.Clock
	idgen                  ports.IDGen
	indexerDecoder         ports.CredentialDecoder
	lock                   *lockfile.DataDirLock
	version                string
	dbPool                 *pgxpool.Pool
	stages                 workStageRecorder
	metadataHTTPClient     *http.Client
	notificationHTTPClient *http.Client
	sseHub                 *httpapi.SSEHub

	mu        sync.RWMutex
	ready     bool
	readyMsg  string
	checks    map[string]bool
	closing   chan struct{}
	runDone   chan struct{}
	runCancel context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

type Option func(*App)

func WithClock(c ports.Clock) Option {
	return func(a *App) {
		a.clock = c
	}
}

func WithIDGen(g ports.IDGen) Option {
	return func(a *App) {
		a.idgen = g
	}
}

func WithLogger(l *slog.Logger) Option {
	return func(a *App) {
		a.logger = l
	}
}

func WithVersion(v string) Option {
	return func(a *App) {
		a.version = v
	}
}

func WithDBPool(pool *pgxpool.Pool) Option {
	return func(a *App) {
		a.dbPool = pool
	}
}

func WithNotificationHTTPClient(client *http.Client) Option {
	return func(a *App) { a.notificationHTTPClient = client }
}

func WithMetadataHTTPClient(client *http.Client) Option {
	return func(a *App) { a.metadataHTTPClient = client }
}

// New creates and initializes an App, acquiring the data directory lock.
func New(cfg *config.Config, opts ...Option) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("app initialization rejected: %w", err)
	}

	// 1. Acquire local data directory lock to prevent split-brain dual-launch
	dl, err := lockfile.Acquire(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire data directory lock: %w", err)
	}

	a := &App{
		cfg:     cfg,
		lock:    dl,
		version: "0.1.0-dev",
		checks:  make(map[string]bool),
		closing: make(chan struct{}),
	}

	for _, opt := range opts {
		opt(a)
	}

	if a.clock == nil {
		a.clock = clock.New()
	}
	if a.idgen == nil {
		a.idgen = idgen.New()
	}
	if a.sseHub == nil {
		a.sseHub = httpapi.NewSSEHub()
	}
	if a.logger == nil {
		a.logger = logging.NewLogger(os.Stdout, cfg.LogLevel)
	}
	// Always mirror accepted records into the live log hub, whichever logger the
	// caller supplied. The sink receives an already redacted single line.
	a.logger = logging.WithSink(a.logger, func(level slog.Level, line string) {
		a.sseHub.PublishLog(level, line)
	})

	// Base readiness check for data directory
	a.checks["datadir"] = true
	a.ready = true

	// Optional database pool initialization if URL is provided
	if a.dbPool == nil && cfg.Database.URL != "" {
		pool, err := postgres.NewPool(context.Background(), cfg.Database.URL, postgres.DefaultPoolConfig())
		if err != nil {
			_ = dl.Release()
			return nil, fmt.Errorf("failed to connect to database: %w", err)
		}
		a.dbPool = pool
	}

	a.logger.Info("application initialized",
		slog.String("data_dir", cfg.DataDir),
		slog.String("log_level", cfg.LogLevel),
		slog.String("version", a.version),
	)

	return a, nil
}

// Healthz returns the liveness status of the process.
func (a *App) Healthz() ports.HealthStatus {
	return ports.HealthStatus{
		Alive:   true,
		Version: a.version,
	}
}

// Readyz returns the readiness status and individual dependency checks.
func (a *App) Readyz(ctx context.Context) ports.ReadyStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()

	checksCopy := make(map[string]bool, len(a.checks))
	allOk := true
	for k, v := range a.checks {
		checksCopy[k] = v
		if !v {
			allOk = false
		}
	}

	readyMsg := a.readyMsg

	// Check database schema readiness if database is configured
	if a.dbPool != nil {
		if err := postgres.CheckSchemaVersion(ctx, a.dbPool, postgres.CurrentMigrationVersion); err != nil {
			checksCopy["database"] = false
			allOk = false
			if readyMsg == "" {
				readyMsg = fmt.Sprintf("database schema check failed: %v", err)
			}
		} else {
			checksCopy["database"] = true
		}
		if err := postgres.RequireRestoreReconciled(ctx, a.dbPool); err != nil {
			checksCopy["restore_reconciled"] = false
			allOk = false
			if readyMsg == "" {
				readyMsg = err.Error()
			}
		} else {
			checksCopy["restore_reconciled"] = true
		}
	}

	return ports.ReadyStatus{
		Ready:  a.ready && allOk,
		Reason: readyMsg,
		Checks: checksCopy,
	}
}

// SetCheck updates the state of a named dependency check.
func (a *App) SetCheck(name string, ok bool, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[name] = ok
	if !ok && reason != "" {
		a.readyMsg = reason
	} else if ok && a.readyMsg == reason {
		a.readyMsg = ""
	}
}

// Run executes the application runtime until the context is canceled.
func (a *App) Run(ctx context.Context) (runErr error) {
	a.mu.Lock()
	select {
	case <-a.closing:
		a.mu.Unlock()
		return errors.New("application is closed")
	default:
	}
	if a.runDone != nil {
		a.mu.Unlock()
		return errors.New("application runtime already started")
	}
	ctx, cancelRuntime := context.WithCancel(ctx)
	runDone := make(chan struct{})
	a.runCancel, a.runDone = cancelRuntime, runDone
	a.mu.Unlock()
	defer func() {
		cancelRuntime()
		a.requestClose()
		runErr = errors.Join(runErr, a.releaseResources())
		close(runDone)
	}()
	a.logger.Info("application runtime starting", slog.String("http_addr", a.cfg.Server.HTTPAddr))

	// Ensure master encryption key exists in data directory
	masterKey, err := crypto.EnsureMasterKey(a.cfg.DataDir, a.cfg.SecretKey)
	if err != nil {
		a.SetCheck("master_key", false, "master key unavailable")
		return fmt.Errorf("failed to initialize master key: %w", err)
	}
	a.SetCheck("master_key", true, "master key unavailable")

	a.indexerDecoder = crypto.NewSecretProtector(masterKey)
	var authSvc *auth.Service
	var settingsSvc *settings.Service
	var acqSvc *acquisition.Service
	var transferSvc *transfer.Service
	var subSvc *subscription.Service
	var polSvc *policy.Service
	var searchSvc *usecaseSearch.Service
	var interactiveSearchRepo ports.InteractiveSearchRepository
	var interactiveSearchSvc *usecaseSearch.InteractiveSearchService
	var automaticTVRounds ports.AutomaticTVRoundRepository
	var automaticTVFinalizer ports.AutomaticTVAcquisitionRepository
	var automaticTVSender ports.AutomaticTVSendJobExecutor
	var automaticTVRecovery ports.AutomaticTVSubmissionRecoveryRepository
	var automaticTVObservationRepo ports.AutomaticTVObservationRepository
	var automaticTVObserver automaticTVObservationExecutor
	var feedSvc *usecaseSearch.FeedService
	var healthSvc *usecaseSearch.HealthService
	var scheduler *usecaseScheduler.AutoFulfillScheduler
	var movieDiscoverySvc *usecaseSearch.MovieDiscoveryService
	var tvEpisodeDiscoverySvc *usecaseSearch.TVEpisodeDiscoveryService
	var dlMgr *download.Manager
	var downloadControlSvc *download.ControlService
	var tvEpisodeUpgradeSvc httpapi.TVEpisodeUpgradeApplication
	var seedingInspectionSvc *download.SeedingInspectionService
	var downloadWatchRepo ports.DownloadWatchRepository
	var downloadWatchObserver ports.DownloadWatchObserver
	var completeTVWatchRepo ports.DownloadWatchRepository
	var completeTVHintObserver ports.DownloadWatchObserver
	var automaticTVWatchRepo ports.DownloadWatchRepository
	var automaticTVHintObserver ports.DownloadWatchObserver
	var movieUpgradeWatchRepo ports.DownloadWatchRepository
	var movieUpgradeHintScheduler ports.MovieUpgradeTrackingHintScheduler
	var untrackedDiscoveryRepo *postgres.UntrackedDiscoveryStore
	var untrackedDiscoverySvc *library.UntrackedDiscoveryService
	var untrackedReviewRepo *postgres.UntrackedReviewStore
	var queue *jobs.Queue
	var completeTV ports.CompleteTVExecutor
	var upgradeObservationRuntime ports.MovieUpgradeObservationRuntime
	var upgradeObservationRepo ports.MovieUpgradeNativeJobObservationRepository
	var upgradeTrackingRuntime ports.MovieUpgradeTrackingRuntime
	var upgradeTrackingRepo ports.MovieUpgradeNativeJobTrackingRepository
	var upgradeSend ports.MovieUpgradeSendJobExecutor
	var upgradeOrganization *movieUpgradeOrganizationWorker
	var syncSvc *mediaserver.SyncService
	var ob *outbox.Outbox
	var untrackedOrganizationRepo *postgres.UntrackedOrganizationStore
	var untrackedOrganizationExecutor ports.UntrackedOrganizationExecutionRepository
	var notifySvc *notify.Dispatcher

	if a.dbPool != nil {
		if err := postgres.RequireRestoreReconciled(ctx, a.dbPool); err != nil {
			return err
		}
		if err := postgres.CheckSchemaVersion(ctx, a.dbPool, postgres.CurrentMigrationVersion); err != nil {
			return errors.New("database schema upgrade required")
		}
		if err := postgres.EncryptLegacyIndexerConfigurations(ctx, a.dbPool, crypto.NewSecretProtector(masterKey), usecaseSearch.ClassifyLegacyIndexerProtocol); err != nil {
			return errors.New("indexer configuration migration unavailable")
		}
		if err := ensureDefaultLibraryRoots(ctx, a.dbPool, a.idgen, a.cfg.DataDir); err != nil {
			a.SetCheck("library_roots", false, "library roots unavailable")
			return fmt.Errorf("failed to initialize library roots: %w", err)
		}
		a.SetCheck("library_roots", true, "library roots unavailable")
		authSvc = auth.NewService(postgres.NewAuthenticationStore(a.dbPool), crypto.AuthenticationSecrets{}, a.clock, a.idgen)
		settingsSvc = settings.NewService(postgres.NewSettingsStore(a.dbPool, a.idgen), crypto.NewSettingsCipher(masterKey))
		queue = jobs.NewQueue(a.dbPool, a.idgen, a.clock)
		ob = outbox.New(a.dbPool, a.idgen, a.clock)
		a.enableWebPush(ob)
		notifySvc = notify.NewDispatcher(postgres.NewNotificationChannelReader(a.dbPool), a.idgen, a.clock, ob, postgres.NewNotificationChannelStore(a.dbPool, a.idgen))
		notifySvc.SetClientFactory(crypto.NewSecretProtector(masterKey), func(kind string, c notify.ChannelConfig) (ports.Notifier, error) {
			return notificationClientWithHTTP(kind, c, a.notificationHTTPClient, a.clock)
		})
		acqSvc = acquisition.NewService(postgres.NewAcquisitionStore(a.dbPool, a.idgen, a.clock, acquisition.SelectMovieRound))
		acqSvc.SetRequirementRepository(postgres.NewDownloadSubmissionStore(a.dbPool, a.idgen))
		transferSvc = transfer.NewService(transferstore.NewRepositoryWithMinimumMediaBytes(a.dbPool, a.idgen, ob, rootfs.NewFilesystem(), a.cfg.Organization.MinMediaBytes, transfer.ExecutePreparedPlan))
		subSvcStore := postgres.NewSubscriptionIntentStore(a.dbPool, a.idgen)
		subSvc = subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(a.dbPool, a.idgen), subSvcStore)
		polSvc = policy.NewService(postgres.NewPolicyStore(a.dbPool, a.idgen))

		downloadRepo := downloadstore.NewRepositoryWithDownloadRates(a.dbPool, a.idgen, masterKey)
		downloadWatchRepo = postgres.NewDownloadWatchStore(a.dbPool)
		downloadWatchObserver = download.NewWatchObserver(downloadRepo)
		completeTVWatchRepo = postgres.NewCompleteTVWatchStore(a.dbPool, a.idgen, a.clock)
		completeTVHintObserver = download.NewCompleteTVHintObserver(postgres.NewCompleteTVTrackingRuntimeStore(a.dbPool, a.idgen))
		automaticTVWatchStore := postgres.NewAutomaticTVWatchStore(a.dbPool, a.idgen, a.clock)
		automaticTVWatchRepo = automaticTVWatchStore
		automaticTVHintObserver = download.NewAutomaticTVHintObserver(automaticTVWatchStore)
		downloadRepo.SetIndexerConfigurationDecoder(a.indexerDecoder)
		downloadRepo.SetCompleteTVTrackingHintFilesystem(rootfs.NewFilesystem())
		downloadRepo.SetTorrentSource(func(kind, baseURL, credential string) ports.IndexerClient {
			switch kind {
			case "rss":
				return universal_rss.NewFeedClient(baseURL, nil)
			case "torznab", "nexusphp":
				return newConfiguredIndexerClient(ports.IndexerProtocol(kind), baseURL, credential)
			default:
				return nil
			}
		}, torrent.Inspector{})
		completeSubmission := postgres.NewCompleteTVSubmissionStore(a.dbPool, a.idgen)
		downloadRepo.SetCompleteTVServices(completeSubmission, completeSubmission, torznab.CompleteTVFetcher{}, torrent.Inspector{})
		automaticSubmission := postgres.NewAutomaticTVSubmissionStore(a.dbPool, a.idgen, a.indexerDecoder)
		downloadRepo.SetAutomaticTVServices(automaticSubmission, torznab.NewAutomaticTVTorrentFetcher(nil), torrent.Inspector{})
		automaticTVSender, automaticTVRecovery = downloadRepo, automaticSubmission
		automaticTVObservationRepo = postgres.NewAutomaticTVObservationStoreWithDownloadRates(a.dbPool, a.idgen)
		automaticTVObserver = downloadRepo
		completeTV = downloadRepo
		dlMgr = download.NewManager(downloadRepo)
		dlMgr.SetSubmissionRepository(postgres.NewDownloadSubmissionStore(a.dbPool, a.idgen))
		dlMgr.SetClientFactory(NativeDownloadClient)
		downloadControlSvc = download.NewControlService(downloadRepo)
		tvUpgrade, tvUpgradeErr := a.tvEpisodeUpgradeService(downloadRepo)
		if tvUpgradeErr != nil {
			return fmt.Errorf("initialize TV episode upgrade: %w", tvUpgradeErr)
		}
		tvEpisodeUpgradeSvc = tvUpgrade
		seedingInspectionSvc = download.NewSeedingInspectionService(downloadRepo, a.clock)

		runtime, observer, observationErr := a.movieUpgradeObservationServices(downloadRepo)
		if observationErr != nil {
			return fmt.Errorf("initialize upgrade observation: %w", observationErr)
		}
		upgradeObservationRuntime, upgradeObservationRepo = runtime, observer
		trackingRuntime, tracker, trackingErr := a.movieUpgradeTrackingServices(downloadRepo)
		if trackingErr != nil {
			return fmt.Errorf("initialize upgrade tracking: %w", trackingErr)
		}
		upgradeTrackingRuntime, upgradeTrackingRepo = trackingRuntime, tracker
		upgradeWatchStore, watchErr := postgres.NewMovieUpgradeWatchStore(trackingRuntime, a.clock)
		if watchErr != nil {
			return fmt.Errorf("initialize upgrade watch: %w", watchErr)
		}
		movieUpgradeWatchRepo, movieUpgradeHintScheduler = upgradeWatchStore, upgradeWatchStore
		untrackedDiscoveryRepo = postgres.NewUntrackedDiscoveryStore(a.dbPool, a.idgen, a.clock)
		untrackedDiscoverySvc = library.NewUntrackedDiscoveryService(untrackedDiscoveryRepo, rootfs.NewFilesystem())
		untrackedReviewRepo = postgres.NewUntrackedReviewStore(a.dbPool, a.idgen, a.clock)
		untrackedOrganizationRepo = postgres.NewUntrackedOrganizationStore(a.dbPool, a.idgen, a.clock)
		untrackedOrganizationExecutor = transferstore.NewUntrackedOrganizationExecutorWithMinimumMediaBytes(a.dbPool, a.idgen, ob, rootfs.NewFilesystem(), a.cfg.Organization.MinMediaBytes)
		upgradeSend, err = a.movieUpgradeSendServices(downloadRepo)
		if err != nil {
			return fmt.Errorf("initialize upgrade send: %w", err)
		}
		upgradeOrganization, err = a.movieUpgradeOrganizationServices()
		if err != nil {
			return fmt.Errorf("initialize upgrade organization: %w", err)
		}

		syncSvc = mediaserver.NewSyncService(ob, postgres.NewMediaConfirmationRepository(a.dbPool, a.idgen))
		syncSvc.SetCredentialDecoder(crypto.NewSecretProtector(masterKey))
		syncSvc.SetServerFactory(NativeMediaServerClient)

		searchSvc = usecaseSearch.NewService(postgres.NewSearchExecutionStore(a.dbPool, a.indexerDecoder), a.idgen, crypto.NewSecretProtector(masterKey))
		searchSvc.SetEndpointRepository(postgres.NewSearchEndpointStore(a.dbPool))
		automaticTVRounds = postgres.NewAutomaticTVRoundStore(a.dbPool, a.idgen)
		if a.cfg.DownloadRouting.IsEnabled() {
			automaticTVRounds = postgres.NewAutomaticTVRoundStoreWithRouting(a.dbPool, a.idgen, a.cfg.DownloadRouting.RulesForSelection())
		}
		automaticTVFinalizer = postgres.NewAutomaticTVAcquisitionStore(a.dbPool, a.idgen, acquisition.SelectAutomaticTVEpisode)
		searchSvc.SetAutomaticTVRoundRepository(automaticTVRounds)
		searchSvc.SetClientFactory(newIndexerClient)
		searchSvc.SetConfiguredClientFactory(newConfiguredIndexerClient)
		interactiveSearchRepo = postgres.NewInteractiveSearchStore(a.dbPool, a.idgen)
		interactiveSearchSvc = usecaseSearch.NewInteractiveSearchService(interactiveSearchRepo, crypto.NewSecretProtector(masterKey))
		movieDiscoverySvc = usecaseSearch.NewMovieDiscoveryService(postgres.NewMovieDiscoveryStore(a.dbPool, a.idgen))
		tvEpisodeDiscoverySvc = usecaseSearch.NewTVEpisodeDiscoveryService(postgres.NewTVEpisodeDiscoveryStore(a.dbPool, a.idgen))
		searchSvc.SetFeedClientFactory(func(kind, baseURL, apiKey string) ports.IndexerClient {
			if kind == "rss" {
				return universal_rss.NewFeedClient(baseURL, nil)
			}
			return torznab.NewClient(baseURL, apiKey, nil)
		})
		feedSvc = usecaseSearch.NewFeedService(postgres.NewRSSStore(a.dbPool, a.idgen), crypto.NewSecretProtector(masterKey), searchSvc)
		healthStore := postgres.NewIndexerHealthStore(a.dbPool, a.idgen)
		searchSvc.SetHealthRepository(healthStore)
		healthSvc = usecaseSearch.NewHealthService(healthStore, searchSvc)
		evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(a.dbPool))
		movieRounds := postgres.NewSearchRoundStore(a.dbPool, a.idgen)
		if a.cfg.DownloadRouting.IsEnabled() {
			movieRounds = postgres.NewSearchRoundStoreWithRouting(a.dbPool, a.idgen, a.cfg.DownloadRouting.RulesForSelection())
		}
		coord := usecaseSearch.NewRoundCoordinator(movieRounds, a.clock, acqSvc)
		automaticCoord := usecaseSearch.NewAutomaticRoundCoordinator(coord, automaticTVRounds)
		scheduler = usecaseScheduler.NewAutoFulfillScheduler(postgres.NewEvaluationScheduleStore(a.dbPool), evaluator, automaticCoord, polSvc)
	}

	metaProvider := &dynamicTMDBProvider{settingsSvc: settingsSvc, httpClient: a.metadataHTTPClient, budget: budget.NewMetadataBudget(a.clock)}
	var untrackedReviewRunner *library.UntrackedReviewRunner
	if untrackedReviewRepo != nil {
		untrackedReviewRunner = library.NewUntrackedReviewRunner(untrackedReviewRepo, rootfs.NewFilesystem(), metaProvider, metaProvider)
	}
	var sidecarSvc *metadatasidecar.Service
	var subtitleSvc *subtitle.Service
	var doubanWishRepo ports.DoubanWishSyncRepository
	var doubanWishSvc *watchlist.SyncService
	var personFollowRepo ports.PersonFollowSyncRepository
	var personFollowSvc *personfollow.SyncService
	var statsRepo ports.SiteAccountStatsRepository
	var siteStatsSvc mediaSidecarExecutor
	var checkinRepo ports.SiteCheckinRepository
	var siteCheckinSvc mediaSidecarExecutor
	var cookieCloudRepo ports.CookieCloudSyncRepository
	var cookieCloudSvc mediaSidecarExecutor
	var seedingRepo seedingScheduler
	var seedingSvc mediaSidecarExecutor

	var globalRatesRepo ports.DownloadGlobalRatesRepository
	var globalRatesSvc mediaSidecarExecutor
	if a.dbPool != nil {
		statsRepo = postgres.NewSiteAccountStatsStore(a.dbPool, a.idgen, a.indexerDecoder)
		checkinRepo = postgres.NewSiteCheckinStore(a.dbPool, a.idgen, a.indexerDecoder)
		cookieCloudRepo = postgres.NewCookieCloudSyncStore(a.dbPool, a.idgen, a.indexerDecoder, crypto.NewSecretProtector(masterKey), postgres.NewScheduleLimits(a.clock))
	}
	if queue != nil && len(masterKey) > 0 {
		siteStatsSvc = siteusecase.NewAccountStatsCollector(statsRepo, &siteAccountStatsClientBuilder{httpClient: a.metadataHTTPClient, clock: a.clock})
		siteCheckinSvc = siteusecase.NewCheckinRunner(checkinRepo, &siteCheckinClientBuilder{httpClient: a.metadataHTTPClient, clock: a.clock})
		cookieCloudSvc = cookiecloudusecase.NewRunner(cookieCloudRepo, cookiecloudadapter.NewClient(a.metadataHTTPClient))
		seedingRepo = postgres.NewSeedingScheduleStore(a.dbPool, a.idgen)
		seedingSvc = seedingusecase.NewRunner(postgres.NewSeedingExecutionStore(a.dbPool, a.idgen), &seedingClientFactory{sources: postgres.NewSeedingClientSourceStore(a.dbPool, a.indexerDecoder)})
	}
	if queue != nil && len(masterKey) > 0 && a.cfg.DownloadGlobalRates.Enabled {
		globalRatesRepo = postgres.NewDownloadGlobalRatesStore(a.dbPool, a.idgen, a.indexerDecoder)
		globalRatesSvc = downloadmetrics.NewService(globalRatesRepo, downloadGlobalRatesBuilder{}, a.clock)
	}
	if queue != nil && len(masterKey) > 0 {
		doubanWishRepo = postgres.NewDoubanWishSyncStore(a.dbPool, a.idgen, postgres.NewScheduleLimits(a.clock))
		creator := &doubanWishSubscriptionCreator{pool: a.dbPool, ids: a.idgen, provider: metaProvider, episodes: metaProvider, protector: crypto.NewSecretProtector(masterKey)}
		doubanWishSvc = watchlist.NewSyncService(doubanWishRepo, douban.NewWishClient("", a.metadataHTTPClient), metaProvider, creator)
		personFollowRepo = postgres.NewPersonFollowSyncStore(a.dbPool, a.idgen, postgres.NewScheduleLimits(a.clock))
		personCreator := &personFollowSubscriptionCreator{pool: a.dbPool, ids: a.idgen, provider: metaProvider, episodes: metaProvider, protector: crypto.NewSecretProtector(masterKey)}
		personFollowSvc = personfollow.NewSyncService(personFollowRepo, metaProvider, personCreator)
	}
	if queue != nil {
		sidecarSvc = metadatasidecar.NewService(sidecarstore.NewRepository(a.dbPool, a.idgen), rootfs.NewFilesystem(), metaProvider, tmdbartwork.New(a.metadataHTTPClient, metaProvider.budget), metadatasidecar.WithArtworkSource(artworkSource{settings: settingsSvc, httpClient: a.metadataHTTPClient}, metaProvider))
		subtitleSvc = subtitle.NewService(sidecarstore.NewSubtitleRepository(a.dbPool, a.idgen), rootfs.NewFilesystem(), shooter.NewClient("", a.metadataHTTPClient))
	}
	sseHub := a.sseHub

	var reuseSvc *transfer.LibraryReuseService
	tvdbFallback := tvdbSource{settings: settingsSvc, httpClient: a.metadataHTTPClient, identity: metaProvider, resolver: metaProvider}
	browserMetadata := &tvdbBrowseProvider{MetadataProvider: metaProvider, translations: metaProvider, source: tvdbFallback}
	serverHandler := httpapi.NewServerHandler(a, authSvc, settingsSvc, acqSvc, transferSvc, browserMetadata, subSvc, polSvc, searchSvc, scheduler, dlMgr, syncSvc, masterKey)
	a.configureReviewServices(serverHandler, authSvc, downloadControlSvc, seedingInspectionSvc, masterKey, metaProvider)
	serverHandler.SetMovieDiscoveryService(movieDiscoverySvc)
	serverHandler.SetTVEpisodeDiscoveryService(tvEpisodeDiscoverySvc)
	serverHandler.SetTVEpisodeUpgradeService(tvEpisodeUpgradeSvc)
	serverHandler.SetMetadataDiagnosticsService(metadatadiagnostics.NewService(newMetadataDiagnosticCatalogue(metaProvider)))
	serverHandler.SetUntrackedCompanionsService(library.NewUntrackedCompanions(postgres.NewUntrackedCompanionStore(a.dbPool, a.idgen, a.clock), rootfs.NewFilesystem()))
	serverHandler.SetDoubanSyncConfigService(watchlist.NewConfigService(postgres.NewDoubanSyncConfigStore(a.dbPool, a.idgen)))
	serverHandler.SetSiteAccountStatsConfigService(siteusecase.NewAccountStatsConfigService(postgres.NewSiteAccountStatsConfigStore(a.dbPool, a.idgen, a.indexerDecoder)))
	serverHandler.SetSiteCheckinConfigService(siteusecase.NewCheckinConfigService(postgres.NewSiteCheckinConfigStore(a.dbPool, a.idgen, a.indexerDecoder)))
	serverHandler.SetCookieCloudConfigService(cookiecloudusecase.NewConfigService(postgres.NewCookieCloudConfigStore(a.dbPool, a.idgen), crypto.NewSecretProtector(masterKey)))
	serverHandler.SetSeedingConfigService(seedingusecase.NewConfigService(postgres.NewSeedingConfigStore(a.dbPool, a.idgen)))
	serverHandler.SetUntrackedScopeService(library.NewUntrackedScopeService(postgres.NewUntrackedScopeStore(a.dbPool, a.idgen, a.clock), rootfs.NewFilesystem()))
	serverHandler.SetUntrackedDiscoveryService(library.NewUntrackedDiscoveryReader(untrackedDiscoveryRepo))
	untrackedReviews := library.NewUntrackedReviewService(untrackedReviewRepo, rootfs.NewFilesystem())
	untrackedOrganizations := library.NewUntrackedOrganizationService(untrackedOrganizationRepo, rootfs.NewFilesystem())
	serverHandler.SetUntrackedReviewService(untrackedReviews)
	serverHandler.SetUntrackedOrganizationService(untrackedOrganizations)
	if a.dbPool != nil {
		serverHandler.SetExternalBatchService(library.NewExternalBatchService(postgres.NewExternalBatchStore(a.dbPool, a.idgen, a.clock), untrackedReviews, untrackedOrganizations))
	}
	serverHandler.SetSiteCheckinHistoryService(siteusecase.NewCheckinHistoryReader(postgres.NewSiteCheckinHistoryStore(a.dbPool)))
	serverHandler.SetSiteAccountStatsService(siteusecase.NewAccountStatsReader(statsRepo))
	serverHandler.SetMetadataDetailService(metadatausecase.NewDetailService(metaProvider))
	serverHandler.SetDoubanRankingService(ranking.NewService(douban.NewClient("", a.metadataHTTPClient, douban.WithCredentialSource(doubanCredentials(settingsSvc)), douban.WithClock(a.clock)), metaProvider, a.clock))
	serverHandler.SetBangumiCalendarService(calendarusecase.NewService(bangumi.NewClient("", a.metadataHTTPClient), a.clock))
	serverHandler.SetTokenManagementService(auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.dbPool, a.idgen), crypto.ReadTokenIssuer{}, a.clock))
	serverHandler.SetNotificationDispatcher(notifySvc)
	serverHandler.SetNotificationTestService(notify.NewTestService(postgres.NewNotificationTestStore(a.dbPool, a.idgen, ob)))
	serverHandler.SetWebPushService(notify.NewWebPushService(postgres.NewWebPushStore(a.dbPool, a.idgen, ob), crypto.NewSecretProtector(masterKey), a.cfg.WebPush.PublicKey, a.cfg.WebPush.Enabled))
	serverHandler.SetPersonFollowService(personfollow.NewConfigService(postgres.NewPersonFollowStore(a.dbPool, a.idgen, ob)))
	serverHandler.SetFileMappingService(mediausecase.NewFileMappingService(postgres.NewFileMappingStore(a.dbPool, a.idgen)))
	serverHandler.SetDeliveryManagementService(notify.NewDeliveryManagementService(postgres.NewDeliveryManagementStore(a.dbPool, a.idgen)))
	serverHandler.SetChannelStateService(notify.NewChannelStateService(postgres.NewChannelStateStore(a.dbPool, a.idgen)))
	serverHandler.SetChannelReplacementService(notify.NewChannelReplacementService(postgres.NewChannelReplacementStore(a.dbPool, a.idgen), crypto.NewSecretProtector(masterKey)))
	serverHandler.SetBatchQueryService(notify.NewBatchQueryService(postgres.NewNotificationBatchQueryStore(a.dbPool)))
	serverHandler.SetDeliverySkipService(notify.NewDeliverySkipService(postgres.NewDeliveryManagementStore(a.dbPool, a.idgen)))
	serverHandler.SetFeedService(feedSvc)
	serverHandler.SetInteractiveSearchService(interactiveSearchSvc)
	if a.dbPool != nil {
		indexerManagementStore := postgres.NewIndexerManagementStore(a.dbPool, a.idgen, a.indexerDecoder)
		serverHandler.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerManagementStore, crypto.NewSecretProtector(masterKey)), usecaseSearch.NewIndexerManagementReceiptService(indexerManagementStore))
		serverHandler.SetWithdrawalService(acquisition.NewWithdrawalService(postgres.NewDownloadSubmissionStore(a.dbPool, a.idgen)))
		serverHandler.SetTransferRecoveryService(transfer.NewRecoveryService(transferstore.NewRepositoryWithMinimumMediaBytes(a.dbPool, a.idgen, ob, rootfs.NewFilesystem(), a.cfg.Organization.MinMediaBytes, transfer.ExecutePreparedPlan)))
		serverHandler.SetCompleteTVOrganizationPreviewService(transfer.NewCompleteTVOrganizationPreviewService(transferstore.NewRepositoryWithMinimumMediaBytes(a.dbPool, a.idgen, nil, nil, a.cfg.Organization.MinMediaBytes, transfer.ExecutePreparedPlan)))
		serverHandler.SetTransferRevisionService(transfer.NewRevisionService(transferstore.NewRepositoryWithMinimumMediaBytes(a.dbPool, a.idgen, ob, rootfs.NewFilesystem(), a.cfg.Organization.MinMediaBytes, transfer.ExecutePreparedPlan)))
		reuseSvc = transfer.NewLibraryReuseService(transferstore.NewRepositoryWithMinimumMediaBytes(a.dbPool, a.idgen, ob, rootfs.NewFilesystem(), a.cfg.Organization.MinMediaBytes, transfer.ExecutePreparedPlan))
		serverHandler.SetLibraryReuseService(reuseSvc)
		serverHandler.SetMediaScrapeService(mediascrape.NewService(scrapestore.NewRepository(a.dbPool), a.idgen))
		serverHandler.SetManagedSidecarUpdateService(metadatasidecar.NewManagedUpdateService(sidecarstore.NewManagedUpdateRepository(a.dbPool, a.idgen), rootfs.NewFilesystem(), metaProvider, tmdbartwork.New(a.metadataHTTPClient, metaProvider.budget), a.idgen))
		if err := a.configureLibraryMaintenance(serverHandler); err != nil {
			return err
		}
		serverHandler.SetAuditTimelineService(audit.NewTimelineService(postgres.NewAuditTimelineStore(a.dbPool)))
		serverHandler.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(a.dbPool)))
		serverHandler.SetSubscriptionCreationService(subscription.NewSubscriptionCreationService(postgres.NewSubscriptionCreationStore(a.dbPool, a.idgen), metaProvider, metaProvider, crypto.NewSecretProtector(masterKey)))
		serverHandler.SetMediaRequestService(subscription.NewMediaRequestService(postgres.NewMediaRequestStore(a.dbPool, a.idgen, a.clock), func(fence ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
			return subscription.NewSubscriptionCreationService(postgres.NewMediaRequestApprovalCreationStore(a.dbPool, a.idgen, fence), metaProvider, metaProvider, crypto.NewSecretProtector(masterKey))
		}))
		serverHandler.SetCandidateExplanationService(usecaseSearch.NewCandidateExplanationService(postgres.NewCandidateExplanationStore(a.dbPool)))
		serverHandler.SetControlService(subscription.NewControlService(postgres.NewSubscriptionControlStore(a.dbPool, a.idgen)))
		serverHandler.SetTVRefreshService(subscription.NewTVRefreshServiceWithSupplements(postgres.NewTVRefreshStore(a.dbPool, a.idgen), metaProvider, tvdbFallback))
		sourceRepo := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(a.dbPool, a.idgen))
		sourceService := subscription.NewEpisodeSourceService(sourceRepo, metaProvider)
		serverHandler.SetEpisodeSourceService(sourceService)
		serverHandler.SetEpisodeMappingService(subscription.NewEpisodeMappingService(postgres.NewEpisodeMappingStore(sourceRepo, a.idgen), sourceService))
		serverHandler.SetEpisodeCandidateReviewService(subscription.NewEpisodeCandidateReviewService(postgres.NewEpisodeMappingStore(sourceRepo, a.idgen)))
		serverHandler.SetTVCoverageCandidateReviewService(subscription.NewTVCoverageCandidateReviewService(postgres.NewTVCoverageCandidateReviewStore(postgres.NewEpisodeMappingStore(sourceRepo, a.idgen))))
		serverHandler.SetCompleteTVAcquisitionService(acquisition.NewCompleteTVService(postgres.NewCompleteTVAcquisitionStore(a.dbPool, a.idgen)))
		serverHandler.SetTVSelectionService(acquisition.NewTVSelectionService(postgres.NewTVSelectionStore(postgres.NewEpisodeMappingStore(sourceRepo, a.idgen))))
		serverHandler.SetUpgradePreviewService(acquisition.NewUpgradePreviewService(postgres.NewUpgradePreviewStore(a.dbPool, a.indexerDecoder)))
		serverHandler.SetUpgradeReservationService(acquisition.NewUpgradeReservationService(postgres.NewUpgradeReservationStore(a.dbPool, a.idgen, a.indexerDecoder)))
		upgradePreparation, err := a.publicUpgradePreparationService(masterKey)
		if err != nil {
			return fmt.Errorf("create public upgrade preparation: %w", err)
		}
		serverHandler.SetPublicUpgradePreparationService(upgradePreparation)
		upgradeSend, err := a.publicUpgradeSendService()
		if err != nil {
			return fmt.Errorf("create public upgrade send: %w", err)
		}
		serverHandler.SetPublicUpgradeSendService(upgradeSend)
		upgradeOrganization, err := a.publicUpgradeOrganizationService()
		if err != nil {
			return fmt.Errorf("create public upgrade organization: %w", err)
		}
		serverHandler.SetPublicUpgradeOrganizationService(upgradeOrganization)
		upgradeRetirement, err := a.publicUpgradeRetirementService()
		if err != nil {
			return fmt.Errorf("create public upgrade retirement: %w", err)
		}
		serverHandler.SetPublicUpgradeRetirementService(upgradeRetirement)
		serverHandler.SetEpisodeAcquisitionService(acquisition.NewEpisodeService(postgres.NewEpisodeAcquisitionStore(a.dbPool, a.idgen)))
		serverHandler.SetMovieSelectionService(acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(a.dbPool, a.idgen, a.clock, acquisition.SelectMovieRound), metaProvider, a.clock))
		serverHandler.SetSubscriptionBindingService(subscription.NewBindingService(postgres.NewSubscriptionBindingStore(a.dbPool, a.idgen)))
		serverHandler.SetSubscriptionStatusService(subscription.NewStatusService(postgres.NewSubscriptionStatusStore(a.dbPool)))
	}
	router := httpapi.NewRouter(a, serverHandler, authSvc, sseHub)
	httpOwner := &runtimeHTTP{handler: router}

	srv := &http.Server{
		Addr:         a.cfg.Server.HTTPAddr,
		Handler:      httpOwner,
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
	}

	// Own the worker until shutdown, including in-flight physical downloader I/O.
	workerCtx, cancelWorker := context.WithCancel(ctx)
	organizationWake := make(chan struct{}, 1)
	upgradeTrackingWake := make(chan struct{}, 1)
	untrackedDiscoveryWake := make(chan struct{}, 1)
	untrackedDiscoveryDone, untrackedDiscoveryWatchDone := make(chan struct{}), make(chan struct{})
	if queue != nil && untrackedDiscoveryRepo != nil && untrackedDiscoverySvc != nil {
		go func() {
			defer close(untrackedDiscoveryDone)
			a.runUntrackedDiscoveryWorker(workerCtx, queue, untrackedDiscoveryRepo, untrackedDiscoverySvc, untrackedDiscoveryWake)
		}()
		go func() {
			defer close(untrackedDiscoveryWatchDone)
			a.runDownloadWatchWorker(workerCtx, &untrackedDiscoveryWatch{scheduler: untrackedDiscoveryRepo}, &untrackedDiscoveryHintObserver{scheduler: untrackedDiscoveryRepo, wake: untrackedDiscoveryWake}, rootfs.NewFilesystem(), nil)
		}()
	} else {
		close(untrackedDiscoveryDone)
		close(untrackedDiscoveryWatchDone)
	}
	untrackedReviewDone := make(chan struct{})
	if queue != nil && untrackedReviewRepo != nil && untrackedReviewRunner != nil {
		go func() {
			defer close(untrackedReviewDone)
			a.runUntrackedReviewWorker(workerCtx, queue, untrackedReviewRepo, untrackedReviewRunner)
		}()
	} else {
		close(untrackedReviewDone)
	}
	untrackedOrganizationDone := make(chan struct{})
	if queue != nil && untrackedOrganizationExecutor != nil {
		go func() {
			defer close(untrackedOrganizationDone)
			a.runUntrackedOrganizationWorker(workerCtx, queue, untrackedOrganizationExecutor)
		}()
	} else {
		close(untrackedOrganizationDone)
	}
	movieUpgradeWatchDone := make(chan struct{})
	if queue != nil && movieUpgradeWatchRepo != nil && movieUpgradeHintScheduler != nil {
		upgradeHintObserver := download.NewMovieUpgradeHintObserver(movieUpgradeHintScheduler, rootfs.NewFilesystem(), upgradeTrackingWake)
		go func() {
			defer close(movieUpgradeWatchDone)
			a.runDownloadWatch(workerCtx, movieUpgradeWatchRepo, upgradeHintObserver, rootfs.NewFilesystem(), nil, downloadWatchOptions{observationTimeout: 35 * time.Second})
		}()
	} else {
		close(movieUpgradeWatchDone)
	}
	automaticTVWatchDone := make(chan struct{})
	if queue != nil && automaticTVWatchRepo != nil && automaticTVHintObserver != nil {
		go func() {
			defer close(automaticTVWatchDone)
			a.runDownloadWatchWorker(workerCtx, automaticTVWatchRepo, automaticTVHintObserver, rootfs.NewFilesystem(), nil)
		}()
	} else {
		close(automaticTVWatchDone)
	}
	completeTVTrackingWake := make(chan struct{}, 1)
	completeTVWatchDone := make(chan struct{})
	if queue != nil && completeTVWatchRepo != nil && completeTVHintObserver != nil {
		go func() {
			defer close(completeTVWatchDone)
			a.runDownloadWatchWorker(workerCtx, completeTVWatchRepo, completeTVHintWakeObserver{delegate: completeTVHintObserver, wake: completeTVTrackingWake}, rootfs.NewFilesystem(), nil)
		}()
	} else {
		close(completeTVWatchDone)
	}
	downloadWatchDone := make(chan struct{})
	if queue != nil && downloadWatchRepo != nil && downloadWatchObserver != nil {
		go func() {
			defer close(downloadWatchDone)
			a.runDownloadWatchWorker(workerCtx, downloadWatchRepo, downloadWatchObserver, rootfs.NewFilesystem(), organizationWake)
		}()
	} else {
		close(downloadWatchDone)
	}
	workerDone := make(chan struct{})
	completeTVDone := make(chan struct{})
	automaticTVDownloadDone := make(chan struct{})
	upgradeObservationDone := make(chan struct{})
	upgradeTrackingDone := make(chan struct{})
	upgradeSendDone := make(chan struct{})
	upgradeOrganizationDone := make(chan struct{})
	organizationDone := make(chan struct{})
	mediaSidecarDone := make(chan struct{})
	mediaSubtitleDone := make(chan struct{})
	doubanWishDone := make(chan struct{})
	personFollowDone := make(chan struct{})
	historyRetentionDone := make(chan struct{})
	if a.dbPool != nil {
		go func() {
			defer close(historyRetentionDone)
			a.runHistoryRetentionWorker(workerCtx, postgres.NewHistoryRetentionStore(a.dbPool))
		}()
	} else {
		close(historyRetentionDone)
	}
	globalRatesDone := make(chan struct{})
	if globalRatesSvc != nil {
		go func() {
			defer close(globalRatesDone)
			a.runDownloadGlobalRatesWorker(workerCtx, queue, globalRatesRepo, globalRatesSvc)
		}()
	} else {
		close(globalRatesDone)
	}
	siteStatsDone := make(chan struct{})
	if siteStatsSvc != nil {
		go func() {
			defer close(siteStatsDone)
			a.runSiteAccountStatsWorker(workerCtx, queue, statsRepo, siteStatsSvc)
		}()
	} else {
		close(siteStatsDone)
	}
	siteCheckinDone := make(chan struct{})
	if siteCheckinSvc != nil {
		go func() {
			defer close(siteCheckinDone)
			a.runSiteCheckinWorker(workerCtx, queue, checkinRepo, siteCheckinSvc)
		}()
	} else {
		close(siteCheckinDone)
	}
	seedingDone := make(chan struct{})
	if seedingSvc != nil {
		go func() { defer close(seedingDone); a.runSeedingWorker(workerCtx, queue, seedingRepo, seedingSvc) }()
	} else {
		close(seedingDone)
	}
	cookieCloudDone := make(chan struct{})
	if cookieCloudSvc != nil {
		go func() {
			defer close(cookieCloudDone)
			a.runCookieCloudWorker(workerCtx, queue, cookieCloudRepo, cookieCloudSvc)
		}()
	} else {
		close(cookieCloudDone)
	}
	if doubanWishSvc != nil {
		go func() {
			defer close(doubanWishDone)
			a.runDoubanWishWorker(workerCtx, queue, doubanWishRepo, doubanWishSvc)
		}()
	} else {
		close(doubanWishDone)
	}
	if personFollowSvc != nil {
		go func() {
			defer close(personFollowDone)
			a.runPersonFollowWorker(workerCtx, queue, personFollowRepo, personFollowSvc)
		}()
	} else {
		close(personFollowDone)
	}
	outboxDone := make(chan struct{})
	searchDone := make(chan struct{})
	libraryDone := make(chan struct{})
	if queue != nil {
		go func() {
			defer close(automaticTVDownloadDone)
			a.runAutomaticTVDownloadWorkers(workerCtx, queue, automaticTVRecovery, automaticTVSender, automaticTVObservationRepo, automaticTVObserver, organizationWake)
		}()
		go func() {
			defer close(upgradeOrganizationDone)
			a.runMovieUpgradeOrganizationWorker(workerCtx, queue, upgradeOrganization)
		}()
		go func() {
			defer close(upgradeTrackingDone)
			a.runMovieUpgradeTrackingWorker(workerCtx, queue, upgradeTrackingRuntime, upgradeTrackingRepo, upgradeTrackingWake)
		}()
		go func() {
			defer close(upgradeSendDone)
			a.runMovieUpgradeSendWorker(workerCtx, queue, upgradeSend)
		}()
		go func() {
			defer close(upgradeObservationDone)
			a.runMovieUpgradeObservationWorker(workerCtx, queue, upgradeObservationRuntime, upgradeObservationRepo)
		}()
		go func() {
			defer close(completeTVDone)
			a.runCompleteTVWorker(workerCtx, queue, completeTV, postgres.NewCompleteTVObservationStore(a.dbPool, a.idgen), postgres.NewCompleteTVRuntimeStore(a.dbPool, a.idgen), postgres.NewCompleteTVTrackingStoreWithDownloadRates(a.dbPool, a.idgen), torrent.Inspector{}, postgres.NewCompleteTVTrackingRuntimeStore(a.dbPool, a.idgen), completeTVTrackingWake)
		}()
		go func() {
			defer close(workerDone)
			a.runDownloadWorker(workerCtx, queue, dlMgr)
		}()
		go func() {
			defer close(organizationDone)
			if downloadWatchRepo == nil {
				a.runOrganizationWorker(workerCtx, queue, transferSvc, reuseSvc)
			} else {
				a.runOrganizationWorkerWithWake(workerCtx, queue, transferSvc, organizationWake, reuseSvc)
			}
		}()
		go func() {
			defer close(mediaSidecarDone)
			a.runMediaSidecarWorker(workerCtx, queue, sidecarSvc)
		}()
		go func() {
			defer close(mediaSubtitleDone)
			a.runMediaSubtitleWorker(workerCtx, queue, subtitleSvc)
		}()
		go func() { defer close(outboxDone); a.runOutboxWorkers(workerCtx, ob, syncSvc, notifySvc) }()
		go func() {
			defer close(searchDone)
			a.runAllSearchWorkers(workerCtx, queue, scheduler, searchSvc, feedSvc, healthSvc, interactiveSearchRepo, automaticTVRounds, automaticTVFinalizer, movieDiscoverySvc)
		}()
		go func() { defer close(libraryDone); a.runLibraryWorkers(workerCtx, queue, metaProvider) }()
	} else {
		close(workerDone)
		close(completeTVDone)
		close(automaticTVDownloadDone)
		close(upgradeObservationDone)
		close(upgradeTrackingDone)
		close(upgradeSendDone)
		close(upgradeOrganizationDone)
		close(organizationDone)
		close(mediaSidecarDone)
		close(mediaSubtitleDone)
		close(outboxDone)
		close(searchDone)
		close(libraryDone)
	}
	stopWorker := func() {
		cancelWorker()
		budget, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		waitRuntime(budget, a.logger, workerDone, downloadWatchDone, completeTVWatchDone, automaticTVWatchDone, movieUpgradeWatchDone, untrackedDiscoveryDone, untrackedDiscoveryWatchDone, untrackedReviewDone, untrackedOrganizationDone, completeTVDone, automaticTVDownloadDone, upgradeObservationDone, upgradeTrackingDone, upgradeSendDone, upgradeOrganizationDone, organizationDone, mediaSidecarDone, mediaSubtitleDone, doubanWishDone, personFollowDone, globalRatesDone, historyRetentionDone, siteStatsDone, siteCheckinDone, cookieCloudDone, seedingDone, outboxDone, searchDone, libraryDone)
	}
	defer stopWorker()

	errChan := make(chan error, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		a.logger.Info("HTTP server listening", slog.String("addr", a.cfg.Server.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}

	}()

	var stopServerOnce sync.Once
	stopServer := func() {
		stopServerOnce.Do(func() {
			sseHub.Close()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = httpOwner.shutdown(shutdownCtx, srv, serverDone, a.logger)
		})
	}
	defer stopServer()
	select {
	case err := <-errChan:
		a.requestClose()
		stopServer()
		stopWorker()
		return fmt.Errorf("http server failure: %w", err)
	case <-ctx.Done():
		a.requestClose()
		a.logger.Info("shutdown signal received, commencing graceful shutdown")
		stopServer()
		stopWorker()
		return nil
	case <-a.closing:
		stopServer()
		stopWorker()
		return nil
	}
}

// requestClose marks the app unready and cancels work while Run retains resources.
func (a *App) requestClose() <-chan struct{} {
	a.mu.Lock()
	select {
	case <-a.closing:
	default:
		close(a.closing)
	}
	a.ready = false
	a.readyMsg = "server shutting down"
	done, cancel := a.runDone, a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return done
}

// Close waits for the owned runtime before releasing resources exactly once.
func (a *App) Close() error {
	if done := a.requestClose(); done != nil {
		<-done
	}
	return a.releaseResources()
}

func (a *App) releaseResources() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.dbPool != nil {
			a.dbPool.Close()
			a.dbPool = nil
		}
		if a.lock != nil {
			if err := a.lock.Release(); err != nil {
				a.closeErr = fmt.Errorf("error releasing datadir lock: %w", err)
			}
			a.lock = nil
		}
		a.logger.Info("application cleanly closed")
	})
	return a.closeErr
}

// Ports accessors
func (a *App) Clock() ports.Clock {
	return a.clock
}

func (a *App) IDGen() ports.IDGen {
	return a.idgen
}

func (a *App) Logger() *slog.Logger {
	return a.logger
}

func (a *App) Config() *config.Config {
	return a.cfg
}

func (a *App) DBPool() *pgxpool.Pool {
	return a.dbPool
}

func (a *App) Queries() *db.Queries {
	if a.dbPool == nil {
		return nil
	}
	return postgres.Queries(a.dbPool)
}
func ensureDefaultLibraryRoots(ctx context.Context, pool *pgxpool.Pool, gen ports.IDGen, dataDir string) error {
	return library.EnsureDefaultRoots(ctx, postgres.NewLibraryBootstrapStore(pool), rootfs.NewFilesystem(), gen, dataDir)
}

// doubanCredentials reads the Douban API key and signing secret per request:
// environment first, then the encrypted settings. Neither value is shipped
// with the repository, and an unconfigured pair leaves the rankings unavailable.
func doubanCredentials(settingsSvc *settings.Service) douban.CredentialSource {
	return func(ctx context.Context) douban.Credentials {
		creds := douban.Credentials{APIKey: os.Getenv("PULSE_DOUBAN_API_KEY"), Secret: os.Getenv("PULSE_DOUBAN_API_SECRET")}
		if settingsSvc != nil {
			if creds.APIKey == "" {
				creds.APIKey, _ = settingsSvc.GetSecret(ctx, "douban.api_key")
			}
			if creds.Secret == "" {
				creds.Secret, _ = settingsSvc.GetSecret(ctx, "douban.api_secret")
			}
		}
		return creds
	}
}

type tmdbMetadataClient interface {
	ports.MetadataProvider
	ports.MovieIdentityProvider
}

type dynamicTMDBProvider struct {
	settingsSvc *settings.Service
	mu          sync.RWMutex
	client      tmdbMetadataClient
	currentKey  string
	budget      ports.MetadataRequestBudget
	httpClient  *http.Client
}

func (p *dynamicTMDBProvider) getClient(ctx context.Context) tmdbMetadataClient {
	p.mu.Lock()
	defer p.mu.Unlock()

	apiKey := os.Getenv("TMDB_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("PULSE_TMDB_API_KEY")
	}
	if apiKey == "" && p.settingsSvc != nil {
		apiKey, _ = p.settingsSvc.GetSecret(ctx, "tmdb.api_key")
	}

	if p.client == nil || apiKey != p.currentKey {
		p.currentKey = apiKey
		options := []tmdb.Option{tmdb.WithRequestBudget(p.budget)}
		if p.httpClient != nil {
			options = append(options, tmdb.WithHTTPClient(p.httpClient))
		}
		p.client = tmdb.NewClient(apiKey, options...)
	}
	return p.client
}

func (p *dynamicTMDBProvider) SearchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, error) {
	return p.getClient(ctx).SearchMovies(ctx, query, year)
}

func (p *dynamicTMDBProvider) SearchMovieIdentityCandidates(ctx context.Context, query string, year int) (ports.MovieIdentitySearch, error) {
	return p.getClient(ctx).SearchMovieIdentityCandidates(ctx, query, year)
}

func (p *dynamicTMDBProvider) SearchMediaIdentityCandidates(ctx context.Context, query, mediaType string, year int) (ports.MovieIdentitySearch, error) {
	provider, ok := p.getClient(ctx).(ports.WatchlistIdentityMetadata)
	if !ok {
		return ports.MovieIdentitySearch{}, ports.ErrUnavailable
	}
	return provider.SearchMediaIdentityCandidates(ctx, query, mediaType, year)
}

func (p *dynamicTMDBProvider) GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	return p.getClient(ctx).GetMovieDetails(ctx, tmdbID, language)
}

func (p *dynamicTMDBProvider) FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*ports.MovieMetadata, error) {
	return p.getClient(ctx).FindMovieByExternalID(ctx, namespace, externalID)
}

func (p *dynamicTMDBProvider) SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	return p.getClient(ctx).SearchMedia(ctx, query, mediaType, year)
}

func (p *dynamicTMDBProvider) ListTVEpisodeGroups(ctx context.Context, id int64) ([]ports.TVEpisodeGroupSummary, error) {
	provider, ok := p.getClient(ctx).(ports.TVEpisodeGroupProvider)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.ListTVEpisodeGroups(ctx, id)
}
func (p *dynamicTMDBProvider) GetTVEpisodeGroup(ctx context.Context, id int64, group string) (*ports.TVEpisodeGroupSnapshot, error) {
	provider, ok := p.getClient(ctx).(ports.TVEpisodeGroupProvider)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.GetTVEpisodeGroup(ctx, id, group)
}
func (p *dynamicTMDBProvider) GetTVSeasonMetadata(ctx context.Context, id int64, season int, language string) (*ports.TVSeasonMetadata, error) {
	provider, ok := p.getClient(ctx).(ports.TVEpisodeProvider)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.GetTVSeasonMetadata(ctx, id, season, language)
}
func (p *dynamicTMDBProvider) GetTVDetails(ctx context.Context, tmdbID int64, language string) (*ports.TVDetails, error) {
	return p.getClient(ctx).GetTVDetails(ctx, tmdbID, language)
}

func (p *dynamicTMDBProvider) GetDiscoverFeed(ctx context.Context, category string, page int) ([]*ports.MovieMetadata, error) {
	return p.getClient(ctx).GetDiscoverFeed(ctx, category, page)
}

// newIndexerClient keeps ordinary submissions and searches on the same configured source.
func newIndexerClient(baseURL, apiKey string) ports.IndexerClient {
	if strings.Contains(baseURL, "{query}") || strings.Contains(baseURL, "{keyword}") ||
		strings.Contains(baseURL, "mikanani.me") || strings.Contains(baseURL, "bangumi.moe") ||
		strings.Contains(baseURL, "dmhy.org") || strings.Contains(baseURL, "nyaa.si") ||
		strings.Contains(baseURL, "acg.rip") || apiKey == "public" {
		return universal_rss.NewClient(baseURL, nil)
	}
	if strings.Contains(apiKey, ";") || strings.Contains(apiKey, "=") || strings.Contains(apiKey, "passkey") {
		return nexusphp.NewClient(baseURL, apiKey, "", nil)
	}
	return torznab.NewClient(baseURL, apiKey, nil)
}
