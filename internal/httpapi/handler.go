package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/dalamudx/PulseMedia/internal/config"
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/dalamudx/PulseMedia/internal/usecase/media_library"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/dalamudx/PulseMedia/internal/usecase/operation"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
)

var _ StrictServerInterface = (*ServerHandler)(nil)

// AppContext abstracts the application core context for the HTTP server.
type AppContext interface {
	Healthz() ports.HealthStatus
	Readyz(ctx context.Context) ports.ReadyStatus
	Config() *config.Config
	Clock() ports.Clock
	IDGen() ports.IDGen
	Logger() *slog.Logger
	DBPool() *pgxpool.Pool
	Queries() *db.Queries
}

// ServerHandler implements StrictServerInterface.
type ServerHandler struct {
	app                       AppContext
	operationSvc              *operation.Service
	operationTimelineSvc      *operation.TimelineService
	libraryCatalogSvc         *media_library.Service
	libraryOverviewSvc        *media_library.OverviewService
	dashboardActivitySvc      *operation.ActivityService
	submissionRetrySvc        *operation.SubmissionRetryService
	activityHub               *SSEHub
	logHub                    *SSEHub
	mediaScrapeSvc            *mediascrape.Service
	tokenManagementSvc        *auth.TokenManagementService
	authSvc                   *auth.Service
	settingsSvc               *settings.Service
	endpointConfigurationSvc  *settings.EndpointConfigurationService
	upgradePreviewSvc         *acquisition.UpgradePreviewService
	upgradeReservationSvc     *acquisition.UpgradeReservationService
	upgradePreparationSvc     *acquisition.PublicUpgradePreparationService
	upgradeSendSvc            *acquisition.PublicUpgradeSendService
	upgradeOrganizationSvc    *acquisition.PublicUpgradeOrganizationService
	upgradeRetirementSvc      *acquisition.PublicUpgradeRetirementService
	acqSvc                    *acquisition.Service
	movieSelectionSvc         *acquisition.MovieSelectionService
	withdrawSvc               *acquisition.WithdrawalService
	transferSvc               *transfer.Service
	recoverySvc               *transfer.RecoveryService
	revisionSvc               *transfer.RevisionService
	reuseSvc                  *transfer.LibraryReuseService
	metaProvider              ports.MetadataProvider
	metadataDetailSvc         metadataDetailApplication
	doubanRankingSvc          doubanRankingApplication
	doubanSyncConfigSvc       *watchlist.ConfigService
	siteAccountStatsConfigSvc siteAccountStatsConfigApplication
	siteAccountStatsSvc siteAccountStatsApplication
	localMediaReader          ports.LocalMediaSearchReader
	subSvc                    *subscription.Service
	subCreationSvc            subscriptionCreationApplication
	subscriptionListSvc       *subscription.ListService
	controlSvc                *subscription.ControlService
	tvRefreshSvc              *subscription.TVRefreshService
	episodeSourceSvc          *subscription.EpisodeSourceService
	episodeMappingSvc         *subscription.EpisodeMappingService
	episodeReviewSvc          *subscription.EpisodeCandidateReviewService
	tvCoverageReviewSvc       *subscription.TVCoverageCandidateReviewService
	episodeAcquisitionSvc     *acquisition.EpisodeService
	bindingSvc                *subscription.BindingService
	rootSelectionSvc          *subscription.RootSelectionService
	polSvc                    *policy.Service
	searchSvc                 *usecaseSearch.Service
	interactiveSearchSvc      *usecaseSearch.InteractiveSearchService
	indexerManagementSvc      *usecaseSearch.IndexerManagementService
	indexerReceiptSvc         *usecaseSearch.IndexerManagementReceiptService
	candidateSvc              *usecaseSearch.CandidateExplanationService
	scheduler                 *usecaseScheduler.AutoFulfillScheduler
	dlMgr                     *download.Manager
	syncSvc                   *mediaserver.SyncService
	notifySvc                 *notify.Dispatcher
	deliveryManagementSvc     *notify.DeliveryManagementService
	channelStateSvc           *notify.ChannelStateService
	channelReplacementSvc     *notify.ChannelReplacementService
	batchQuerySvc             *notify.BatchQueryService
	deliverySkipSvc           *notify.DeliverySkipService
	fileMappingSvc            *mediausecase.FileMappingService
	statusSvc                 *subscription.StatusService
	feedSvc                   *usecaseSearch.FeedService
	masterKey                 []byte

	completeTVAcquisitionSvc *acquisition.CompleteTVService
	tvSelectionSvc           *acquisition.TVSelectionService

	completeTVOrganizationPreviewSvc *transfer.CompleteTVOrganizationPreviewService
}

func NewServerHandler(
	a AppContext,
	authSvc *auth.Service,
	settingsSvc *settings.Service,
	acqSvc *acquisition.Service,
	transferSvc *transfer.Service,
	metaProvider ports.MetadataProvider,
	subSvc *subscription.Service,
	polSvc *policy.Service,
	searchSvc *usecaseSearch.Service,
	scheduler *usecaseScheduler.AutoFulfillScheduler,
	dlMgr *download.Manager,
	syncSvc *mediaserver.SyncService,
	masterKey []byte,
) *ServerHandler {
	var operationSvc *operation.Service
	if provider, ok := a.(interface{ OperationReader() ports.OperationQuery }); ok {
		operationSvc = operation.NewService(provider.OperationReader())
	}
	var operationTimelineSvc *operation.TimelineService
	if provider, ok := a.(interface {
		OperationTimelineReader() ports.OperationTimelineReader
	}); ok {
		operationTimelineSvc = operation.NewTimelineService(provider.OperationTimelineReader())
	}
	var submissionRetrySvc *operation.SubmissionRetryService
	if provider, ok := a.(interface {
		SubmissionRetryRepository() ports.SubmissionRetryRepository
	}); ok {
		submissionRetrySvc = operation.NewSubmissionRetryService(provider.SubmissionRetryRepository())
	}
	var libraryOverviewSvc *media_library.OverviewService
	if provider, ok := a.(interface {
		LibraryOverviewReader() ports.LibraryOverviewReader
	}); ok {
		libraryOverviewSvc = media_library.NewOverviewService(provider.LibraryOverviewReader())
	}
	var dashboardActivitySvc *operation.ActivityService
	if provider, ok := a.(interface {
		DashboardActivityReader() ports.DashboardActivityReader
	}); ok {
		dashboardActivitySvc = operation.NewActivityService(provider.DashboardActivityReader())
	}
	var libraryCatalogSvc *media_library.Service
	if provider, ok := a.(interface {
		LibraryCatalogReader() ports.LibraryCatalogReader
	}); ok {
		libraryCatalogSvc = media_library.NewService(provider.LibraryCatalogReader())
	}
	var subscriptionListSvc *subscription.ListService
	if provider, ok := a.(interface {
		SubscriptionListReader() ports.SubscriptionListReader
	}); ok {
		subscriptionListSvc = subscription.NewListService(provider.SubscriptionListReader(), metaProvider)
	}
	var localMediaReader ports.LocalMediaSearchReader
	if provider, ok := a.(interface {
		LocalMediaSearchReader() ports.LocalMediaSearchReader
	}); ok {
		localMediaReader = provider.LocalMediaSearchReader()
	}
	var endpointConfigurationSvc *settings.EndpointConfigurationService
	if provider, ok := a.(interface {
		EndpointConfigurationService([]byte, ports.DownloadEndpointTester, ports.MediaEndpointTester) *settings.EndpointConfigurationService
	}); ok {
		var downloadTester ports.DownloadEndpointTester
		if dlMgr != nil {
			downloadTester = dlMgr
		}
		var mediaTester ports.MediaEndpointTester
		if syncSvc != nil {
			mediaTester = syncSvc
		}
		endpointConfigurationSvc = provider.EndpointConfigurationService(masterKey, downloadTester, mediaTester)
	}
	return &ServerHandler{
		subscriptionListSvc:      subscriptionListSvc,
		libraryCatalogSvc:        libraryCatalogSvc,
		libraryOverviewSvc:       libraryOverviewSvc,
		dashboardActivitySvc:     dashboardActivitySvc,
		submissionRetrySvc:       submissionRetrySvc,
		operationSvc:             operationSvc,
		operationTimelineSvc:     operationTimelineSvc,
		app:                      a,
		authSvc:                  authSvc,
		settingsSvc:              settingsSvc,
		endpointConfigurationSvc: endpointConfigurationSvc,
		acqSvc:                   acqSvc,
		transferSvc:              transferSvc,
		metaProvider:             metaProvider,
		localMediaReader:         localMediaReader,
		subSvc:                   subSvc,
		polSvc:                   polSvc,
		searchSvc:                searchSvc,
		scheduler:                scheduler,
		dlMgr:                    dlMgr,
		syncSvc:                  syncSvc,
		masterKey:                masterKey,
	}
}

// GetHealthz handles GET /healthz
func (s *ServerHandler) GetHealthz(ctx context.Context, request GetHealthzRequestObject) (GetHealthzResponseObject, error) {
	h := s.app.Healthz()
	return GetHealthz200JSONResponse{
		Alive:   h.Alive,
		Version: h.Version,
	}, nil
}

// GetReadyz handles GET /readyz
func (s *ServerHandler) GetReadyz(ctx context.Context, request GetReadyzRequestObject) (GetReadyzResponseObject, error) {
	r := s.app.Readyz(ctx)
	checks := make(map[string]bool, len(r.Checks))
	for k, v := range r.Checks {
		checks[k] = v
	}

	if r.Ready {
		return GetReadyz200JSONResponse{
			Ready:  true,
			Checks: &checks,
		}, nil
	}

	reason := r.Reason
	return GetReadyz503JSONResponse{
		Ready:  false,
		Reason: &reason,
		Checks: &checks,
	}, nil
}

// GetSystemStatus handles GET /api/v1/system/status
func (s *ServerHandler) GetSystemStatus(ctx context.Context, request GetSystemStatusRequestObject) (GetSystemStatusResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	h := s.app.Healthz()
	cfg := s.app.Config()
	r := s.app.Readyz(ctx)
	dbReady := r.Checks["database"]

	var dataDir *string
	if actor.IsAdmin() {
		dataDir = &cfg.DataDir
	}
	return GetSystemStatus200JSONResponse{
		Version:  h.Version,
		DbReady:  dbReady,
		DataDir:  dataDir,
		LogLevel: cfg.LogLevel,
	}, nil
}

// GetAuthStatus handles GET /api/v1/auth/status
func (s *ServerHandler) GetAuthStatus(ctx context.Context, request GetAuthStatusRequestObject) (GetAuthStatusResponseObject, error) {
	initialized := false
	if s.authSvc != nil {
		init, err := s.authSvc.IsInitialized(ctx)
		if err != nil {
			return nil, err
		}
		initialized = init
	}

	resp := GetAuthStatus200JSONResponse{
		Initialized: initialized,
	}

	if actor := GetActor(ctx); actor != nil {
		role := Member
		if actor.IsAdmin() {
			role = Admin
		}
		actorUUID, _ := uuid.Parse(actor.ID)
		resp.CurrentUser = &UserInfo{
			Id:        actorUUID,
			Username:  actor.Name,
			Role:      role,
			IsActive:  true,
			CreatedAt: s.app.Clock().Now(),
		}
	}

	return resp, nil
}

// SetupAdmin handles POST /api/v1/auth/setup
func (s *ServerHandler) SetupAdmin(ctx context.Context, request SetupAdminRequestObject) (SetupAdminResponseObject, error) {
	reqID := GetRequestID(ctx)
	if s.authSvc == nil {
		return SetupAdmin400JSONResponse{
			Code:      "SERVICE_UNAVAILABLE",
			Message:   "auth service unavailable",
			RequestId: reqID,
		}, nil
	}

	user, token, err := s.authSvc.SetupAdmin(ctx, request.Body.Username, request.Body.Password)
	if err != nil {
		if errors.Is(err, ports.ErrAlreadyInitialized) {
			return SetupAdmin409JSONResponse{
				Code:      "ALREADY_INITIALIZED",
				Message:   "administrator has already been initialized",
				RequestId: reqID,
			}, nil
		}
		return SetupAdmin400JSONResponse{
			Code:      "BAD_REQUEST",
			Message:   "request could not be completed",
			RequestId: reqID,
		}, nil
	}

	return SetupAdmin200JSONResponse{
		Token: token,
		User: UserInfo{
			Id:        user.ID,
			Username:  user.Username,
			Role:      Admin,
			IsActive:  user.IsActive,
			CreatedAt: user.CreatedAt,
		},
	}, nil
}

// Login handles POST /api/v1/auth/login
func (s *ServerHandler) Login(ctx context.Context, request LoginRequestObject) (LoginResponseObject, error) {
	reqID := GetRequestID(ctx)
	if s.authSvc == nil {
		return Login401JSONResponse{
			Code:      "SERVICE_UNAVAILABLE",
			Message:   "auth service unavailable",
			RequestId: reqID,
		}, nil
	}

	user, token, err := s.authSvc.Login(ctx, request.Body.Username, request.Body.Password)
	if err != nil {
		return Login401JSONResponse{
			Code:      "UNAUTHORIZED",
			Message:   "invalid username or password",
			RequestId: reqID,
		}, nil
	}

	role := Member
	if user.IsAdmin() {
		role = Admin
	}

	return Login200JSONResponse{
		Token: token,
		User: UserInfo{
			Id:        user.ID,
			Username:  user.Username,
			Role:      role,
			IsActive:  user.IsActive,
			CreatedAt: user.CreatedAt,
		},
	}, nil
}

// Logout handles POST /api/v1/auth/logout
func (s *ServerHandler) Logout(ctx context.Context, request LogoutRequestObject) (LogoutResponseObject, error) {
	if s.authSvc == nil {
		return nil, ports.ErrUnavailable
	}
	if err := s.authSvc.Logout(ctx, GetRawToken(ctx)); err != nil {
		return nil, err
	}
	return Logout204Response{}, nil
}

// GetCurrentUser handles GET /api/v1/auth/me
func (s *ServerHandler) GetCurrentUser(ctx context.Context, request GetCurrentUserRequestObject) (GetCurrentUserResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		reqID := GetRequestID(ctx)
		return GetCurrentUser401JSONResponse{
			Code:      "UNAUTHORIZED",
			Message:   "authentication required",
			RequestId: reqID,
		}, nil
	}

	role := Member
	if actor.IsAdmin() {
		role = Admin
	}

	actorUUID, _ := uuid.Parse(actor.ID)
	return GetCurrentUser200JSONResponse{
		Id:        actorUUID,
		Username:  actor.Name,
		Role:      role,
		IsActive:  true,
		CreatedAt: s.app.Clock().Now(),
	}, nil
}

// GetSettings handles GET /api/v1/settings
func (s *ServerHandler) GetSettings(ctx context.Context, request GetSettingsRequestObject) (GetSettingsResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return GetSettings403JSONResponse{Code: "FORBIDDEN", Message: "administrator permission required", RequestId: GetRequestID(ctx)}, nil
	}
	if s.settingsSvc == nil {
		return nil, ports.ErrUnavailable
	}
	items, err := s.settingsSvc.ListSettings(ctx, actor)
	if err != nil {
		return nil, err
	}
	out := make([]SettingItem, 0, len(items))
	for _, item := range items {
		value, ok := item.Value.(map[string]any)
		if !ok || value == nil {
			return nil, ports.ErrUnavailable
		}
		out = append(out, SettingItem{Key: item.Key, Value: value, Version: int(item.Version), UpdatedAt: item.UpdatedAt})
	}
	return GetSettings200JSONResponse{Items: out}, nil
}

// UpdateSetting handles PUT /api/v1/settings/{key}
func (s *ServerHandler) UpdateSetting(ctx context.Context, request UpdateSettingRequestObject) (UpdateSettingResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return UpdateSetting403JSONResponse{
			Code:      "UNAUTHORIZED",
			Message:   "administrator permission required",
			RequestId: reqID,
		}, nil
	}

	if s.settingsSvc == nil {
		return UpdateSetting503JSONResponse{
			Code:      "SERVICE_UNAVAILABLE",
			Message:   "settings service unavailable",
			RequestId: reqID,
		}, nil
	}

	idempKey := GetIdempotencyKey(ctx)
	res, err := s.settingsSvc.UpdateSetting(ctx, actor, request.Key, settings.UpdateSettingRequest{
		Value:           request.Body.Value,
		ExpectedVersion: int64(request.Body.ExpectedVersion),
	}, idempKey)
	if err != nil {
		if errors.Is(err, ports.ErrUnauthorized) {
			return UpdateSetting401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrForbidden) {
			return UpdateSetting403JSONResponse{Code: "FORBIDDEN", Message: "current administrator session required", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrSettingVersionConflict) {
			return UpdateSetting412JSONResponse{Code: "VERSION_CONFLICT", Message: "setting changed; refresh before retrying", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrCommandConflict) {
			return UpdateSetting409JSONResponse{Code: "IDEMPOTENCY_CONFLICT", Message: "request key belongs to another command", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrInvalidSettingCommand) {
			return UpdateSetting400JSONResponse{Code: "INVALID_COMMAND", Message: "invalid setting command", RequestId: reqID}, nil
		}
		return UpdateSetting503JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "settings temporarily unavailable", RequestId: reqID}, nil
	}

	var resValue map[string]interface{}
	if m, ok := res.Data.Value.(map[string]interface{}); ok {
		resValue = m
	} else {
		resValue = make(map[string]interface{})
	}

	return UpdateSetting200JSONResponse{
		Key:       res.Data.Key,
		Value:     resValue,
		Version:   int(res.Data.Version),
		UpdatedAt: res.Data.UpdatedAt,
	}, nil
}

// AcquireMovie handles POST /api/v1/movies/acquire
func (s *ServerHandler) AcquireMovie(ctx context.Context, request AcquireMovieRequestObject) (AcquireMovieResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return AcquireMovie401JSONResponse{
			Code:      "UNAUTHORIZED",
			Message:   "authentication required",
			RequestId: reqID,
		}, nil
	}

	if s.acqSvc == nil {
		return AcquireMovie400JSONResponse{
			Code:      "SERVICE_UNAVAILABLE",
			Message:   "acquisition service unavailable",
			RequestId: reqID,
		}, nil
	}

	idempKey := GetIdempotencyKey(ctx)
	res, err := s.acqSvc.AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{
		UnitIDs:        []uuid.UUID{request.Body.UnitId},
		RootID:         request.Body.RootId,
		CandidateID:    request.Body.CandidateId,
		IdempotencyKey: idempKey,
	})
	if err != nil {
		if errors.Is(err, ports.ErrUnauthorized) {
			return AcquireMovie401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrForbidden) {
			return AcquireMovie403JSONResponse{Code: "FORBIDDEN", Message: "current user cannot execute or recover this command", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrAcquisitionCommandConflict) {
			return AcquireMovie409JSONResponse{Code: "IDEMPOTENCY_CONFLICT", Message: "request key belongs to another command", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrUnavailable) {
			return AcquireMovie503JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "acquisition temporarily unavailable", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrUnsupportedCoverage) {
			return AcquireMovie409JSONResponse{Code: "UNSUPPORTED_COVERAGE", Message: "TV, packs and unproven coverage require review; no acquisition created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrOwnershipUncertain) {
			return AcquireMovie409JSONResponse{Code: "OWNERSHIP_UNCERTAIN", Message: "target file evidence requires review; no acquisition created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrLocalReuseRequired) {
			return AcquireMovie409JSONResponse{Code: "LOCAL_REUSE_REQUIRED", Message: "an existing file in another root requires a reuse plan; no new download created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrUnitAlreadyReserved) || errors.Is(err, acquisition.ErrAlreadyOwned) {
			return AcquireMovie409JSONResponse{
				Code:      "CONFLICT",
				Message:   "request could not be completed",
				RequestId: reqID,
			}, nil
		}
		return AcquireMovie400JSONResponse{
			Code:      "ACQUIRE_FAILED",
			Message:   "request could not be completed",
			RequestId: reqID,
		}, nil
	}

	return AcquireMovie202JSONResponse{
		OperationId: res.OperationID,
		Status:      res.Status,
	}, nil
}

// GetOperation handles GET /api/v1/operations/{id}
func (s *ServerHandler) GetOperation(ctx context.Context, request GetOperationRequestObject) (GetOperationResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetOperation401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}
	value, e := s.operationSvc.Read(ctx, actor, request.Id)
	if e != nil {
		switch {
		case errors.Is(e, ports.ErrForbidden):
			return GetOperation403JSONResponse{Code: "FORBIDDEN", Message: "access to private operation denied", RequestId: reqID}, nil
		case errors.Is(e, ports.ErrNotFound):
			return GetOperation404JSONResponse{Code: "NOT_FOUND", Message: "operation not found", RequestId: reqID}, nil
		default:
			return GetOperation503JSONResponse{Code: "UNAVAILABLE", Message: "operation status temporarily unavailable", RequestId: reqID}, nil
		}
	}
	return GetOperation200JSONResponse(OperationDetails{OperationId: value.OperationID, Status: value.Status, DownloadStatus: value.DownloadStatus, Progress: value.Progress, PlanStatus: value.PlanStatus, PlanId: value.PlanID, Telemetry: operationTelemetryResponse(value.Telemetry)}), nil
}

// CancelOperation handles POST /api/v1/operations/{id}/cancel
func (s *ServerHandler) CancelOperation(ctx context.Context, request CancelOperationRequestObject) (CancelOperationResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return CancelOperation401JSONResponse{
			Code:      "UNAUTHORIZED",
			Message:   "authentication required",
			RequestId: reqID,
		}, nil
	}

	// A shared operation is private to actual owners; never perform SQL here.
	if s.withdrawSvc == nil {
		return CancelOperation503JSONResponse{PolicyUnavailableJSONResponse: PolicyUnavailableJSONResponse{Code: "UNAVAILABLE", Message: "operation service unavailable", RequestId: reqID}}, nil
	}
	_, scopeErr := s.withdrawSvc.List(ctx, actor, request.Id)
	if scopeErr != nil && !errors.Is(scopeErr, ports.ErrForbidden) {
		return CancelOperation503JSONResponse{PolicyUnavailableJSONResponse: PolicyUnavailableJSONResponse{Code: "UNAVAILABLE", Message: "operation scope unavailable", RequestId: reqID}}, nil
	}
	if scopeErr != nil {
		return CancelOperation403JSONResponse{Code: "FORBIDDEN", Message: "access denied", RequestId: reqID}, nil
	}

	// An operation may have been submitted to a downloader or share reservations
	// with other requirements. Until a fenced, transactional cancel and external
	// confirmation exists, never claim success or release its reservations.
	return CancelOperation409JSONResponse{Code: "CANCEL_REQUIRES_REVIEW", Message: "automatic cancellation is unavailable; verify the downloader and requirements before releasing this operation", RequestId: reqID}, nil
}

// ApproveTransferPlan handles POST /api/v1/transfer-plans/{id}/approve
func (s *ServerHandler) ApproveTransferPlan(ctx context.Context, request ApproveTransferPlanRequestObject) (ApproveTransferPlanResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return ApproveTransferPlan401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}
	if !actor.IsAdmin() {
		return ApproveTransferPlan403JSONResponse{Code: "FORBIDDEN", Message: "administrator approval required", RequestId: reqID}, nil
	}

	// This legacy request has no frozen preview credential or leased task.
	// Execution belongs to the persistent organization worker; original-plan
	// recovery uses its separate confirmed preview and command receipt.
	return ApproveTransferPlan409JSONResponse{
		Code: "PLAN_APPROVAL_REQUIRES_REVIEW", Message: "a confirmed original-plan recovery preview is required; inspect the plan before retrying", RequestId: reqID,
	}, nil
}

// SearchMovieReleases handles GET /api/v1/search/movies
func (s *ServerHandler) SearchMovieReleases(ctx context.Context, request SearchMovieReleasesRequestObject) (SearchMovieReleasesResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return SearchMovieReleases401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}
	query := strings.TrimSpace(request.Params.Query)
	kind := "multi"
	if request.Params.Type != nil && *request.Params.Type != "all" {
		kind = string(*request.Params.Type)
	}
	year := 0
	if request.Params.Year != nil {
		year = *request.Params.Year
	}
	if query == "" || utf8.RuneCountInString(query) > 256 || (kind != "movie" && kind != "tv" && kind != "multi") || (request.Params.Year != nil && (year < 1800 || year > 9999)) {
		return SearchMovieReleases400JSONResponse{Code: "INVALID_SEARCH", Message: "provide a query, movie/tv/multi type, and optional valid year", RequestId: reqID}, nil
	}
	var movies []*ports.MovieMetadata
	var err error
	if s.metaProvider != nil {
		movies, err = s.metaProvider.SearchMedia(ctx, query, kind, year)
		if err != nil && !errors.Is(err, ports.ErrAuthRequired) {
			return SearchMovieReleases503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "metadata search is temporarily unavailable", RequestId: reqID}, nil
		}
	}
	if s.metaProvider == nil || errors.Is(err, ports.ErrAuthRequired) {
		if s.localMediaReader == nil {
			return SearchMovieReleases503JSONResponse{Code: "METADATA_NOT_CONFIGURED", Message: "metadata service is not configured or its credentials are invalid", RequestId: reqID}, nil
		}
		movies, err = s.localMediaReader.SearchLocalMedia(ctx, actor, query, kind, year)
		if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
			return SearchMovieReleases403JSONResponse{Code: "FORBIDDEN", Message: "catalog read access is no longer available", RequestId: reqID}, nil
		}
		if err != nil {
			return SearchMovieReleases503JSONResponse{Code: "CATALOG_UNAVAILABLE", Message: "local catalog search is temporarily unavailable", RequestId: reqID}, nil
		}
		if len(movies) == 0 {
			return SearchMovieReleases503JSONResponse{Code: "METADATA_NOT_CONFIGURED", Message: "metadata service is not configured or its credentials are invalid", RequestId: reqID}, nil
		}
	}
	results := make([]MovieSearchResult, 0, len(movies))
	for _, m := range movies {
		if m == nil {
			return SearchMovieReleases503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "metadata search is temporarily unavailable", RequestId: reqID}, nil
		}
		mediaType := m.MediaType
		if mediaType == "" {
			mediaType = "movie"
		}
		if mediaType != "movie" && mediaType != "tv" {
			return SearchMovieReleases503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "metadata search is temporarily unavailable", RequestId: reqID}, nil
		}
		result := MovieSearchResult{TmdbId: m.TMDBID, Title: m.Title, MediaType: (*MovieSearchResultMediaType)(&mediaType)}
		if m.Year > 0 {
			result.Year = &m.Year
		}
		if m.Overview != "" {
			result.Overview = &m.Overview
		}
		if m.PosterURL != "" {
			result.PosterUrl = &m.PosterURL
		}
		results = append(results, result)
	}
	return SearchMovieReleases200JSONResponse{Results: results}, nil
}

// ListSubscriptions handles GET /api/v1/subscriptions
func (s *ServerHandler) ListSubscriptions(ctx context.Context, request ListSubscriptionsRequestObject) (ListSubscriptionsResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return ListSubscriptions401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: GetRequestID(ctx)}, nil
	}
	if s.subscriptionListSvc == nil {
		return nil, ports.ErrUnavailable
	}
	rows, err := s.subscriptionListSvc.List(ctx, actor)
	if err != nil {
		return nil, err
	}
	items := make([]SubscriptionDetails, len(rows))
	for i, row := range rows {
		wanted := make([]WantedItemDetails, len(row.WantedItems))
		for j, item := range row.WantedItems {
			var stage *string
			if item.CurrentFactStage != "" {
				value := item.CurrentFactStage
				stage = &value
			}
			var air *openapi_types.Date
			if item.AirDate != nil {
				value := openapi_types.Date{Time: *item.AirDate}
				air = &value
			}
			wanted[j] = WantedItemDetails{Id: item.ID, UnitId: item.UnitID, UnitKey: &item.UnitKey,
				IsSkipped: item.IsSkipped, IsSatisfied: item.IsSatisfied, CurrentFactStage: stage,
				UserSkipped: &item.UserSkipped, ScopeExcluded: &item.ScopeExcluded, Retired: &item.Retired,
				AirDate: air, AbsoluteNumber: item.AbsoluteNumber, MetadataVersion: &item.MetadataVersion}
		}
		items[i] = SubscriptionDetails{
			Id: row.ID, UserId: row.UserID, MediaId: row.MediaID, RootId: row.RootID,
			QualityPolicyId: row.QualityPolicyID, QualityPolicyRevision: int(row.QualityPolicyRevision),
			State: row.State, IntentVersion: int(row.IntentVersion), ScopeType: row.ScopeType,
			Title: &row.Title, Year: row.Year, MediaType: &row.MediaType, RootName: &row.RootName, RootPath: row.RootPath,
			IndexerIds: &row.IndexerIDs, DownloaderId: row.DownloaderID, PosterUrl: row.PosterURL,
			TotalEpisodes: &row.TotalEpisodes, SatisfiedEpisodes: &row.SatisfiedEpisodes, WantedItems: &wanted,
			TmdbId: subscriptionListTMDBID(row.MediaType, row.TMDBExternalID),
		}
	}
	return ListSubscriptions200JSONResponse{Items: items}, nil
}

// GetSubscription handles GET /api/v1/subscriptions/{id}
func (s *ServerHandler) GetSubscription(ctx context.Context, request GetSubscriptionRequestObject) (GetSubscriptionResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetSubscription401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	sub, err := s.app.Queries().GetSubscriptionByID(ctx, request.Id)
	if err != nil {
		return GetSubscription404JSONResponse{Code: "NOT_FOUND", Message: "subscription not found", RequestId: reqID}, nil
	}

	if !actor.IsAdmin() && sub.UserID.String() != actor.ID {
		return GetSubscription403JSONResponse{Code: "FORBIDDEN", Message: "access to subscription denied", RequestId: reqID}, nil
	}

	media, err := s.app.Queries().GetMediaByID(ctx, sub.MediaID)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	root, err := s.app.Queries().GetLibraryRootByID(ctx, sub.RootID)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	wantedList, err := s.app.Queries().ListWantedItemsWithUnitDetails(ctx, sub.ID)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	wantedDetails := make([]WantedItemDetails, len(wantedList))
	active, satisfied := 0, 0
	for i, wi := range wantedList {
		if !wi.IsSkipped {
			active++
			if wi.IsSatisfied {
				satisfied++
			}
		}
		var stage *string
		if wi.CurrentFactStage != "" {
			value := wi.CurrentFactStage
			stage = &value
		}
		var air *openapi_types.Date
		var absolute *int
		if wi.AirDate.Valid {
			v := openapi_types.Date{Time: wi.AirDate.Time}
			air = &v
		}
		if wi.AbsoluteNumber.Valid {
			v := int(wi.AbsoluteNumber.Int32)
			absolute = &v
		}
		wantedDetails[i] = WantedItemDetails{Id: wi.ID, UnitId: wi.UnitID, UnitKey: &wi.UnitKey, IsSkipped: wi.IsSkipped, IsSatisfied: wi.IsSatisfied, CurrentFactStage: stage, UserSkipped: &wi.UserSkipped, ScopeExcluded: &wi.ScopeExcluded, AirDate: air, AbsoluteNumber: absolute, Retired: &wi.Retired, MetadataVersion: &wi.MetadataVersion}
	}
	var year *int
	if media.Year.Valid {
		value := int(media.Year.Int32)
		year = &value
	}

	boundIndexers, boundDownloader := subscriptionBindings(sub)
	var selectedIDs []uuid.UUID
	var scopeSeasons []int
	if json.Unmarshal(sub.SelectedUnitIds, &selectedIDs) != nil || json.Unmarshal(sub.Seasons, &scopeSeasons) != nil {
		return nil, ports.ErrUnavailable
	}
	return GetSubscription200JSONResponse{SelectedUnitIds: &selectedIDs, Seasons: &scopeSeasons, IncludeSpecials: &sub.IncludeSpecials,
		Title: &media.Title, MediaType: &media.MediaType, Year: year, RootName: &root.Name, RootPath: subscriptionRootPath(actor, root.Path), TotalEpisodes: &active, SatisfiedEpisodes: &satisfied,
		IndexerIds: boundIndexers, DownloaderId: boundDownloader,
		Id:                    sub.ID,
		UserId:                sub.UserID,
		MediaId:               sub.MediaID,
		RootId:                sub.RootID,
		State:                 sub.State,
		IntentVersion:         int(sub.IntentVersion),
		ScopeType:             sub.ScopeType,
		QualityPolicyId:       sub.QualityPolicyID,
		QualityPolicyRevision: int(sub.QualityPolicyRevision),
		WantedItems:           &wantedDetails,
	}, nil
}

// Configuration identities are safe; endpoint credentials never leave their adapters.
func subscriptionBindings(sub db.Subscription) (*[]uuid.UUID, *uuid.UUID) {
	ids := []uuid.UUID{}
	_ = json.Unmarshal(sub.IndexerIds, &ids)
	var downloader *uuid.UUID
	if sub.DownloaderID.Valid {
		v := uuid.UUID(sub.DownloaderID.Bytes)
		downloader = &v
	}
	return &ids, downloader
}

func subscriptionRootPath(actor *ports.Actor, path string) *string {
	if actor == nil || !actor.IsAdmin() {
		return nil
	}
	return &path
}

// Legacy versionless writes never override a newer intent or delete history.
func (s *ServerHandler) PauseSubscription(ctx context.Context, r PauseSubscriptionRequestObject) (PauseSubscriptionResponseObject, error) {
	return s.legacyControl(ctx, r.Id), nil
}
func (s *ServerHandler) ResumeSubscription(ctx context.Context, r ResumeSubscriptionRequestObject) (ResumeSubscriptionResponseObject, error) {
	return s.legacyControl(ctx, r.Id), nil
}
func (s *ServerHandler) DeleteSubscription(ctx context.Context, r DeleteSubscriptionRequestObject) (DeleteSubscriptionResponseObject, error) {
	return s.legacyControl(ctx, r.Id), nil
}

// PreviewPolicy handles POST /api/v1/policies/preview
func (s *ServerHandler) PreviewPolicy(ctx context.Context, request PreviewPolicyRequestObject) (PreviewPolicyResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return PreviewPolicy401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if request.Body == nil || len(request.Body.ReleaseTitle) == 0 || len(request.Body.ReleaseTitle) > 1000 {
		return policyFailure(ctx, ports.ErrInvalidPolicy), nil
	}
	var rules *quality.PolicyRules
	if request.Body.Rules != nil {
		r := policyRules(*request.Body.Rules)
		if quality.ValidateRules(r) != nil {
			return policyFailure(ctx, ports.ErrInvalidPolicy), nil
		}
		rules = &r
	} else {
		if s.polSvc == nil {
			return policyFailure(ctx, ports.ErrUnavailable), nil
		}
		id := "default"
		if request.Body.PolicyId != nil {
			id = *request.Body.PolicyId
		}
		var revision int64
		if request.Body.Revision != nil {
			revision = *request.Body.Revision
		}
		p, e := s.polSvc.Read(ctx, id, revision)
		if e != nil {
			return policyFailure(ctx, e), nil
		}
		rules = &p.Rules
	}
	rec := domainMedia.Recognize(request.Body.ReleaseTitle)
	eval := quality.EvaluateCandidate(*rules, request.Body.ReleaseTitle, rec.Quality)
	if eval.ScoreBreakdown == nil {
		eval.ScoreBreakdown = []string{}
	}

	var rejectReason *string
	if eval.RejectedReason != "" {
		rejectReason = &eval.RejectedReason
	}

	return PreviewPolicy200JSONResponse{
		Passed:         eval.Passed,
		RejectedReason: rejectReason,
		Score:          eval.Score,
		MeetsCutoff:    eval.MeetsCutoff,
		ScoreBreakdown: eval.ScoreBreakdown,
	}, nil
}

// SearchReleases keeps the legacy path non-mutating. New observations require
// an explicit, actor-owned creation command followed by pure progress reads.
func (s *ServerHandler) SearchReleases(ctx context.Context, request SearchReleasesRequestObject) (SearchReleasesResponseObject, error) {
	if GetActor(ctx) == nil {
		return SearchReleases401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: GetRequestID(ctx)}, nil
	}
	return SearchReleases400JSONResponse{Code: "EXPLICIT_SEARCH_REQUIRED", Message: "create an explicit search before reading candidates", RequestId: GetRequestID(ctx)}, nil
}

// DownloadRelease handles POST /api/v1/releases/download
func (s *ServerHandler) DownloadRelease(ctx context.Context, request DownloadReleaseRequestObject) (DownloadReleaseResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return DownloadRelease401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	cand, err := s.app.Queries().GetCandidateByID(ctx, request.Body.CandidateId)
	if err != nil {
		return DownloadRelease400JSONResponse{Code: "NOT_FOUND", Message: "candidate not found", RequestId: reqID}, nil
	}

	rootID := uuid.Nil
	if request.Body.RootId != nil && *request.Body.RootId != uuid.Nil {
		rootID = *request.Body.RootId
	} else {
		firstRoot, err := s.app.Queries().GetFirstOnlineRootForType(ctx, "movie")
		if err == nil {
			rootID = firstRoot.ID
		}
	}
	if rootID == uuid.Nil {
		rootID = s.soleConfiguredRoot(ctx, "movie")
	}
	if rootID == uuid.Nil {
		return DownloadRelease400JSONResponse{Code: "NO_ROOT_AVAILABLE", Message: "no online library root available", RequestId: reqID}, nil
	}

	unitID := uuid.Nil
	if request.Body.UnitId != nil && *request.Body.UnitId != uuid.Nil {
		unitID = *request.Body.UnitId
	} else if cand.MatchedUnitID.Valid {
		unitID = cand.MatchedUnitID.Bytes
	}

	if unitID == uuid.Nil {
		return DownloadRelease400JSONResponse{Code: "UNIT_REQUIRED", Message: "explicit recognized target unit is required; no media or acquisition created", RequestId: reqID}, nil
	}

	res, err := s.acqSvc.AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{
		UnitIDs:      []uuid.UUID{unitID},
		RootID:       rootID,
		CandidateID:  cand.ID,
		DownloaderID: request.Body.ClientId,
	})
	if err != nil {
		if errors.Is(err, ports.ErrForbidden) {
			return DownloadRelease403JSONResponse{Code: "FORBIDDEN", Message: "current user cannot acquire this object", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrUnavailable) {
			return DownloadRelease503JSONResponse{Code: "ACQUIRE_UNAVAILABLE", Message: "acquisition result is unavailable; retry the original request", RequestId: reqID}, nil
		}
		if errors.Is(err, ports.ErrAcquisitionCommandConflict) {
			return DownloadRelease409JSONResponse{Code: "IDEMPOTENCY_CONFLICT", Message: "request key conflicts with an existing command", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrUnsupportedCoverage) {
			return DownloadRelease409JSONResponse{Code: "UNSUPPORTED_COVERAGE", Message: "TV, packs and unproven coverage require review; no acquisition created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrOwnershipUncertain) {
			return DownloadRelease409JSONResponse{Code: "OWNERSHIP_UNCERTAIN", Message: "target file evidence requires review; no acquisition created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrLocalReuseRequired) {
			return DownloadRelease409JSONResponse{Code: "LOCAL_REUSE_REQUIRED", Message: "an existing file in another root requires a reuse plan; no new download created", RequestId: reqID}, nil
		}
		if errors.Is(err, acquisition.ErrUnitAlreadyReserved) || errors.Is(err, acquisition.ErrAlreadyOwned) {
			return DownloadRelease409JSONResponse{Code: "CONFLICT", Message: "request could not be completed", RequestId: reqID}, nil
		}
		return DownloadRelease400JSONResponse{Code: "ACQUIRE_FAILED", Message: "request could not be completed", RequestId: reqID}, nil
	}

	return DownloadRelease202JSONResponse{
		OperationId: res.OperationID,
		Status:      res.Status,
	}, nil
}

// RecheckSubscriptionSearch handles POST /api/v1/subscriptions/{id}/search
func (s *ServerHandler) RecheckSubscriptionSearch(ctx context.Context, request RecheckSubscriptionSearchRequestObject) (RecheckSubscriptionSearchResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return RecheckSubscriptionSearch401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	sub, err := s.app.Queries().GetSubscriptionByID(ctx, request.Id)
	if err != nil {
		return RecheckSubscriptionSearch404JSONResponse{Code: "NOT_FOUND", Message: "subscription not found", RequestId: reqID}, nil
	}
	if !actor.IsAdmin() && sub.UserID.String() != actor.ID {
		return RecheckSubscriptionSearch403JSONResponse{Code: "FORBIDDEN", Message: "access to subscription denied", RequestId: reqID}, nil
	}
	if sub.State != "active" {
		return RecheckSubscriptionSearch409JSONResponse{Code: "NOT_ACTIVE", Message: "resume the subscription before searching", RequestId: reqID}, nil
	}
	if s.searchSvc == nil {
		return RecheckSubscriptionSearch503JSONResponse{Code: "SEARCH_UNAVAILABLE", Message: "search is not configured", RequestId: reqID}, nil
	}

	media, err := s.app.Queries().GetMediaByID(ctx, sub.MediaID)
	if err != nil {
		return RecheckSubscriptionSearch404JSONResponse{Code: "NOT_FOUND", Message: "media record not found", RequestId: reqID}, nil
	}

	// 1. Search online indexers
	candidates, err := s.searchSvc.AggregateSearch(ctx, media.Title)
	if err != nil {
		return RecheckSubscriptionSearch503JSONResponse{Code: "SEARCH_FAILED", Message: "candidate search failed; retry after restoring the search service", RequestId: reqID}, nil
	}
	if len(candidates) == 0 {
		matched := false
		msg := "未找到同名候选资源；未创建下载任务。"
		return RecheckSubscriptionSearch200JSONResponse{
			Matched: &matched,
			Message: &msg,
		}, nil
	}

	// AggregateSearch currently matches the title substring only. Neither media
	// identity nor season/episode coverage is verified; pairing its first result
	// with the first missing unit can download the wrong work or a skipped unit.
	// Inspection is safe; acquisition must wait for verified unit mapping.
	matched := false
	msg := "找到同名资源，但尚未核验影视身份与季集；未创建下载任务。"
	return RecheckSubscriptionSearch200JSONResponse{Matched: &matched, Message: &msg}, nil
}

// GetTMDBMovieDetails handles GET /api/v1/media/tmdb/movie/{id}
func (s *ServerHandler) GetTMDBMovieDetails(ctx context.Context, request GetTMDBMovieDetailsRequestObject) (GetTMDBMovieDetailsResponseObject, error) {
	reqID, actor := GetRequestID(ctx), GetActor(ctx)
	if actor == nil {
		return GetTMDBMovieDetails401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", reqID)), nil
	}
	if !metadataReadAllowed(actor) {
		return GetTMDBMovieDetails403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "metadata read access denied", reqID)), nil
	}
	if request.Id <= 0 {
		return GetTMDBMovieDetails400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_ID", "invalid movie identity", reqID)), nil
	}
	if s.metaProvider == nil {
		return GetTMDBMovieDetails503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "movie metadata is temporarily unavailable", reqID)), nil
	}
	meta, err := s.metaProvider.GetMovieDetails(ctx, request.Id, "zh-CN")
	if errors.Is(err, ports.ErrNotFound) {
		return GetTMDBMovieDetails404ApplicationProblemPlusJSONResponse(problemResponse(http.StatusNotFound, "NOT_FOUND", "movie not found", reqID)), nil
	}
	if err != nil || meta == nil || meta.TMDBID != request.Id || strings.TrimSpace(meta.Title) == "" || (meta.MediaType != "" && meta.MediaType != "movie") {
		return GetTMDBMovieDetails503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "movie metadata is temporarily unavailable", reqID)), nil
	}
	var ratingPtr *float32
	if meta.Rating > 0 {
		r := float32(meta.Rating)
		ratingPtr = &r
	}

	return GetTMDBMovieDetails200JSONResponse{
		TmdbId:        meta.TMDBID,
		Title:         meta.Title,
		OriginalTitle: &meta.OriginalTitle,
		Year:          &meta.Year,
		Overview:      &meta.Overview,
		PosterUrl:     &meta.PosterURL,
		BackdropUrl:   &meta.BackdropURL,
		Rating:        ratingPtr,
		Runtime:       &meta.Runtime,
		Genres:        &meta.Genres,
	}, nil
}

// GetTMDBTVDetails handles GET /api/v1/media/tmdb/tv/{id}
func (s *ServerHandler) GetTMDBTVDetails(ctx context.Context, request GetTMDBTVDetailsRequestObject) (GetTMDBTVDetailsResponseObject, error) {
	reqID, actor := GetRequestID(ctx), GetActor(ctx)
	if actor == nil {
		return GetTMDBTVDetails401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", reqID)), nil
	}
	if !metadataReadAllowed(actor) {
		return GetTMDBTVDetails403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "metadata read access denied", reqID)), nil
	}
	if request.Id <= 0 {
		return GetTMDBTVDetails400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_ID", "invalid TV identity", reqID)), nil
	}
	if s.metaProvider == nil {
		return GetTMDBTVDetails503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "TV metadata is temporarily unavailable", reqID)), nil
	}
	tv, err := s.metaProvider.GetTVDetails(ctx, request.Id, "zh-CN")
	if errors.Is(err, ports.ErrNotFound) {
		return GetTMDBTVDetails404JSONResponse{Code: "NOT_FOUND", Message: "TV show not found", RequestId: reqID}, nil
	}
	if err != nil || tv == nil || tv.TMDBID != request.Id || strings.TrimSpace(tv.Title) == "" {
		return GetTMDBTVDetails503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "TV metadata is temporarily unavailable", RequestId: reqID}, nil
	}

	var ratingPtr *float32
	if tv.Rating > 0 {
		r := float32(tv.Rating)
		ratingPtr = &r
	}

	seasons := make([]TMDBTVSeasonItem, len(tv.Seasons))
	for i, item := range tv.Seasons {
		var airDatePtr *string
		if item.AirDate != "" {
			airDatePtr = &item.AirDate
		}
		var posterPtr *string
		if item.PosterURL != "" {
			posterPtr = &item.PosterURL
		}
		seasons[i] = TMDBTVSeasonItem{
			SeasonNumber: item.SeasonNumber,
			Name:         item.Name,
			EpisodeCount: item.EpisodeCount,
			AirDate:      airDatePtr,
			PosterUrl:    posterPtr,
		}
	}

	return GetTMDBTVDetails200JSONResponse{
		TmdbId:        tv.TMDBID,
		Title:         tv.Title,
		OriginalTitle: &tv.OriginalTitle,
		Year:          &tv.Year,
		Overview:      &tv.Overview,
		PosterUrl:     &tv.PosterURL,
		BackdropUrl:   &tv.BackdropURL,
		Rating:        ratingPtr,
		Genres:        &tv.Genres,
		SeasonsCount:  tv.SeasonsCount,
		EpisodesCount: tv.EpisodesCount,
		Seasons:       seasons,
	}, nil
}

// SyncDoubanWishlist handles POST /api/v1/sync/douban
func (s *ServerHandler) SyncDoubanWishlist(ctx context.Context, request SyncDoubanWishlistRequestObject) (SyncDoubanWishlistResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return SyncDoubanWishlist401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	// The legacy route used an unverified Frodo protocol and the first browse
	// match. It cannot write until the persistent synchronization fences exist.
	return SyncDoubanWishlist503ApplicationProblemPlusJSONResponse{
		Code: "DOUBAN_SYNC_UNAVAILABLE", Message: "persistent Douban synchronization is unavailable", RequestId: reqID,
	}, nil
}

// CheckinIndexer handles POST /api/v1/indexers/{id}/checkin
func (s *ServerHandler) CheckinIndexer(ctx context.Context, request CheckinIndexerRequestObject) (CheckinIndexerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return CheckinIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	idx, err := s.app.Queries().GetIndexerByID(ctx, request.Id)
	if err != nil {
		return CheckinIndexer404JSONResponse{Code: "NOT_FOUND", Message: "indexer not found", RequestId: reqID}, nil
	}

	_ = idx // Configured site cookies and per-site sign-in contracts are not wired yet.
	succ := false
	msg := "站点签到尚未配置认证和结果确认；未向站点发送请求"

	return CheckinIndexer200JSONResponse{
		Success: &succ,
		Message: &msg,
	}, nil
}

// ScanDirectoryReorganize handles POST /api/v1/transfer/scan
func (s *ServerHandler) ScanDirectoryReorganize(ctx context.Context, request ScanDirectoryReorganizeRequestObject) (ScanDirectoryReorganizeResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return ScanDirectoryReorganize401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	// No configured, symlink-safe source root or preview/commit contract exists.
	// Return no fabricated paths or confidence scores for arbitrary client paths.
	items := []ScanReorganizeItem{}
	return ScanDirectoryReorganize200JSONResponse{Items: &items}, nil
}
