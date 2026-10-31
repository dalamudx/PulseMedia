package media_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func TestR3FaultMatrix(t *testing.T) {
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
	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-r3-faults-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	downloadsDir := filepath.Join(tempDir, "downloads")
	moviesDir := filepath.Join(tempDir, "movies")
	_ = os.MkdirAll(downloadsDir, 0755)
	_ = os.MkdirAll(moviesDir, 0755)

	// ---------------------------------------------------------
	// 故障场景 1: Add 丢响应 / 网络超时安全对账
	// ---------------------------------------------------------
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	source, rawMetainfo, infoHash, sourceCalls := movieE2ESource(t)
	repo := downloadstore.NewRepository(pool, gen, masterKey)
	configureMovieE2ESource(repo, source)
	dlMgr := download.NewManager(repo)
	dlClientID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                dlClientID,
		Name:              "qBittorrent Fault Test",
		ClientType:        "qbittorrent",
		Url:               "http://mock-qb:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	mockDL := &e2eMockDownloader{
		failOnce: true, // first Add times out
		torrents: make(map[string]*ports.TorrentInfo),
	}
	dlMgr.RegisterClient(dlClientID, mockDL)

	indexerID, _ := uuid.NewV7()
	encryptedKey, err := crypto.Encrypt(masterKey, []byte("fixture-source-key"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = queries.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexerID, Name: "Trk", Url: source.URL, ApiKeyEncrypted: encryptedKey, Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	mockDL.metainfo, mockDL.expectedHash = rawMetainfo, infoHash
	candID, _ := uuid.NewV7()
	encLink, _ := crypto.Encrypt(masterKey, []byte(source.URL+"/dl/550.torrent?passkey=fixture-source-key"))
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             candID,
		IndexerID:      indexerID,
		Title:          "Fault.Movie.1080p.mkv",
		Guid:           "guid-fault-1",
		LinkEncrypted:  encLink,
		InfoHash:       pgtype.Text{String: infoHash, Valid: true},
		SizeBytes:      5000000000,
		QualityProfile: []byte("{}"),
	})

	opID, _ := uuid.NewV7()
	// Add times out -> task enters unknown state
	task, err := dlMgr.SubmitDownload(ctx, opID, dlClientID, candID, downloadsDir)
	if err == nil {
		t.Fatal("expected submit error on timeout")
	}
	if task == nil {
		t.Fatalf("task was nil on submit: %v", err)
	}
	if task.State != "unknown" {
		t.Fatalf("expected unknown state on timeout, got: %s", task.State)
	}

	if mockDL.adds != 1 || sourceCalls.Load() != 1 {
		t.Fatal("R3 must fetch once and send the original validated bytes once")
	}
	// Reconcile recovers without duplicating side effects
	reconciled, err := dlMgr.ReconcileTask(ctx, opID)
	if err != nil || reconciled == nil || reconciled.State != "tracking" {
		t.Fatalf("expected tracking after reconcile: task=%+v, err=%v", reconciled, err)
	}

	// ---------------------------------------------------------
	// 故障场景 2: 挂载空目录 / 断网导致 marker 缺失
	// ---------------------------------------------------------
	rootID, _ := uuid.NewV7()
	root, _ := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      moviesDir,
		MediaType: "movie",
		Name:      "Movies Fault Test",
	})

	// Pre-create an active file in database
	fileID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileID,
		RootID:          rootID,
		RelPath:         "Existing.Movie (2020)/Existing.Movie.mkv",
		SizeBytes:       5000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})

	// moviesDir does NOT have .pulsemedia_root (simulating disconnected mount or empty mountpoint)
	scanner := library.NewScanner(gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool))
	_, scanErr := scanner.ScanRoot(ctx, root.ID)
	if !errors.Is(scanErr, library.ErrMarkerMissing) {
		t.Fatalf("expected ErrMarkerMissing for disconnected mount: %v", scanErr)
	}

	// Invariant: Root marked offline, existing files in database MUST NOT be deleted or marked missing!
	existingFile, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
		RootID:  rootID,
		RelPath: "Existing.Movie (2020)/Existing.Movie.mkv",
	})
	if err != nil || existingFile.RetiredAt.Valid {
		t.Fatalf("disconnected mount falsely removed existing file: %+v", existingFile)
	}

	// ---------------------------------------------------------
	// 故障场景 3: 整理中断与硬链接恢复
	// ---------------------------------------------------------
	// Put marker file to restore root online
	_ = os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("ok"), 0644)
	if _, err := queries.UpdateRootHealth(ctx, db.UpdateRootHealthParams{ID: rootID, Health: "online"}); err != nil {
		t.Fatalf("restore probed fixture root: %v", err)
	}

	// The source directory is explicitly configured; arbitrary /tmp paths are not capabilities.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: downloadsDir, MediaType: "movie", Name: "Downloads"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}

	srcPath := filepath.Join(downloadsDir, "Fight.Club.1080p.mkv")
	_ = os.WriteFile(srcPath, []byte("VIDEO_CONTENT_R3"), 0644)

	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Fight Club", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	mockClock := clock.NewMock(time.Now())
	ob := outbox.New(pool, gen, mockClock)
	transferSvc := transfer.NewService(transferstore.NewRepository(pool, gen, ob, rootfs.NewFilesystem(), transfer.ExecutePreparedPlan))
	q := quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray}

	plan, step, err := transferSvc.CreateMoviePlan(ctx, opID, rootID, unitID, srcPath, "Fight Club", 1999, q)
	if err != nil {
		t.Fatalf("create plan failed: %v", err)
	}

	// Simulate crash: hardlink was already created on disk, but process crashed before DB commit
	_ = os.MkdirAll(filepath.Dir(step.TargetPath), 0755)
	_ = os.Link(srcPath, step.TargetPath)

	// Execute plan: detects existing link with same inode, verifies post-conditions, and safely finishes
	if err := transferSvc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("interrupted plan execution recovery failed: %v", err)
	}

	// Verify same inode
	sInfo, _ := os.Stat(srcPath)
	tInfo, _ := os.Stat(step.TargetPath)
	sStat := sInfo.Sys().(*syscall.Stat_t)
	tStat := tInfo.Sys().(*syscall.Stat_t)
	if sStat.Ino != tStat.Ino || sStat.Dev != tStat.Dev {
		t.Fatalf("inode or device mismatch: src=%d, tgt=%d", sStat.Ino, tStat.Ino)
	}

	// ---------------------------------------------------------
	// 故障场景 4: 异构冲突触发人工复核 (Review Required)
	// ---------------------------------------------------------
	// If target file exists but has DIFFERENT content/inode
	plan2, step2, _ := transferSvc.CreateMoviePlan(ctx, uuid.New(), rootID, unitID, srcPath, "Fight Club 2", 1999, q)
	_ = os.MkdirAll(filepath.Dir(step2.TargetPath), 0755)
	_ = os.WriteFile(step2.TargetPath, []byte("DIFFERENT_INODE_CONTENT"), 0644) // different inode!

	err = transferSvc.ExecutePlan(ctx, plan2.ID)
	if !errors.Is(err, transfer.ErrReviewRequired) {
		t.Fatalf("expected ErrReviewRequired when target file has different inode, got: %v", err)
	}

	plan2Row, _ := queries.GetTransferPlanByID(ctx, plan2.ID)
	if plan2Row.State != "review_required" {
		t.Fatalf("expected plan state review_required, got: %s", plan2Row.State)
	}
}
