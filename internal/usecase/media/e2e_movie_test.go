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

	"github.com/dalamudx/PulseMedia/internal/domain/media"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

type e2eMockIndexerClient struct {
	releases []*ports.ReleaseItem
}

func (m *e2eMockIndexerClient) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	return m.releases, nil
}

func (m *e2eMockIndexerClient) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	return []byte("fake-torrent-file-content"), nil
}

type e2eMockDownloader struct {
	failOnce     bool
	torrents     map[string]*ports.TorrentInfo
	metainfo     []byte
	expectedHash string
	adds         int
}

func (m *e2eMockDownloader) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if p.DownloadURL != "" || p.InfoHash != m.expectedHash || !sameMovieMetainfo(p.TorrentBytes, m.metainfo) {
		return ports.ErrInvalidTorrentMetainfo
	}
	m.adds++
	if m.failOnce {
		m.failOnce = false
		// Simulate network timeout where remote client actually accepted but response dropped
		m.torrents[p.InfoHash] = &ports.TorrentInfo{
			InfoHash:    p.InfoHash,
			SavePath:    p.SavePath,
			ContentPath: p.SavePath + "/Fight.Club.1999.1080p.mkv",
			Progress:    0.1,
			State:       "downloading",
		}
		return ports.ErrTimeout
	}
	m.torrents[p.InfoHash] = &ports.TorrentInfo{
		InfoHash:    p.InfoHash,
		SavePath:    p.SavePath,
		ContentPath: p.SavePath + "/Fight.Club.1999.1080p.mkv",
		Progress:    1.0,
		State:       "completed",
	}
	return nil
}

func (m *e2eMockDownloader) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	if t, ok := m.torrents[infoHash]; ok {
		return t, nil
	}
	return nil, ports.ErrNotFound
}

func (m *e2eMockDownloader) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	out := make([]*ports.TorrentInfo, 0, len(m.torrents))
	for _, t := range m.torrents {
		out = append(out, t)
	}
	return out, nil
}

func (m *e2eMockDownloader) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	delete(m.torrents, infoHash)
	return nil
}

func (m *e2eMockDownloader) Test(ctx context.Context) error {
	return nil
}

type e2eMockMediaServer struct {
	refreshedPaths []string
	indexedFiles   map[string]bool
}

func (m *e2eMockMediaServer) RefreshPaths(ctx context.Context, paths []string) error {
	m.refreshedPaths = append(m.refreshedPaths, paths...)
	for _, p := range paths {
		m.indexedFiles[p] = true
	}
	return nil
}

func (m *e2eMockMediaServer) FindByPath(ctx context.Context, path string) (string, bool, error) {
	if m.indexedFiles[path] {
		return "jellyfin-item-550", true, nil
	}
	return "", false, nil
}

func (m *e2eMockMediaServer) Test(ctx context.Context) error {
	return nil
}

func TestFullMovieEndToEndLifecycle(t *testing.T) {
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

	tempDir, err := os.MkdirTemp("", "pulsemedia-e2e-movie-*")
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

	// Create root marker (.pulsemedia_root) to prove real storage mount
	_ = os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("root-1"), 0644)

	// 1. Setup Admin Account
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, mockClock, gen)
	adminUser, _, err := authSvc.SetupAdmin(ctx, "admin", "StrongPass12345!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   adminUser.ID.String(),
		Name: adminUser.Username,
		Role: ports.RoleAdmin,
	}

	// 2. Register Library Root in DB & verify baseline scanner
	rootID, _ := uuid.NewV7()
	root, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      moviesDir,
		MediaType: "movie",
		Name:      "Movies Main",
	})
	if err != nil {
		t.Fatalf("create root failed: %v", err)
	}

	scanner := library.NewScanner(gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool))
	scanReport, err := scanner.ScanRoot(ctx, root.ID)
	if err != nil {
		t.Fatalf("initial scan failed: %v", err)
	}
	if scanReport.FilesObserved != 0 {
		t.Fatalf("expected empty initial root")
	}

	updatedRoot, _ := queries.GetLibraryRootByID(ctx, root.ID)
	if !updatedRoot.BaselineCompleted {
		t.Fatal("expected baseline completed after scan")
	}

	// 3. Search Metadata (TMDB) & Create Media/Unit
	mockMetaProvider := &mockMetadataProvider{
		movies: map[string]*ports.MovieMetadata{
			"Fight Club": {
				TMDBID:        550,
				IMDbID:        "tt0137523",
				Title:         "Fight Club",
				OriginalTitle: "Fight Club",
				Year:          1999,
			},
		},
	}

	matcher := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(pool, gen), mockMetaProvider)
	_ = matcher

	mediaID, _ := uuid.NewV7()
	mediaRow, _ := queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        mediaID,
		Title:     "Fight Club",
		MediaType: "movie",
	})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:      unitID,
		MediaID: mediaRow.ID,
		UnitKey: media.FormatMovieUnitKey(),
	})

	// 4. Indexer Release Search & Encrypted Link Persistence
	indexerID, _ := uuid.NewV7()
	source, rawMetainfo, infoHash, sourceCalls := movieE2ESource(t)
	encKey, _ := crypto.Encrypt(masterKey, []byte("fixture-source-key"))
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Tracker 1",
		Url:             source.URL,
		ApiKeyEncrypted: encKey,
		Categories:      []byte("[]"),
	})

	mockIndexer := &e2eMockIndexerClient{
		releases: []*ports.ReleaseItem{
			{
				Title:       "Fight.Club.1999.1080p.BluRay.x264-SPARKS",
				GUID:        "guid-sparks-550",
				DownloadURL: source.URL + "/dl/550.torrent?passkey=fixture-source-key",
				InfoHash:    infoHash,
				SizeBytes:   8589934592,
				Seeders:     88,
			},
		},
	}

	searchSvc := search.NewService(postgres.NewSearchExecutionStore(pool), gen, crypto.NewSecretProtector(masterKey))
	searchSvc.RegisterClient(indexerID, mockIndexer)

	searchRes, err := searchSvc.ExecuteSearch(ctx, indexerID, "Fight Club", 0)
	if err != nil || searchRes.ResultsCount != 1 {
		t.Fatalf("indexer search failed: %v", err)
	}

	var candID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT id FROM release_candidate WHERE guid = $1", "guid-sparks-550").Scan(&candID)
	if err != nil {
		t.Fatalf("failed to locate saved candidate: %v", err)
	}

	// 5. Atomic Acquisition & Unit Reservation
	ob := outbox.New(pool, gen, mockClock)
	acqSvc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, mockClock, acquisition.SelectMovieRound))

	acqRes, err := acqSvc.AcquireMovie(ctx, adminActor, acquisition.AcquireMovieCommand{
		UnitIDs:     []uuid.UUID{unitID},
		RootID:      root.ID,
		CandidateID: candID,
	})
	if err != nil {
		t.Fatalf("acquisition failed: %v", err)
	}
	if acqRes.OperationID == uuid.Nil {
		t.Fatal("expected valid operation ID")
	}

	// 6. Download Submission with Response Loss Recovery
	dlClientID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                dlClientID,
		Name:              "qBittorrent",
		ClientType:        "qbittorrent",
		Url:               "http://qbittorrent:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	mockDownloader := &e2eMockDownloader{
		failOnce: true, // simulate timeout / lost response on first Add
		torrents: make(map[string]*ports.TorrentInfo),
	}

	downloadRepo := downloadstore.NewRepository(pool, gen, masterKey)
	configureMovieE2ESource(downloadRepo, source)
	mockDownloader.metainfo, mockDownloader.expectedHash = rawMetainfo, infoHash
	dlMgr := download.NewManager(downloadRepo)
	dlMgr.RegisterClient(dlClientID, mockDownloader)

	// Submit triggers unknown state due to timeout
	dlTask, err := dlMgr.SubmitDownload(ctx, acqRes.OperationID, dlClientID, candID, downloadsDir)
	if err == nil || dlTask == nil || dlTask.State != "unknown" {
		t.Fatalf("expected submit to enter unknown state on lost response, got task=%+v, err=%v", dlTask, err)
	}

	if mockDownloader.adds != 1 || sourceCalls.Load() != 1 {
		t.Fatal("expected one source fetch and one same-byte Add despite the lost response")
	}
	// Reconcile checks downloader by infohash: matches and safely transitions to tracking!
	reconciledTask, err := dlMgr.ReconcileTask(ctx, acqRes.OperationID)
	if err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	if reconciledTask.State != "tracking" {
		t.Fatalf("expected task recovered to tracking, got: %s", reconciledTask.State)
	}

	// Download completes on disk
	downloadedFilePath := filepath.Join(downloadsDir, "Fight.Club.1999.1080p.mkv")
	moviePayload := []byte("REAL_MOVIE_VIDEO_BYTES_550_PAYLOAD")
	_ = os.WriteFile(downloadedFilePath, moviePayload, 0644)

	msID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaServer(ctx, db.CreateMediaServerParams{
		ID:              msID,
		Name:            "Jellyfin Local",
		ServerType:      "jellyfin",
		Url:             "http://jellyfin:8096",
		ApiKeyEncrypted: []byte("key"),
		PathMapping:     []byte("{}"),
	})
	// 7. Transfer Plan, Hardlink Execution & Crash Observation Recovery
	transferSvc := transfer.NewService(transferstore.NewRepository(pool, gen, ob, rootfs.NewFilesystem()))
	q := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecH264, CodecParserVersion: quality.CodecTokenParserVersion}

	plan, step, err := transferSvc.CreateMoviePlan(ctx, acqRes.OperationID, root.ID, unitID, downloadedFilePath, "Fight Club", 1999, q)
	if err != nil {
		t.Fatalf("create transfer plan failed: %v", err)
	}

	// Simulate pre-existing hardlink (e.g. crash right after os.Link before DB commit)
	_ = os.MkdirAll(filepath.Dir(step.TargetPath), 0755)
	_ = os.Link(downloadedFilePath, step.TargetPath)

	// ExecutePlan recognizes existing link, verifies same inode & same device, skips duplicate Link, records library file + imported fact!
	if err := transferSvc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("execute plan with observation recovery failed: %v", err)
	}

	// Verify post-conditions
	srcStatInfo, _ := os.Stat(downloadedFilePath)
	tgtStatInfo, _ := os.Stat(step.TargetPath)
	srcStat := srcStatInfo.Sys().(*syscall.Stat_t)
	tgtStat := tgtStatInfo.Sys().(*syscall.Stat_t)
	if srcStat.Dev != tgtStat.Dev || srcStat.Ino != tgtStat.Ino {
		t.Fatalf("hardlink failed: device/inode mismatch")
	}

	// Verify imported fact exists
	facts, err := queries.ListActiveFactsForUnit(ctx, unitID)
	if err != nil || len(facts) != 1 || facts[0].Stage != "imported" {
		t.Fatalf("expected 1 imported fact for movie unit: %+v", facts)
	}

	// 8. MediaServer Refresh & Confirmed Fact

	mockMS := &e2eMockMediaServer{
		refreshedPaths: make([]string, 0),
		indexedFiles:   make(map[string]bool),
	}
	syncSvc := mediaserver.NewSyncService(ob, postgres.NewMediaConfirmationRepository(pool, gen))
	syncSvc.RegisterClient(msID, mockMS)

	// Process refresh delivery from Outbox
	delivery, err := ob.Claim(ctx, "mediaserver.refresh:"+msID.String(), "worker_ms", 30*time.Second)
	if err != nil || delivery == nil {
		t.Fatalf("failed to claim mediaserver.refresh delivery from outbox: %v", err)
	}

	err = syncSvc.ProcessRefreshDelivery(ctx, delivery.ID, msID, "worker_ms", delivery.LeaseToken, step.TargetPath)
	if err != nil {
		t.Fatalf("process refresh delivery failed: %v", err)
	}
	if len(mockMS.refreshedPaths) != 1 {
		t.Fatal("expected media server refresh called")
	}

	// Confirm file
	var targetFileID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT target_file_id FROM transfer_step WHERE id = $1", step.ID).Scan(&targetFileID)
	if err != nil {
		t.Fatalf("failed to read step target_file_id: %v", err)
	}

	confirmedFact, err := syncSvc.ConfirmFile(ctx, msID, targetFileID, unitID)
	if err != nil || confirmedFact.Stage != "confirmed" {
		t.Fatalf("confirm file failed: %v", err)
	}

	// 9. Anti-Duplicate Protection: Attempting to acquire the same movie again must fail with ErrAlreadyOwned!
	_, err = acqSvc.AcquireMovie(ctx, adminActor, acquisition.AcquireMovieCommand{
		UnitIDs:     []uuid.UUID{unitID},
		RootID:      root.ID,
		CandidateID: candID,
	})
	if !errors.Is(err, acquisition.ErrAlreadyOwned) {
		t.Fatalf("expected ErrAlreadyOwned on repeated acquisition of existing movie: %v", err)
	}
}
