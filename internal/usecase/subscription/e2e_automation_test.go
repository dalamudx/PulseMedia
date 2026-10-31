package subscription_test

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

type e2eAutoMockDownloader struct {
	torrents     map[string]*ports.TorrentInfo
	metainfo     []byte
	expectedHash string
	adds         int
}

func (m *e2eAutoMockDownloader) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if p.DownloadURL != "" || p.InfoHash != m.expectedHash || !bytes.Equal(p.TorrentBytes, m.metainfo) {
		return ports.ErrInvalidTorrentMetainfo
	}
	m.adds++
	m.torrents[p.InfoHash] = &ports.TorrentInfo{
		InfoHash:    p.InfoHash,
		SavePath:    p.SavePath,
		ContentPath: p.SavePath + "/Inception.2010.2160p.mkv",
		Progress:    1.0,
		State:       "completed",
	}
	return nil
}

func (m *e2eAutoMockDownloader) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	if t, ok := m.torrents[infoHash]; ok {
		return t, nil
	}
	return nil, ports.ErrNotFound
}

func (m *e2eAutoMockDownloader) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	out := make([]*ports.TorrentInfo, 0, len(m.torrents))
	for _, t := range m.torrents {
		out = append(out, t)
	}
	return out, nil
}

func (m *e2eAutoMockDownloader) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	delete(m.torrents, infoHash)
	return nil
}

func (m *e2eAutoMockDownloader) Test(ctx context.Context) error {
	return nil
}

func TestFullSubscriptionAutomationLifecycle(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	gen := idgen.New()
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-e2e-auto-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	moviesDir := filepath.Join(tempDir, "movies")
	downloadsDir := filepath.Join(tempDir, "downloads")
	_ = os.MkdirAll(moviesDir, 0755)
	_ = os.MkdirAll(downloadsDir, 0755)
	// Explicit source capability: a task save_path does not authorize a directory.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: downloadsDir, MediaType: "movie", Name: "Fixture download source"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}
	_ = os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("root-auto"), 0644)

	// 1. Create User
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, mockClock, gen)
	adminUser, _, err := authSvc.SetupAdmin(ctx, "admin", "AdminPassword123!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   adminUser.ID.String(),
		Name: adminUser.Username,
		Role: ports.RoleAdmin,
	}

	// 2. Register Root & Media
	rootID, _ := uuid.NewV7()
	root, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      moviesDir,
		MediaType: "movie",
		Name:      "Movies Auto",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", root.ID)

	mediaID, _ := uuid.NewV7()
	mediaRow, _ := queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        mediaID,
		Title:     "Inception",
		Year:      pgtype.Int4{Int32: 2010, Valid: true},
		MediaType: "movie",
	})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:      unitID,
		MediaID: mediaRow.ID,
		UnitKey: domainMedia.FormatMovieUnitKey(),
	})

	// 3. Register a configured loopback source and independently encode its metainfo.
	payload := []byte("INCEPTION_4K_DATA")
	piece := sha1.Sum(payload)
	info := append([]byte("d6:lengthi17e4:name24:Inception.2010.2160p.mkv12:piece lengthi16384e6:pieces20:"), piece[:]...)
	info = append(info, 'e')
	infoDigest := sha1.Sum(info)
	expectedInfoHash := hex.EncodeToString(infoDigest[:])
	rawMetainfo := append([]byte("d4:info"), info...)
	rawMetainfo = append(rawMetainfo, 'e')
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/dl/4k.torrent" {
			http.NotFound(w, r)
			return
		}
		sourceCalls.Add(1)
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(rawMetainfo)
	}))
	t.Cleanup(source.Close)
	credential, err := crypto.Encrypt(masterKey, []byte("fixture-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	indexerID, _ := uuid.NewV7()
	if _, err := queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID: indexerID, Name: "Tracker Auto", Url: source.URL,
		ApiKeyEncrypted: credential, Categories: []byte("[]"),
	}); err != nil {
		t.Fatal(err)
	}
	encLinkCam, _ := crypto.Encrypt(masterKey, []byte(source.URL+"/dl/cam.torrent"))
	candCamID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             candCamID,
		IndexerID:      indexerID,
		Title:          "Inception.2010.HDCAM-GROUP",
		Guid:           "guid-cam",
		LinkEncrypted:  encLinkCam,
		SizeBytes:      1500000000,
		QualityProfile: []byte("{}"),
	})

	encLink1080p, _ := crypto.Encrypt(masterKey, []byte(source.URL+"/dl/1080p.torrent"))
	cand1080pID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             cand1080pID,
		IndexerID:      indexerID,
		Title:          "Inception.2010.1080p.WEB-DL.x264",
		Guid:           "guid-1080p",
		LinkEncrypted:  encLink1080p,
		SizeBytes:      8000000000,
		QualityProfile: []byte("{}"),
	})

	encLink4K, _ := crypto.Encrypt(masterKey, []byte(source.URL+"/dl/4k.torrent"))
	cand4KID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             cand4KID,
		IndexerID:      indexerID,
		Title:          "Inception.2010.2160p.UHD.Remux.HEVC.DV",
		Guid:           "guid-4k",
		LinkEncrypted:  encLink4K,
		InfoHash:       pgtype.Text{String: expectedInfoHash, Valid: true},
		SizeBytes:      25000000000,
		QualityProfile: []byte("{}"),
	})

	// 4. Create Subscription (Intent Definition)
	ob := outbox.New(pool, gen, mockClock)
	acqSvc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, mockClock, acquisition.SelectMovieRound))
	polSvc := policy.NewService(postgres.NewPolicyStore(pool, gen))
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(pool, gen), mockClock, acqSvc)
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)
	scheduler := usecaseScheduler.NewAutoFulfillScheduler(postgres.NewEvaluationScheduleStore(pool), evaluator, coord, polSvc)

	sub, wantedItems, err := subSvc.CreateSubscription(ctx, adminActor, subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    root.ID,
		ScopeType: "all",
	})
	if err != nil || len(wantedItems) != 1 {
		t.Fatalf("create sub failed: %v", err)
	}
	wantedID := wantedItems[0].ID

	// 5. Automated Evaluation Batch -> Triggers SearchRound & Evaluates Candidates
	// Start SearchRound for wanted item
	round, err := coord.StartRound(ctx, wantedID, 30*time.Second)
	if err != nil {
		t.Fatalf("start search round failed: %v", err)
	}

	// Add candidates to round
	_ = coord.AddCandidate(ctx, round.ID, candCamID)
	_ = coord.AddCandidate(ctx, round.ID, cand1080pID)
	_ = coord.AddCandidate(ctx, round.ID, cand4KID)

	// Finalize round: CAM is rejected, 4K is chosen over 1080p (最佳候选择优)
	finRep, err := coord.FinalizeRound(ctx, adminActor, round.ID)
	if err != nil {
		t.Fatalf("finalize round failed: %v", err)
	}
	if finRep.State != "completed" || finRep.SelectedCandidateID == nil || *finRep.SelectedCandidateID != cand4KID {
		t.Fatalf("expected 4K candidate chosen: %+v", finRep)
	}

	// 6. Complete Download & Transfer to Library
	dlClientID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                dlClientID,
		Name:              "qB Auto",
		ClientType:        "qbittorrent",
		Url:               "http://qb:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})
	mockDownloader := &e2eAutoMockDownloader{torrents: make(map[string]*ports.TorrentInfo), metainfo: rawMetainfo, expectedHash: expectedInfoHash}
	downloadRepo := downloadstore.NewRepository(pool, gen, masterKey)
	downloadRepo.SetTorrentSource(func(_ string, endpoint, key string) ports.IndexerClient {
		if endpoint != source.URL || key != "fixture-source-key" {
			return nil
		}
		return torznab.NewClient(endpoint, key, source.Client())
	}, torrent.Inspector{})
	dlMgr := download.NewManager(downloadRepo)
	dlMgr.RegisterClient(dlClientID, mockDownloader)

	_, err = dlMgr.SubmitDownload(ctx, finRep.OperationID, dlClientID, cand4KID, downloadsDir)
	if err != nil {
		t.Fatalf("submit download failed: %v", err)
	}
	if mockDownloader.adds != 1 || sourceCalls.Load() != 1 || len(mockDownloader.torrents) != 1 || mockDownloader.torrents[expectedInfoHash] == nil {
		t.Fatal("expected one configured-source fetch and one Add of exactly the validated metainfo")
	}

	// Create downloaded file on disk
	downloadedFile := filepath.Join(downloadsDir, "Inception.2010.2160p.mkv")
	if err := os.WriteFile(downloadedFile, payload, 0644); err != nil {
		t.Fatal(err)
	}

	// Hardlink transfer into library root
	transferSvc := transfer.NewService(transferstore.NewRepository(pool, gen, ob, rootfs.NewFilesystem(), transfer.ExecutePreparedPlan))
	qProf := quality.Profile{Resolution: quality.Res2160p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceRemux, SourceParserVersion: quality.SourceTokenParserVersion}
	plan, _, err := transferSvc.CreateMoviePlan(ctx, finRep.OperationID, root.ID, unitID, downloadedFile, "Inception", 2010, qProf)
	if err != nil {
		t.Fatalf("create transfer plan failed: %v", err)
	}
	if err := transferSvc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("execute transfer plan failed: %v", err)
	}

	// 7. Evaluator re-evaluates WantedItem: now SATISFIED based on imported fact!
	rules, _ := polSvc.GetRevision(ctx, "default", 1)
	polObj := quality.Policy{
		ID:              "default",
		Revision:        1,
		ResolutionOrder: rules.ResolutionOrder,
		SourceOrder:     rules.SourceOrder,
		PreferHDR:       rules.PreferHDR,
	}

	evalRep, err := evaluator.EvaluateWantedItem(ctx, wantedID, polObj)
	if err != nil {
		t.Fatalf("evaluate wanted item failed: %v", err)
	}
	if !evalRep.IsSatisfied || *evalRep.FactStage != "imported" {
		t.Fatalf("expected wanted item satisfied: %+v", evalRep)
	}

	// 8. Invariant: "删除可再次缺失" (Deleted File Re-missing Automation)
	// Physically delete/retire file from library root
	relPath := "Inception (2010)/Inception (2010) [Remux 2160p].mkv"
	_ = queries.RetireFileByPath(ctx, db.RetireFileByPathParams{
		RootID:  root.ID,
		RelPath: relPath,
	})

	// Re-evaluation immediately detects absence in root, resets is_satisfied=false, marks missing!
	evalRepAfterDelete, err := evaluator.EvaluateWantedItem(ctx, wantedID, polObj)
	if err != nil {
		t.Fatalf("evaluate after delete failed: %v", err)
	}
	if evalRepAfterDelete.IsSatisfied || evalRepAfterDelete.WaitReason != "missing" {
		t.Fatalf("expected missing after file retired: %+v", evalRepAfterDelete)
	}

	// Scheduler batch detects missing again and successfully initiates replenishment!
	processed, err := scheduler.RunEvaluationBatch(ctx, adminActor, 10)
	if err != nil || processed != 1 {
		t.Fatalf("scheduler should re-evaluate missing item: processed=%d, err=%v", processed, err)
	}

	_ = sub
}
