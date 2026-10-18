package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/dalamudx/PulseMedia/internal/usecase/operation"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
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
	app                    AppContext
	operationSvc           *operation.Service
	libraryCatalogSvc      *media_library.Service
	submissionRetrySvc     *operation.SubmissionRetryService
	activityHub            *SSEHub
	tokenManagementSvc     *auth.TokenManagementService
	authSvc                *auth.Service
	settingsSvc            *settings.Service
	upgradePreviewSvc      *acquisition.UpgradePreviewService
	upgradeReservationSvc  *acquisition.UpgradeReservationService
	upgradePreparationSvc  *acquisition.PublicUpgradePreparationService
	upgradeSendSvc         *acquisition.PublicUpgradeSendService
	upgradeOrganizationSvc *acquisition.PublicUpgradeOrganizationService
	upgradeRetirementSvc   *acquisition.PublicUpgradeRetirementService
	acqSvc                 *acquisition.Service
	movieSelectionSvc      *acquisition.MovieSelectionService
	withdrawSvc            *acquisition.WithdrawalService
	transferSvc            *transfer.Service
	recoverySvc            *transfer.RecoveryService
	revisionSvc            *transfer.RevisionService
	reuseSvc               *transfer.LibraryReuseService
	metaProvider           ports.MetadataProvider
	subSvc                 *subscription.Service
	subscriptionListSvc    *subscription.ListService
	controlSvc             *subscription.ControlService
	tvRefreshSvc           *subscription.TVRefreshService
	episodeSourceSvc       *subscription.EpisodeSourceService
	episodeMappingSvc      *subscription.EpisodeMappingService
	episodeReviewSvc       *subscription.EpisodeCandidateReviewService
	tvCoverageReviewSvc    *subscription.TVCoverageCandidateReviewService
	episodeAcquisitionSvc  *acquisition.EpisodeService
	bindingSvc             *subscription.BindingService
	rootSelectionSvc       *subscription.RootSelectionService
	polSvc                 *policy.Service
	searchSvc              *usecaseSearch.Service
	candidateSvc           *usecaseSearch.CandidateExplanationService
	scheduler              *usecaseScheduler.AutoFulfillScheduler
	dlMgr                  *download.Manager
	syncSvc                *mediaserver.SyncService
	notifySvc              *notify.Dispatcher
	deliveryManagementSvc  *notify.DeliveryManagementService
	channelStateSvc        *notify.ChannelStateService
	channelReplacementSvc  *notify.ChannelReplacementService
	batchQuerySvc          *notify.BatchQueryService
	deliverySkipSvc        *notify.DeliverySkipService
	fileMappingSvc         *mediausecase.FileMappingService
	statusSvc              *subscription.StatusService
	feedSvc                *usecaseSearch.FeedService
	masterKey              []byte

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
	var submissionRetrySvc *operation.SubmissionRetryService
	if provider, ok := a.(interface {
		SubmissionRetryRepository() ports.SubmissionRetryRepository
	}); ok {
		submissionRetrySvc = operation.NewSubmissionRetryService(provider.SubmissionRetryRepository())
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
	return &ServerHandler{
		subscriptionListSvc: subscriptionListSvc,
		libraryCatalogSvc:   libraryCatalogSvc,
		submissionRetrySvc:  submissionRetrySvc,
		operationSvc:        operationSvc,
		app:                 a,
		authSvc:             authSvc,
		settingsSvc:         settingsSvc,
		acqSvc:              acqSvc,
		transferSvc:         transferSvc,
		metaProvider:        metaProvider,
		subSvc:              subSvc,
		polSvc:              polSvc,
		searchSvc:           searchSvc,
		scheduler:           scheduler,
		dlMgr:               dlMgr,
		syncSvc:             syncSvc,
		masterKey:           masterKey,
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
	return GetOperation200JSONResponse(OperationDetails{OperationId: value.OperationID, Status: value.Status, DownloadStatus: value.DownloadStatus, Progress: value.Progress, PlanStatus: value.PlanStatus, PlanId: value.PlanID}), nil
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

	year := 0
	if request.Params.Year != nil {
		year = *request.Params.Year
	}

	results := make([]MovieSearchResult, 0)

	mediaType := "all"
	if request.Params.Type != nil {
		mediaType = string(*request.Params.Type)
	}

	// 1. Try querying configured metadata provider (TMDB)
	if s.metaProvider != nil {
		movies, err := s.metaProvider.SearchMedia(ctx, request.Params.Query, mediaType, year)
		if err == nil {
			for _, m := range movies {
				mType := m.MediaType
				if mType == "" {
					mType = "movie"
				}
				res := MovieSearchResult{
					TmdbId:    m.TMDBID,
					Title:     m.Title,
					MediaType: (*MovieSearchResultMediaType)(&mType),
				}
				if m.Year > 0 {
					res.Year = &m.Year
				}
				if m.Overview != "" {
					res.Overview = &m.Overview
				}
				if m.PosterURL != "" {
					res.PosterUrl = &m.PosterURL
				}
				results = append(results, res)
			}
			return SearchMovieReleases200JSONResponse{Results: results}, nil
		}

		if !errors.Is(err, ports.ErrAuthRequired) {
			return SearchMovieReleases401JSONResponse{Code: "SEARCH_FAILED", Message: "request could not be completed", RequestId: reqID}, nil
		}
	}

	// 2. If TMDB key unauthenticated or missing: search local media library in database
	if s.app != nil && s.app.DBPool() != nil {
		rows, err := s.app.DBPool().Query(ctx,
			"SELECT id, title, year FROM media WHERE title ILIKE '%' || $1 || '%' LIMIT 20",
			request.Params.Query)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var mID uuid.UUID
				var mTitle string
				var mYear *int32
				if err := rows.Scan(&mID, &mTitle, &mYear); err == nil {
					res := MovieSearchResult{
						TmdbId: 0,
						Title:  mTitle,
					}
					if mYear != nil {
						y := int(*mYear)
						res.Year = &y
					}
					results = append(results, res)
				}
			}
		}
	}

	if len(results) > 0 {
		return SearchMovieReleases200JSONResponse{Results: results}, nil
	}

	// 3. If no local media and TMDB unauthenticated: return instructional error
	return SearchMovieReleases401JSONResponse{
		Code:      "AUTH_REQUIRED",
		Message:   "TMDB API 密钥未配置或已失效。请在「系统配置」中配置 TMDB 密钥，或设置环境变量 TMDB_API_KEY",
		RequestId: reqID,
	}, nil
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
			wanted[j] = WantedItemDetails{Id: item.ID, UnitId: item.UnitID, UnitKey: &item.UnitKey,
				IsSkipped: item.IsSkipped, IsSatisfied: item.IsSatisfied, CurrentFactStage: stage}
		}
		items[i] = SubscriptionDetails{
			Id: row.ID, UserId: row.UserID, MediaId: row.MediaID, RootId: row.RootID,
			QualityPolicyId: row.QualityPolicyID, QualityPolicyRevision: int(row.QualityPolicyRevision),
			State: row.State, IntentVersion: int(row.IntentVersion), ScopeType: row.ScopeType,
			Title: &row.Title, Year: row.Year, MediaType: &row.MediaType, RootName: &row.RootName, RootPath: row.RootPath,
			IndexerIds: &row.IndexerIDs, DownloaderId: row.DownloaderID, PosterUrl: row.PosterURL,
			TotalEpisodes: &row.TotalEpisodes, SatisfiedEpisodes: &row.SatisfiedEpisodes, WantedItems: &wanted,
		}
	}
	return ListSubscriptions200JSONResponse{Items: items}, nil
}

// CreateSubscription handles POST /api/v1/subscriptions
func (s *ServerHandler) CreateSubscription(ctx context.Context, request CreateSubscriptionRequestObject) (CreateSubscriptionResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return CreateSubscription401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || (actor.Role != ports.RoleMember && actor.Role != ports.RoleAdmin) {
		return CreateSubscription403JSONResponse{Code: "FORBIDDEN", Message: "user session required", RequestId: reqID}, nil
	}

	if s.subSvc == nil {
		return CreateSubscription400JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "subscription service unavailable", RequestId: reqID}, nil
	}

	mediaID := uuid.Nil
	if request.Body.MediaId != nil {
		mediaID = *request.Body.MediaId
	}

	mediaType := "movie"
	if request.Body.MediaType != nil {
		mediaType = string(*request.Body.MediaType)
	}

	var mediaInput *subscription.SubscriptionMediaInput
	if request.Body.TmdbId != nil {
		id := *request.Body.TmdbId
		if id <= 0 || mediaID != uuid.Nil || (mediaType != "movie" && mediaType != "tv") {
			return CreateSubscription400JSONResponse{Code: "BAD_REQUEST", Message: "select one valid media identity", RequestId: reqID}, nil
		}
		if s.metaProvider == nil {
			return CreateSubscription503JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "metadata service unavailable", RequestId: reqID}, nil
		}
		var title string
		var year int
		var seasons []subscription.SeasonEpisodes
		if mediaType == "movie" {
			m, err := s.metaProvider.GetMovieDetails(ctx, int64(id), "zh-CN")
			if err != nil {
				return CreateSubscription503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "metadata could not be verified", RequestId: reqID}, nil
			}
			if m == nil || m.TMDBID != int64(id) || m.MediaType != "movie" {
				return CreateSubscription400JSONResponse{Code: "BAD_METADATA", Message: "movie identity could not be verified", RequestId: reqID}, nil
			}
			title, year = m.Title, m.Year
			if title == "" {
				title = m.OriginalTitle
			}
		} else {
			m, err := s.metaProvider.GetTVDetails(ctx, int64(id), "zh-CN")
			if err != nil {
				return CreateSubscription503JSONResponse{Code: "METADATA_UNAVAILABLE", Message: "metadata could not be verified", RequestId: reqID}, nil
			}
			if m == nil || m.TMDBID != int64(id) {
				return CreateSubscription400JSONResponse{Code: "BAD_METADATA", Message: "TV identity could not be verified", RequestId: reqID}, nil
			}
			title, year = m.Title, m.Year
			for _, season := range m.Seasons {
				seasons = append(seasons, subscription.SeasonEpisodes{Season: season.SeasonNumber, Count: season.EpisodeCount})
			}
		}
		if title == "" || year < 0 || year > 9999 {
			return CreateSubscription400JSONResponse{Code: "BAD_METADATA", Message: "media details could not be verified", RequestId: reqID}, nil
		}
		mediaInput = &subscription.SubscriptionMediaInput{Namespace: "tmdb." + mediaType, ExternalID: strconv.FormatInt(id, 10), Title: title, MediaType: mediaType, Seasons: seasons}
		if year > 0 {
			y := int32(year)
			mediaInput.Year = &y
		}
	}

	if mediaID == uuid.Nil && mediaInput == nil {
		return CreateSubscription400JSONResponse{Code: "BAD_REQUEST", Message: "either media_id or tmdb_id must be provided", RequestId: reqID}, nil
	}

	// 2. Resolve Root ID: if not provided, auto-select first online root for this media type!
	var rootID uuid.UUID
	if request.Body.RootId != nil && *request.Body.RootId != uuid.Nil {
		rootID = *request.Body.RootId
	} else {
		rootID = s.soleConfiguredRoot(ctx, mediaType)
	}

	if rootID == uuid.Nil {
		return CreateSubscription400JSONResponse{Code: "NO_ROOT_AVAILABLE", Message: fmt.Sprintf("select an explicitly configured online library root for %s", mediaType), RequestId: reqID}, nil
	}

	scope := "all"
	if request.Body.ScopeType != nil {
		scope = string(*request.Body.ScopeType)
	}
	policyID := "default"
	if request.Body.QualityPolicyId != nil {
		policyID = *request.Body.QualityPolicyId
	}

	var seasons []int
	if request.Body.Seasons != nil {
		seasons = *request.Body.Seasons
	}
	includeSpecials := false
	if request.Body.IncludeSpecials != nil {
		includeSpecials = *request.Body.IncludeSpecials
	}

	var episodes []string
	if request.Body.Episodes != nil {
		episodes = *request.Body.Episodes
	}

	if len(episodes) > 0 {
		scope = "selected"
	}
	var indexers []uuid.UUID
	if request.Body.IndexerIds != nil {
		indexers = *request.Body.IndexerIds
	}

	sub, _, err := s.subSvc.CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{
		MediaInput: mediaInput, EpisodeKeys: episodes,
		MediaID:         mediaID,
		RootID:          rootID,
		ScopeType:       scope,
		QualityPolicyID: policyID,
		Seasons:         seasons,
		IncludeSpecials: includeSpecials,
		SelectedUnitIDs: nil,
		IndexerIDs:      indexers, DownloaderID: request.Body.DownloaderId,
	})
	if err != nil {
		if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) {
			return CreateSubscription403JSONResponse{Code: "FORBIDDEN", Message: "current account cannot create subscription", RequestId: reqID}, nil
		}
		if errors.Is(err, subscription.ErrInvalidScope) {
			return CreateSubscription400JSONResponse{Code: "INVALID_SCOPE", Message: "select a valid media scope and online root", RequestId: reqID}, nil
		}
		return CreateSubscription503JSONResponse{Code: "CREATE_UNAVAILABLE", Message: "subscription could not be saved", RequestId: reqID}, nil
	}

	// HTTP only saves intent. Owned persistent workers perform automatic search.

	wantedItems, err := s.app.Queries().ListWantedItemsWithUnitDetails(ctx, sub.ID)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	wantedDetails := make([]WantedItemDetails, len(wantedItems))
	for i, wi := range wantedItems {
		wantedDetails[i] = WantedItemDetails{
			Id:          wi.ID,
			UnitId:      wi.UnitID,
			IsSkipped:   wi.IsSkipped,
			IsSatisfied: wi.IsSatisfied,
		}
	}

	boundIndexers := append([]uuid.UUID{}, sub.IndexerIDs...)
	boundDownloader := sub.DownloaderID
	return CreateSubscription201JSONResponse{
		IndexerIds: &boundIndexers, DownloaderId: boundDownloader,
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

func (s *ServerHandler) soleConfiguredRoot(ctx context.Context, mediaType string) uuid.UUID {
	if s.rootSelectionSvc == nil {
		return uuid.Nil
	}
	roots, e := s.rootSelectionSvc.List(ctx, GetActor(ctx))
	if e != nil {
		return uuid.Nil
	}
	id := uuid.Nil
	for _, root := range roots {
		if root.MediaType != mediaType {
			continue
		}
		if id != uuid.Nil {
			return uuid.Nil
		}
		id = root.ID
	}
	return id
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

// ListIndexers handles GET /api/v1/indexers
func (s *ServerHandler) ListIndexers(ctx context.Context, request ListIndexersRequestObject) (ListIndexersResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return ListIndexers401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if s.searchSvc == nil {
		return ListIndexers503JSONResponse{Code: "UNAVAILABLE", Message: "indexer state unavailable", RequestId: reqID}, nil
	}
	rows, err := s.searchSvc.ListConfiguredIndexers(ctx)
	if err != nil {
		return ListIndexers503JSONResponse{Code: "UNAVAILABLE", Message: "indexer state unavailable", RequestId: reqID}, nil
	}

	items := make([]IndexerDetails, len(rows))
	for i, r := range rows {
		items[i] = IndexerDetails{
			Id:   r.ID,
			Name: r.Name,
			Url:  endpointDisplayURL(r.URL), Health: r.Health, ConfigVersion: int(r.ConfigVersion), CreatedAt: &r.CreatedAt,
			HealthCheckedAt: r.CheckedAt, HealthObservedAt: r.ObservedAt, HealthNextCheckAt: &r.NextCheckAt, HealthFailures: func() *int { x := int(r.Failures); return &x }(), HealthReason: func() *IndexerDetailsHealthReason { x := IndexerDetailsHealthReason(r.Reason); return &x }(),
		}
	}
	return ListIndexers200JSONResponse{Items: items}, nil
}

// CreateIndexer handles POST /api/v1/indexers
func (s *ServerHandler) CreateIndexer(ctx context.Context, request CreateIndexerRequestObject) (CreateIndexerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return CreateIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.searchSvc == nil {
		return CreateIndexer400JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "search service unavailable", RequestId: reqID}, nil
	}

	var cats []int
	if request.Body.Categories != nil {
		cats = *request.Body.Categories
	}

	if endpointURLHasMask(request.Body.Url) {
		return CreateIndexer400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL", RequestId: reqID}, nil
	}
	row, err := s.searchSvc.CreateIndexer(ctx, request.Body.Name, request.Body.Url, request.Body.ApiKey, cats)
	if err != nil {
		return CreateIndexer400JSONResponse{Code: "CREATE_FAILED", Message: "request could not be completed", RequestId: reqID}, nil
	}

	return CreateIndexer201JSONResponse{
		Id:            row.ID,
		Name:          row.Name,
		Url:           endpointDisplayURL(row.Url),
		Health:        row.Health,
		ConfigVersion: int(row.ConfigVersion),
		CreatedAt:     &row.CreatedAt,
	}, nil
}

// DeleteIndexer handles DELETE /api/v1/indexers/{id}
func (s *ServerHandler) DeleteIndexer(ctx context.Context, request DeleteIndexerRequestObject) (DeleteIndexerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return DeleteIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.searchSvc == nil {
		return DeleteIndexer404JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "search service unavailable", RequestId: reqID}, nil
	}

	err := s.searchSvc.DeleteIndexer(ctx, request.Id)
	if err != nil {
		return DeleteIndexer404JSONResponse{Code: "NOT_FOUND", Message: "request could not be completed", RequestId: reqID}, nil
	}
	return DeleteIndexer204Response{}, nil
}

// UpdateIndexer handles PUT /api/v1/indexers/{id}
func (s *ServerHandler) UpdateIndexer(ctx context.Context, request UpdateIndexerRequestObject) (UpdateIndexerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return UpdateIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	var encKey []byte
	if request.Body.ApiKey != "" {
		var err error
		encKey, err = crypto.Encrypt(s.masterKey, []byte(request.Body.ApiKey))
		if err != nil {
			return nil, ports.ErrUnavailable
		}
	}
	var catsBytes []byte
	if request.Body.Categories != nil {
		catsBytes, _ = json.Marshal(*request.Body.Categories)
	}

	tx, q, err := s.beginEndpointEdit(ctx)
	if errors.Is(err, ports.ErrForbidden) {
		return UpdateIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := q.LockIndexerForEdit(ctx, request.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpdateIndexer404JSONResponse{Code: "NOT_FOUND", Message: "endpoint not found", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	endpointURL, err := resolveEndpointEditURL(request.Body.Url, current.Url, request.Body.ExpectedVersion != nil, request.Body.ExpectedVersion != nil && *request.Body.ExpectedVersion == current.ConfigVersion)
	if errors.Is(err, errEndpointRevision) {
		return UpdateIndexer409JSONResponse{Code: "ENDPOINT_VERSION_CONFLICT", Message: "endpoint changed or preservation version missing; reload before editing", RequestId: reqID}, nil
	}
	if err != nil {
		return UpdateIndexer400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL when changing the endpoint", RequestId: reqID}, nil
	}
	row, err := q.EditIndexer(ctx, db.EditIndexerParams{
		ID:              request.Id,
		Name:            request.Body.Name,
		Url:             endpointURL,
		ApiKeyEncrypted: encKey,
		Categories:      catsBytes,
	})
	if err != nil {
		return UpdateIndexer404JSONResponse{Code: "NOT_FOUND", Message: "indexer not found", RequestId: reqID}, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return UpdateIndexer200JSONResponse{
		Id:            row.ID,
		Name:          row.Name,
		Url:           endpointDisplayURL(row.Url),
		Health:        row.Health,
		ConfigVersion: int(row.ConfigVersion),
	}, nil
}

// TestIndexer handles POST /api/v1/indexers/{id}/test
func (s *ServerHandler) TestIndexer(ctx context.Context, request TestIndexerRequestObject) (TestIndexerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return TestIndexer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.searchSvc == nil {
		return TestIndexer404JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "search service unavailable", RequestId: reqID}, nil
	}

	alive, latencyMs, msg, _ := s.searchSvc.TestIndexer(ctx, request.Id)
	latInt := int(latencyMs)
	return TestIndexer200JSONResponse{
		Alive:     alive,
		LatencyMs: &latInt,
		Message:   msg,
	}, nil
}

// SearchReleases handles GET /api/v1/releases/search
func (s *ServerHandler) SearchReleases(ctx context.Context, request SearchReleasesRequestObject) (SearchReleasesResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return SearchReleases401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if s.searchSvc == nil {
		return SearchReleases503JSONResponse{Code: "SEARCH_UNAVAILABLE", Message: "search is not configured", RequestId: reqID}, nil
	}

	candidates, partial, err := s.searchSvc.AggregateSearchDetailed(ctx, request.Params.Query)
	if err != nil {
		return SearchReleases503JSONResponse{Code: "SEARCH_FAILED", Message: "candidate search failed; retry after restoring the search service", RequestId: reqID}, nil
	}

	results := make([]ReleaseItemResult, len(candidates))
	for i, c := range candidates {
		rec := domainMedia.Recognize(c.Title)
		idx, _ := s.app.Queries().GetIndexerByID(ctx, c.IndexerID)
		idxName := idx.Name
		if idxName == "" {
			idxName = "Tracker"
		}

		resStr := string(rec.Quality.Resolution)
		srcStr := string(rec.Quality.Source)
		codecStr := string(rec.Quality.Codec)
		scoreVal := quality.DefaultPolicy().Score(rec.Quality)

		results[i] = ReleaseItemResult{
			Id:          c.ID,
			Title:       c.Title,
			SizeBytes:   c.SizeBytes,
			Seeders:     int(c.Seeders),
			Leechers:    int(c.Leechers),
			IndexerName: idxName,
			Resolution:  &resStr,
			Source:      &srcStr,
			Codec:       &codecStr,
			Score:       &scoreVal,
		}
	}
	return SearchReleases200JSONResponse{Results: results, Partial: partial}, nil
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

// ListDownloadClients handles GET /api/v1/download-clients
func (s *ServerHandler) ListDownloadClients(ctx context.Context, request ListDownloadClientsRequestObject) (ListDownloadClientsResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return ListDownloadClients401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	rows, err := s.app.Queries().ListDownloadClients(ctx)
	if err != nil {
		return ListDownloadClients200JSONResponse{Items: []DownloadClientDetails{}}, nil
	}

	items := make([]DownloadClientDetails, len(rows))
	for i, r := range rows {
		items[i] = DownloadClientDetails{
			Id:         r.ID,
			Name:       r.Name,
			ClientType: r.ClientType,
			Url:        endpointDisplayURL(r.Url),
			UpdatedAt:  r.UpdatedAt.Time,
			Username:   r.Username,
		}
	}
	return ListDownloadClients200JSONResponse{Items: items}, nil
}

// CreateDownloadClient handles POST /api/v1/download-clients
func (s *ServerHandler) CreateDownloadClient(ctx context.Context, request CreateDownloadClientRequestObject) (CreateDownloadClientResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return CreateDownloadClient401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	encPass, err := crypto.Encrypt(s.masterKey, []byte(request.Body.Password))
	if err != nil {
		return CreateDownloadClient400JSONResponse{Code: "ENCRYPTION_FAILED", Message: "credential encryption unavailable", RequestId: reqID}, nil
	}
	if endpointURLHasMask(request.Body.Url) {
		return CreateDownloadClient400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL", RequestId: reqID}, nil
	}
	pathMapBytes, _ := json.Marshal(request.Body.RemotePathMap)
	clientID := s.app.IDGen().NewUUID()

	row, err := s.app.Queries().CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                clientID,
		Name:              request.Body.Name,
		ClientType:        string(request.Body.ClientType),
		Url:               request.Body.Url,
		Username:          request.Body.Username,
		PasswordEncrypted: encPass,
		RemotePathMap:     pathMapBytes,
	})
	if err != nil {
		return CreateDownloadClient400JSONResponse{Code: "CREATE_FAILED", Message: "request could not be completed", RequestId: reqID}, nil
	}

	return CreateDownloadClient201JSONResponse{
		Id:         row.ID,
		Name:       row.Name,
		ClientType: row.ClientType,
		Url:        endpointDisplayURL(row.Url),
		UpdatedAt:  row.UpdatedAt.Time,
		Username:   row.Username,
	}, nil
}

// DeleteDownloadClient handles DELETE /api/v1/download-clients/{id}
func (s *ServerHandler) DeleteDownloadClient(ctx context.Context, request DeleteDownloadClientRequestObject) (DeleteDownloadClientResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return DeleteDownloadClient401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.dlMgr == nil {
		return DeleteDownloadClient409JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "download manager unavailable", RequestId: reqID}, nil
	}
	if err := s.dlMgr.DeleteClient(ctx, request.Id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeleteDownloadClient404JSONResponse{Code: "NOT_FOUND", Message: "download client not found", RequestId: reqID}, nil
		}
		if errors.Is(err, download.ErrClientInUse) {
			return DeleteDownloadClient409JSONResponse{Code: "CLIENT_IN_USE", Message: "download client is referenced by tasks or subscriptions; resolve them before removal", RequestId: reqID}, nil
		}
		return nil, err
	}
	return DeleteDownloadClient204Response{}, nil
}

// UpdateDownloadClient handles PUT /api/v1/download-clients/{id}
func (s *ServerHandler) UpdateDownloadClient(ctx context.Context, request UpdateDownloadClientRequestObject) (UpdateDownloadClientResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return UpdateDownloadClient401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	var encPass []byte
	if request.Body.Password != "" {
		var err error
		encPass, err = crypto.Encrypt(s.masterKey, []byte(request.Body.Password))
		if err != nil {
			return nil, err
		}
	}

	tx, q, err := s.beginEndpointEdit(ctx)
	if errors.Is(err, ports.ErrForbidden) {
		return UpdateDownloadClient401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := q.LockDownloadClientForEdit(ctx, request.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpdateDownloadClient404JSONResponse{Code: "NOT_FOUND", Message: "endpoint not found", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	if request.Body.Password == "" {
		encPass = current.PasswordEncrypted
	}
	endpointURL, err := resolveEndpointEditURL(request.Body.Url, current.Url, request.Body.ExpectedUpdatedAt != nil, request.Body.ExpectedUpdatedAt != nil && request.Body.ExpectedUpdatedAt.Equal(current.UpdatedAt.Time))
	if errors.Is(err, errEndpointRevision) {
		return UpdateDownloadClient409JSONResponse{Code: "ENDPOINT_VERSION_CONFLICT", Message: "endpoint changed or preservation version missing; reload before editing", RequestId: reqID}, nil
	}
	if err != nil {
		return UpdateDownloadClient400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL when changing the endpoint", RequestId: reqID}, nil
	}
	row, err := q.EditDownloadClient(ctx, db.EditDownloadClientParams{
		ID:                request.Id,
		Name:              request.Body.Name,
		ClientType:        string(request.Body.ClientType),
		Url:               endpointURL,
		Username:          request.Body.Username,
		PasswordEncrypted: encPass,
	})
	if err != nil {
		return UpdateDownloadClient404JSONResponse{Code: "NOT_FOUND", Message: "download client not found", RequestId: reqID}, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return UpdateDownloadClient200JSONResponse{
		Id:         row.ID,
		Name:       row.Name,
		ClientType: row.ClientType,
		Url:        endpointDisplayURL(row.Url),
		UpdatedAt:  row.UpdatedAt.Time,
		Username:   row.Username,
	}, nil
}

// TestDownloadClient handles POST /api/v1/download-clients/{id}/test
func (s *ServerHandler) TestDownloadClient(ctx context.Context, request TestDownloadClientRequestObject) (TestDownloadClientResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return TestDownloadClient401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.dlMgr == nil {
		return TestDownloadClient200JSONResponse{Alive: false, Message: "download manager unavailable"}, nil
	}

	alive, msg, _ := s.dlMgr.TestClient(ctx, request.Id)
	return TestDownloadClient200JSONResponse{
		Alive:   alive,
		Message: msg,
	}, nil
}

// ListMediaServers handles GET /api/v1/media-servers
func (s *ServerHandler) ListMediaServers(ctx context.Context, request ListMediaServersRequestObject) (ListMediaServersResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return ListMediaServers401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	rows, err := s.app.Queries().ListMediaServers(ctx)
	if err != nil {
		return ListMediaServers200JSONResponse{Items: []MediaServerDetails{}}, nil
	}

	items := make([]MediaServerDetails, len(rows))
	for i, r := range rows {
		items[i] = MediaServerDetails{
			Id:         r.ID,
			Name:       r.Name,
			ServerType: r.ServerType,
			Url:        endpointDisplayURL(r.Url),
			UpdatedAt:  r.UpdatedAt.Time,
			Health:     r.Health,
		}
	}
	return ListMediaServers200JSONResponse{Items: items}, nil
}

// CreateMediaServer handles POST /api/v1/media-servers
func (s *ServerHandler) CreateMediaServer(ctx context.Context, request CreateMediaServerRequestObject) (CreateMediaServerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return CreateMediaServer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	encKey, err := crypto.Encrypt(s.masterKey, []byte(request.Body.ApiKey))
	if err != nil {
		return CreateMediaServer400JSONResponse{Code: "ENCRYPTION_FAILED", Message: "credential encryption unavailable", RequestId: reqID}, nil
	}
	if endpointURLHasMask(request.Body.Url) {
		return CreateMediaServer400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL", RequestId: reqID}, nil
	}
	pathMapBytes, _ := json.Marshal(request.Body.PathMapping)
	serverID := s.app.IDGen().NewUUID()

	row, err := s.app.Queries().CreateMediaServer(ctx, db.CreateMediaServerParams{
		ID:              serverID,
		Name:            request.Body.Name,
		ServerType:      string(request.Body.ServerType),
		Url:             request.Body.Url,
		ApiKeyEncrypted: encKey,
		PathMapping:     pathMapBytes,
	})
	if err != nil {
		return CreateMediaServer400JSONResponse{Code: "CREATE_FAILED", Message: "request could not be completed", RequestId: reqID}, nil
	}

	return CreateMediaServer201JSONResponse{
		Id:         row.ID,
		Name:       row.Name,
		ServerType: row.ServerType,
		Url:        endpointDisplayURL(row.Url),
		UpdatedAt:  row.UpdatedAt.Time,
		Health:     row.Health,
	}, nil
}

// DeleteMediaServer handles DELETE /api/v1/media-servers/{id}
func (s *ServerHandler) DeleteMediaServer(ctx context.Context, request DeleteMediaServerRequestObject) (DeleteMediaServerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return DeleteMediaServer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	_ = s.app.Queries().DeleteMediaServer(ctx, request.Id)
	return DeleteMediaServer204Response{}, nil
}

// UpdateMediaServer handles PUT /api/v1/media-servers/{id}
func (s *ServerHandler) UpdateMediaServer(ctx context.Context, request UpdateMediaServerRequestObject) (UpdateMediaServerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return UpdateMediaServer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	var encKey []byte
	if request.Body.ApiKey != "" {
		var err error
		encKey, err = crypto.Encrypt(s.masterKey, []byte(request.Body.ApiKey))
		if err != nil {
			return nil, err
		}
	}

	tx, q, err := s.beginEndpointEdit(ctx)
	if errors.Is(err, ports.ErrForbidden) {
		return UpdateMediaServer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := q.LockMediaServerForEdit(ctx, request.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpdateMediaServer404JSONResponse{Code: "NOT_FOUND", Message: "endpoint not found", RequestId: reqID}, nil
	}
	if err != nil {
		return nil, err
	}
	if request.Body.ApiKey == "" {
		encKey = current.ApiKeyEncrypted
	}
	endpointURL, err := resolveEndpointEditURL(request.Body.Url, current.Url, request.Body.ExpectedUpdatedAt != nil, request.Body.ExpectedUpdatedAt != nil && request.Body.ExpectedUpdatedAt.Equal(current.UpdatedAt.Time))
	if errors.Is(err, errEndpointRevision) {
		return UpdateMediaServer409JSONResponse{Code: "ENDPOINT_VERSION_CONFLICT", Message: "endpoint changed or preservation version missing; reload before editing", RequestId: reqID}, nil
	}
	if err != nil {
		return UpdateMediaServer400JSONResponse{Code: "INVALID_ENDPOINT_URL", Message: "provide a complete endpoint URL when changing the endpoint", RequestId: reqID}, nil
	}
	row, err := q.EditMediaServer(ctx, db.EditMediaServerParams{
		ID:              request.Id,
		Name:            request.Body.Name,
		ServerType:      string(request.Body.ServerType),
		Url:             endpointURL,
		ApiKeyEncrypted: encKey,
	})
	if err != nil {
		return UpdateMediaServer404JSONResponse{Code: "NOT_FOUND", Message: "media server not found", RequestId: reqID}, nil
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return UpdateMediaServer200JSONResponse{
		Id:         row.ID,
		Name:       row.Name,
		ServerType: row.ServerType,
		Url:        endpointDisplayURL(row.Url),
		UpdatedAt:  row.UpdatedAt.Time,
		Health:     row.Health,
	}, nil
}

// TestMediaServer handles POST /api/v1/media-servers/{id}/test
func (s *ServerHandler) TestMediaServer(ctx context.Context, request TestMediaServerRequestObject) (TestMediaServerResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() {
		return TestMediaServer401JSONResponse{Code: "UNAUTHORIZED", Message: "administrator access required", RequestId: reqID}, nil
	}

	if s.syncSvc == nil {
		return TestMediaServer200JSONResponse{Alive: false, Message: "sync service unavailable"}, nil
	}

	alive, msg, _ := s.syncSvc.TestServer(ctx, request.Id)
	return TestMediaServer200JSONResponse{
		Alive:   alive,
		Message: msg,
	}, nil
}

// GetTMDBMovieDetails handles GET /api/v1/media/tmdb/movie/{id}
func (s *ServerHandler) GetTMDBMovieDetails(ctx context.Context, request GetTMDBMovieDetailsRequestObject) (GetTMDBMovieDetailsResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetTMDBMovieDetails401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if s.metaProvider == nil {
		return GetTMDBMovieDetails404JSONResponse{Code: "NOT_FOUND", Message: "metadata provider unavailable", RequestId: reqID}, nil
	}

	meta, err := s.metaProvider.GetMovieDetails(ctx, request.Id, "zh-CN")
	if err != nil {
		return GetTMDBMovieDetails404JSONResponse{Code: "NOT_FOUND", Message: "request could not be completed", RequestId: reqID}, nil
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
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetTMDBTVDetails401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if s.metaProvider == nil {
		return GetTMDBTVDetails404JSONResponse{Code: "NOT_FOUND", Message: "metadata provider unavailable", RequestId: reqID}, nil
	}

	tv, err := s.metaProvider.GetTVDetails(ctx, request.Id, "zh-CN")
	if err != nil {
		return GetTMDBTVDetails404JSONResponse{Code: "NOT_FOUND", Message: "request could not be completed", RequestId: reqID}, nil
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
		Seasons:       seasons,
	}, nil
}

// GetDiscoverFeed handles GET /api/v1/discover
func (s *ServerHandler) GetDiscoverFeed(ctx context.Context, request GetDiscoverFeedRequestObject) (GetDiscoverFeedResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return GetDiscoverFeed401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	category := "trending"
	if request.Params.Category != nil {
		category = string(*request.Params.Category)
	}
	page := 1
	if request.Params.Page != nil && *request.Params.Page > 0 {
		page = *request.Params.Page
	}

	var results []DiscoverMediaItem

	if s.metaProvider != nil {
		items, err := s.metaProvider.GetDiscoverFeed(ctx, category, page)
		if err == nil {
			results = make([]DiscoverMediaItem, len(items))
			for i, m := range items {
				mType := DiscoverMediaItemMediaType(m.MediaType)
				if mType != "movie" && mType != "tv" {
					mType = "movie"
				}
				var ratingPtr *float32
				if m.Rating > 0 {
					r := float32(m.Rating)
					ratingPtr = &r
				}
				results[i] = DiscoverMediaItem{
					TmdbId:      m.TMDBID,
					Title:       m.Title,
					Year:        &m.Year,
					MediaType:   mType,
					Overview:    &m.Overview,
					PosterUrl:   &m.PosterURL,
					BackdropUrl: &m.BackdropURL,
					Rating:      ratingPtr,
					Genres:      &m.Genres,
				}
			}
			return GetDiscoverFeed200JSONResponse{Results: results}, nil
		}
	}

	return GetDiscoverFeed200JSONResponse{Results: []DiscoverMediaItem{}}, nil
}

// SyncDoubanWishlist handles POST /api/v1/sync/douban
func (s *ServerHandler) SyncDoubanWishlist(ctx context.Context, request SyncDoubanWishlistRequestObject) (SyncDoubanWishlistResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return SyncDoubanWishlist401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	if s.subSvc == nil {
		cnt := 0
		msg := "subscription service unavailable"
		return SyncDoubanWishlist200JSONResponse{CreatedCount: &cnt, Message: &msg}, nil
	}

	rootID := s.soleConfiguredRoot(ctx, "movie")
	syncSvc := subscription.NewDoubanSyncService(s.subSvc, s.metaProvider, nil)
	count, err := syncSvc.SyncUserWishlist(ctx, request.Body.DoubanUid, actor, rootID)
	msg := fmt.Sprintf("已成功从豆瓣同步 %d 部想看影视并转入追更订阅！", count)
	if err != nil {
		msg = fmt.Sprintf("同步部分完成 (%d 部): %v", count, err)
	}

	return SyncDoubanWishlist200JSONResponse{
		CreatedCount: &count,
		Message:      &msg,
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

// RescrapeMedia handles POST /api/v1/media/scrape
func (s *ServerHandler) RescrapeMedia(ctx context.Context, request RescrapeMediaRequestObject) (RescrapeMediaResponseObject, error) {
	reqID := GetRequestID(ctx)
	actor := GetActor(ctx)
	if actor == nil {
		return RescrapeMedia401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: reqID}, nil
	}

	// This endpoint has no file-scoped scraper or verified output yet.
	// Never report filesystem side effects that were not performed.
	succ := false
	count := 0
	msg := "刮削任务尚未接入媒体文件与 NFO 写入流程；未修改任何文件"

	return RescrapeMedia200JSONResponse{
		Success:      &succ,
		UpdatedCount: &count,
		Message:      &msg,
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
