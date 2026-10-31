package download_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

const migrationsDir = "../../../migrations"
const ordinaryFixtureInfo = "d6:lengthi4e4:name15:Show.S01E01.mkv12:piece lengthi4e6:pieces20:00000000000000000000e"

func ordinaryFixtureHash() string {
	h := sha1.Sum([]byte(ordinaryFixtureInfo))
	return hex.EncodeToString(h[:])
}

type mockDownloadClient struct {
	failAdd  bool
	torrents map[string]*ports.TorrentInfo
}

func (m *mockDownloadClient) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if m.failAdd {
		return ports.ErrTimeout // simulate response loss / network timeout
	}
	m.torrents[p.InfoHash] = &ports.TorrentInfo{
		InfoHash:    p.InfoHash,
		SavePath:    p.SavePath,
		ContentPath: p.SavePath + "/Fight Club (1999)/Fight Club.mkv",
		Progress:    0.1,
		State:       "downloading",
	}
	return nil
}

func (m *mockDownloadClient) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	if t, ok := m.torrents[infoHash]; ok {
		return t, nil
	}
	return nil, ports.ErrNotFound
}

func (m *mockDownloadClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	results := make([]*ports.TorrentInfo, 0, len(m.torrents))
	for _, t := range m.torrents {
		results = append(results, t)
	}
	return results, nil
}

func (m *mockDownloadClient) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	delete(m.torrents, infoHash)
	return nil
}

func (m *mockDownloadClient) Test(ctx context.Context) error {
	return nil
}

func setupDownloadTest(t *testing.T) (*download.Manager, *db.Queries, []byte, uuid.UUID, uuid.UUID) {
	t.Helper()
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
	t.Cleanup(func() {
		pool.Close()
	})

	gen := idgen.New()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	mgr := download.NewManager(pool, gen, masterKey)
	queries := postgres.Queries(pool)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("d4:info" + ordinaryFixtureInfo + "e"))
	}))
	t.Cleanup(origin.Close)
	mgr.SetTorrentSource(func(_, endpoint, key string) ports.IndexerClient { return torznab.NewClient(endpoint, key, nil) }, torrent.Inspector{})
	credential, err := crypto.Encrypt(masterKey, []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	// Create indexer & candidate
	indexerID, _ := uuid.NewV7()
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Tracker",
		Url:             origin.URL,
		ApiKeyEncrypted: credential,
		Categories:      []byte("[]"),
	})

	encLink, _ := crypto.Encrypt(masterKey, []byte(origin.URL+"/torrent"))
	candID, _ := uuid.NewV7()
	infoHash := ordinaryFixtureHash()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{
		ID:                  candID,
		SourceConfigVersion: 1,
		IndexerID:           indexerID,
		Title:               "Fight.Club.1080p.mkv",
		Guid:                "guid-1",
		LinkEncrypted:       encLink,
		InfoHash:            pgtype.Text{String: infoHash, Valid: true},
		SizeBytes:           8000000000,
		QualityProfile:      []byte("{}"),
	})

	// Create client in DB
	clientID, _ := uuid.NewV7()
	_, err = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                clientID,
		Name:              "qBittorrent Local",
		ClientType:        "qbittorrent",
		Url:               "http://localhost:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})
	if err != nil {
		t.Fatalf("failed to create download client: %v", err)
	}

	return mgr, queries, masterKey, clientID, candID
}

func TestDownloadSubmissionLostResponseAndReconciliation(t *testing.T) {
	mgr, _, _, clientID, candID := setupDownloadTest(t)
	ctx := context.Background()

	mockClient := &mockDownloadClient{
		failAdd:  true, // simulate timeout / lost response on initial Add
		torrents: make(map[string]*ports.TorrentInfo),
	}
	mgr.RegisterClient(clientID, mockClient)

	opID, _ := uuid.NewV7()
	savePath := "/downloads/movies"

	// 1. Initial SubmitDownload fails due to network timeout
	task, err := submitDownload(mgr, ctx, opID, clientID, candID, savePath)
	if err == nil {
		t.Fatal("expected submit error on simulated timeout")
	}
	if task == nil || task.State != "unknown" {
		t.Fatalf("expected task in unknown state after response loss: %+v", task)
	}

	// 2. Client side actually received the torrent (Add succeeded but response was lost)
	mockClient.torrents[ordinaryFixtureHash()] = &ports.TorrentInfo{
		InfoHash:    ordinaryFixtureHash(),
		SavePath:    savePath,
		ContentPath: savePath + "/Fight Club (1999)/Fight Club.mkv",
		Progress:    0.25,
		TotalBytes:  8000000000,
		State:       "downloading",
	}

	// 3. Reconcile unknown task: finds torrent in client and safely transitions to tracking!
	reconciled, err := mgr.ReconcileTask(ctx, opID)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if reconciled.State != "tracking" || reconciled.Progress != 0.25 {
		t.Fatalf("expected task recovered to tracking: %+v", reconciled)
	}

	// 4. Repeated submission with same opID reuses existing task without altering path
	reused, err := submitDownload(mgr, ctx, opID, clientID, candID, "/different/path")
	if err != nil {
		t.Fatalf("reusing task failed: %v", err)
	}
	if reused.ID != task.ID || reused.SavePath != savePath {
		t.Fatalf("existing task must be reused and save_path preserved: %+v", reused)
	}
}

func TestReconcileUnknownHashOnlyByUniqueOperationTag(t *testing.T) {
	mgr, queries, _, clientID, candID := setupDownloadTest(t)
	ctx := context.Background()
	opID := uuid.New()
	_, err := queries.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{
		ID: uuid.New(), OperationID: opID, ClientID: clientID, CandidateID: candID,
		InfoHash: "", State: "unknown", SavePath: "/downloads/movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	tag := "pulsemedia:op:" + opID.String()
	hash := ordinaryFixtureHash()
	mock := &mockDownloadClient{torrents: map[string]*ports.TorrentInfo{
		hash:    {InfoHash: hash, Tags: "pulsemedia," + tag, SavePath: "/downloads/movies", Progress: 0.2},
		"other": {InfoHash: "abcd", Tags: "pulsemedia:op:" + uuid.NewString(), SavePath: "/downloads/movies"},
	}}
	mgr.RegisterClient(clientID, mock)
	task, err := mgr.ReconcileTask(ctx, opID)
	if err != nil || task.State != "tracking" || task.InfoHash != hash {
		t.Fatalf("tag recovery failed: %+v %v", task, err)
	}
}

func TestObserveTaskDoesNotDeclareImportOnCompleteOrMissingTorrent(t *testing.T) {
	mgr, queries, _, clientID, candID := setupDownloadTest(t)
	ctx := context.Background()
	opID := uuid.New()
	_, err := queries.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{
		ID: uuid.New(), OperationID: opID, ClientID: clientID, CandidateID: candID,
		InfoHash: "aaa", State: "tracking", SavePath: "/downloads/movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockDownloadClient{torrents: map[string]*ports.TorrentInfo{
		"aaa": {InfoHash: "aaa", SavePath: "/downloads/movies", ContentPath: "/downloads/movies/movie.mkv", Progress: 1},
	}}
	mgr.RegisterClient(clientID, mock)
	observed, err := mgr.ObserveTask(ctx, opID)
	if err != nil || observed.Progress != 1 || observed.State != "tracking" {
		t.Fatalf("complete download should remain tracking until verified import: %+v %v", observed, err)
	}
	delete(mock.torrents, "aaa")
	if _, err := mgr.ObserveTask(ctx, opID); !errors.Is(err, download.ErrReconcilePending) {
		t.Fatalf("disappeared torrent requires review: %v", err)
	}
	persisted, err := queries.GetDownloadTaskByOpID(ctx, opID)
	if err != nil || persisted.State != "unknown" {
		t.Fatalf("disappeared torrent lost unknown protection: %+v %v", persisted, err)
	}
	if _, err := queries.TransitionTaskToTracking(ctx, persisted.ID); err != nil {
		t.Fatal(err)
	}
	mock.torrents["aaa"] = &ports.TorrentInfo{InfoHash: "aaa", SavePath: "/outside", ContentPath: "/outside/movie.mkv", Progress: 1}
	if _, err := mgr.ObserveTask(ctx, opID); !errors.Is(err, download.ErrReconcilePending) {
		t.Fatalf("relocated torrent must be quarantined: %v", err)
	}
	persisted, err = queries.GetDownloadTaskByOpID(ctx, opID)
	if err != nil || persisted.State != "unknown" {
		t.Fatalf("relocated torrent lost reservation: %+v %v", persisted, err)
	}
}
func TestDeleteClientKeepsQueuedSubmissions(t *testing.T) {
	mgr, queries, _, clientID, _ := setupDownloadTest(t)
	ctx := context.Background()
	payload, err := json.Marshal(map[string]string{"client_id": clientID.String(), "operation_id": uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.EnqueueJob(ctx, db.EnqueueJobParams{
		ID: uuid.New(), Kind: "download_submission", Payload: payload,
		MaxAttempts: 3, NotBefore: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteClient(ctx, clientID); !errors.Is(err, download.ErrClientInUse) {
		t.Fatalf("queued job must protect client: %v", err)
	}
	if _, err := queries.GetDownloadClientByID(ctx, clientID); err != nil {
		t.Fatalf("client removed: %v", err)
	}
}
func TestDeleteClientKeepsDownloadTasks(t *testing.T) {
	mgr, queries, _, clientID, candID := setupDownloadTest(t)
	ctx := context.Background()
	opID := uuid.New()
	task, err := queries.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{
		ID: uuid.New(), OperationID: opID, ClientID: clientID, CandidateID: candID,
		InfoHash: "aaa", State: "unknown", SavePath: "/downloads",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteClient(ctx, clientID); !errors.Is(err, download.ErrClientInUse) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, err := queries.GetDownloadClientByID(ctx, clientID); err != nil {
		t.Fatalf("client removed: %v", err)
	}
	if got, err := queries.GetDownloadTaskByOpID(ctx, opID); err != nil || got.ID != task.ID {
		t.Fatalf("unknown task lost: %+v %v", got, err)
	}
}

func TestTranslateRemotePath(t *testing.T) {
	pathMap := map[string]string{
		"/downloads": "/mnt/nas/downloads",
	}

	translated := download.TranslateRemotePath(pathMap, "/downloads/movies/Fight Club.mkv")
	expected := "/mnt/nas/downloads/movies/Fight Club.mkv"
	if translated != expected {
		t.Fatalf("expected %q, got %q", expected, translated)
	}

	// Unmapped path remains unchanged
	unmapped := download.TranslateRemotePath(pathMap, "/other/path/file.mkv")
	if unmapped != "/other/path/file.mkv" {
		t.Fatalf("expected unmapped path unchanged, got %q", unmapped)
	}
}
