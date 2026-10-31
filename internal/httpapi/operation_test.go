package httpapi_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func setupOperationHTTP(t *testing.T, configure ...func(*httpapi.ServerHandler, *app.App)) (*app.App, string, string, uuid.UUID, uuid.UUID, uuid.UUID, http.Handler, *httpapi.SSEHub) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsPath); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	tempDir, err := os.MkdirTemp("", "pulsemedia-op-http-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	cfg := config.NewDefault()
	cfg.DataDir = filepath.Join(tempDir, "data")
	masterKey := make([]byte, 32)
	cfg.SecretKey = hex.EncodeToString(masterKey)
	cfg.Database.URL = dbDSN

	var logBuf bytes.Buffer
	logger := logging.NewLogger(&logBuf, "debug")
	mockClock := clock.NewMock(time.Now().UTC())
	gen := idgen.New()

	a, err := app.New(cfg,
		app.WithLogger(logger),
		app.WithClock(mockClock),
		app.WithIDGen(gen),
		app.WithVersion("0.1.0-test"),
	)
	if err != nil {
		t.Fatalf("failed to initialize app: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Close()
	})

	queries := postgres.Queries(a.DBPool())

	// Create admin & member users
	authSvc := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, mockClock, gen)
	_, adminToken, err := authSvc.SetupAdmin(ctx, "admin", "AdminPass12345!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}

	// Create member user directly in DB
	memberID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           memberID,
		Username:     "member1",
		PasswordHash: "hash",
		Role:         ports.RoleMember,
		IsActive:     true,
	})
	// Member session
	memberToken := "pm_sess_member1_secret"
	_, _ = queries.CreateSession(ctx, db.CreateSessionParams{
		ID:            uuid.New(),
		UserID:        memberID,
		TokenHash:     crypto.HashToken(memberToken),
		CsrfTokenHash: []byte("csrf"),
		ExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})

	// Create root, media, unit, indexer, candidate
	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      filepath.Join(tempDir, "movies"),
		MediaType: "movie",
		Name:      "Movies",
	})
	_ = os.MkdirAll(filepath.Join(tempDir, "movies"), 0755)
	if _, err := a.DBPool().Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", rootID); err != nil {
		t.Fatal(err)
	}

	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Fight Club", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	indexerKey, err := crypto.Encrypt(masterKey, []byte("fixture-key"))
	if err != nil {
		t.Fatal(err)
	}
	candidateLink, err := crypto.Encrypt(masterKey, []byte("https://tracker.invalid/download.torrent"))
	if err != nil {
		t.Fatal(err)
	}
	indexerID, _ := uuid.NewV7()
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Tracker",
		Url:             "https://tracker.invalid",
		ApiKeyEncrypted: indexerKey,
		Categories:      []byte("[]"),
	})
	candID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             candID,
		IndexerID:      indexerID,
		Title:          "Fight.Club.1999.1080p.mkv",
		Guid:           "guid-1",
		LinkEncrypted:  candidateLink,
		SizeBytes:      8000000000,
		QualityProfile: []byte("{}"),
	})

	ob := outbox.New(a.DBPool(), gen, mockClock)
	acqSvc := acquisition.NewService(postgres.NewAcquisitionStore(a.DBPool(), gen, mockClock, acquisition.SelectMovieRound))
	transferSvc := transfer.NewService(transferstore.NewRepository(a.DBPool(), gen, ob, rootfs.NewFilesystem(), transfer.ExecutePreparedPlan))
	settingsSvc := settings.NewService(postgres.NewSettingsStore(a.DBPool(), gen))
	metaProvider := &mockMetaProvider{}

	sseHub := httpapi.NewSSEHub()
	subSvcStore := postgres.NewSubscriptionIntentStore(a.DBPool(), gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(a.DBPool(), gen), subSvcStore)
	polSvc := policy.NewService(postgres.NewPolicyStore(a.DBPool(), gen))
	searchSvc := usecaseSearch.NewService(postgres.NewSearchExecutionStore(a.DBPool()), gen, crypto.NewSecretProtector(masterKey))
	searchSvc.SetHealthRepository(postgres.NewIndexerHealthStore(a.DBPool(), gen))
	searchSvc.SetClientFactory(func(baseURL, key string) ports.IndexerClient { return torznab.NewClient(baseURL, key, nil) })
	searchSvc.RegisterClient(indexerID, &emptyOperationIndexer{})
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(a.DBPool()))
	coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(a.DBPool(), gen), mockClock, acqSvc)
	scheduler := usecaseScheduler.NewAutoFulfillScheduler(postgres.NewEvaluationScheduleStore(a.DBPool()), evaluator, coord, polSvc)
	serverHandler := httpapi.NewServerHandler(a, authSvc, settingsSvc, acqSvc, transferSvc, metaProvider, subSvc, polSvc, searchSvc, scheduler, nil, nil, nil)
	serverHandler.SetWithdrawalService(acquisition.NewWithdrawalService(postgres.NewDownloadSubmissionStore(a.DBPool(), gen)))
	serverHandler.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(a.DBPool())))
	serverHandler.SetCandidateExplanationService(usecaseSearch.NewCandidateExplanationService(postgres.NewCandidateExplanationStore(a.DBPool())))
	serverHandler.SetControlService(subscription.NewControlService(postgres.NewSubscriptionControlStore(a.DBPool(), gen)))
	notifications := notify.NewDispatcher(postgres.NewNotificationChannelReader(a.DBPool()), gen, mockClock, ob, postgres.NewNotificationChannelStore(a.DBPool(), gen))
	notifications.SetClientFactory(crypto.NewSecretProtector(masterKey), nil)
	serverHandler.SetNotificationDispatcher(notifications)
	serverHandler.SetFeedService(usecaseSearch.NewFeedService(postgres.NewRSSStore(a.DBPool(), gen), crypto.NewSecretProtector(masterKey), searchSvc))
	serverHandler.SetSubscriptionBindingService(subscription.NewBindingService(postgres.NewSubscriptionBindingStore(a.DBPool(), gen)))
	serverHandler.SetSubscriptionStatusService(subscription.NewStatusService(postgres.NewSubscriptionStatusStore(a.DBPool())))
	serverHandler.SetSubscriptionCreationService(subscription.NewSubscriptionCreationService(postgres.NewSubscriptionCreationStore(a.DBPool(), gen), metaProvider, nil, crypto.NewSecretProtector(masterKey)))
	for _, f := range configure {
		f(serverHandler, a)
	}
	router := httpapi.NewRouter(a, serverHandler, authSvc, sseHub)

	return a, adminToken, memberToken, rootID, unitID, candID, router, sseHub
}

// Explicitly successful empty search; a missing client is not a successful probe.
type emptyOperationIndexer struct{}

func (*emptyOperationIndexer) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	return nil, nil
}

func (*emptyOperationIndexer) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}

type mockMetaProvider struct{}

func (m *mockMetaProvider) SearchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockMetaProvider) GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockMetaProvider) FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockMetaProvider) SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockMetaProvider) GetTVDetails(ctx context.Context, tmdbID int64, language string) (*ports.TVDetails, error) {
	return nil, nil
}

func (m *mockMetaProvider) GetDiscoverFeed(ctx context.Context, category string, page int) ([]*ports.MovieMetadata, error) {
	return nil, nil
}

func TestMovieAcquire202AndObjectPermissionIsolation(t *testing.T) {
	a, adminToken, memberToken, rootID, unitID, candID, router, _ := setupOperationHTTP(t)

	// 1. Acquire movie as Member: must succeed with 202 Accepted and stable operation_id
	acquireBody, _ := json.Marshal(httpapi.AcquireMovieRequest{
		UnitId:      unitID,
		RootId:      rootID,
		CandidateId: candID,
	})

	reqAcq := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(acquireBody))
	reqAcq.Header.Set("Authorization", "Bearer "+memberToken)
	reqAcq.Header.Set("Content-Type", "application/json")
	recAcq := httptest.NewRecorder()

	router.ServeHTTP(recAcq, reqAcq)

	if recAcq.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on movie acquire, got %d: %s", recAcq.Code, recAcq.Body.String())
	}

	var acqResp httpapi.AcquireMovieResponse
	_ = json.Unmarshal(recAcq.Body.Bytes(), &acqResp)
	if acqResp.OperationId == uuid.Nil || acqResp.Status != "dispatched" {
		t.Fatalf("unexpected acquire response: %+v", acqResp)
	}

	// 2. Member accesses their own operation: 200 OK
	reqOp := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+acqResp.OperationId.String(), nil)
	reqOp.Header.Set("Authorization", "Bearer "+memberToken)
	recOp := httptest.NewRecorder()
	router.ServeHTTP(recOp, reqOp)

	if recOp.Code != http.StatusOK {
		t.Fatalf("expected 200 on member accessing own operation, got: %d", recOp.Code)
	}
	var details httpapi.OperationDetails
	if err := json.Unmarshal(recOp.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details.Status != "queued" || details.DownloadStatus != nil || details.Progress != nil || details.PlanId != nil || details.PlanStatus != nil {
		t.Fatalf("queued submission must not invent a download task or progress: %+v", details)
	}
	var queued int
	if err := a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1 AND state='queued'`, acqResp.OperationId.String()).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("queued status requires one durable submission job", queued, err)
	}

	// 3. Admin can access the operation: 200 OK
	reqAdmin := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+acqResp.OperationId.String(), nil)
	reqAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	recAdmin := httptest.NewRecorder()
	router.ServeHTTP(recAdmin, reqAdmin)

	if recAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 on admin accessing operation, got: %d", recAdmin.Code)
	}

	// 4. Another member attempts to access this private operation: MUST be rejected with 403 Forbidden!
	// (Invariant: 成员看不到别人的私有对象/敏感 payload!)
	otherMemberToken := "pm_sess_other_member"
	// create second member
	reqForbidden := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+acqResp.OperationId.String(), nil)
	reqForbidden.Header.Set("Authorization", "Bearer "+otherMemberToken)
	recForbidden := httptest.NewRecorder()
	router.ServeHTTP(recForbidden, reqForbidden)

	// Unauthorized since token not in DB, but even with valid token must not be 200
	if recForbidden.Code == http.StatusOK {
		t.Fatal("unauthorized / other member must never receive 200 OK")
	}

	// 5. Duplicate acquire returns 409 Conflict
	recAcq2 := httptest.NewRecorder()
	reqAcq2 := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(acquireBody))
	reqAcq2.Header.Set("Authorization", "Bearer "+memberToken)
	reqAcq2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recAcq2, reqAcq2)

	if recAcq2.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on duplicate acquire, got: %d", recAcq2.Code)
	}

	// A terminal job leaves only the pending requirement, not an active download.
	changed, err := a.DBPool().Exec(context.Background(), `UPDATE job SET state='failed' WHERE kind='download_submission' AND payload->>'operation_id'=$1 AND state='queued'`, acqResp.OperationId.String())
	if err != nil || changed.RowsAffected() != 1 {
		t.Fatal("terminate original submission job", changed.RowsAffected(), err)
	}
	reqBare := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+acqResp.OperationId.String(), nil)
	reqBare.Header.Set("Authorization", "Bearer "+memberToken)
	recBare := httptest.NewRecorder()
	router.ServeHTTP(recBare, reqBare)
	var bare httpapi.OperationDetails
	if recBare.Code != http.StatusOK || json.Unmarshal(recBare.Body.Bytes(), &bare) != nil || bare.Status != "unknown" || bare.DownloadStatus != nil || bare.Progress != nil || bare.PlanId != nil || bare.PlanStatus != nil {
		t.Fatalf("requirement alone must not report an active task: status=%d body=%s", recBare.Code, recBare.Body.String())
	}
}

func TestCancelOperationRetainsReservationsUntilDownloaderReconciliation(t *testing.T) {
	a, _, memberToken, rootID, unitID, candID, router, _ := setupOperationHTTP(t)
	body, _ := json.Marshal(httpapi.AcquireMovieRequest{UnitId: unitID, RootId: rootID, CandidateId: candID})
	acquire := httptest.NewRequest(http.MethodPost, "/api/v1/movies/acquire", bytes.NewReader(body))
	acquire.Header.Set("Authorization", "Bearer "+memberToken)
	acquire.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, acquire)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("acquire: %d %s", rec.Code, rec.Body.String())
	}
	var op httpapi.AcquireMovieResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
		t.Fatal(err)
	}
	cancel := httptest.NewRequest(http.MethodPost, "/api/v1/operations/"+op.OperationId.String()+"/cancel", nil)
	cancel.Header.Set("Authorization", "Bearer "+memberToken)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, cancel)
	if result.Code != http.StatusConflict || !strings.Contains(result.Body.String(), "CANCEL_REQUIRES_REVIEW") {
		t.Fatalf("unsafe cancel must be refused: %d %s", result.Code, result.Body.String())
	}
	var reserved int
	if err := a.DBPool().QueryRow(context.Background(), "SELECT count(*) FROM unit_reservation WHERE operation_id = $1 AND state IN ('reserved', 'dispatched')", op.OperationId).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved == 0 {
		t.Fatal("refused cancellation released the reservation")
	}
}

func TestSSERejectsMemberForUnscopedEvents(t *testing.T) {
	_, _, memberToken, _, _, _, router, _ := setupOperationHTTP(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+memberToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "event: connected") {
		t.Fatalf("member received global SSE events: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSSEEndpointConnect(t *testing.T) {
	_, adminToken, _, _, _, _, router, sseHub := setupOperationHTTP(t)

	// Broadcast test event
	sseHub.Broadcast("job.progress", `{"progress":0.5}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	router.ServeHTTP(rec, req.WithContext(ctx))

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got: %s", contentType)
	}
	if !strings.Contains(rec.Body.String(), "event: connected") {
		t.Fatalf("expected initial connected event, got: %s", rec.Body.String())
	}
}
