package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
)

type lostResponseClient struct {
	adds int
	info *ports.TorrentInfo
}

func (m *lostResponseClient) AddTorrent(_ context.Context, p ports.AddTorrentParam) error {
	m.adds++
	m.info = &ports.TorrentInfo{InfoHash: p.InfoHash, SavePath: p.SavePath, State: "downloading"}
	return ports.ErrTimeout
}
func (m *lostResponseClient) GetTorrent(_ context.Context, hash string) (*ports.TorrentInfo, error) {
	if m.info != nil && m.info.InfoHash == hash {
		return m.info, nil
	}
	return nil, ports.ErrNotFound
}
func (m *lostResponseClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	return nil, nil
}
func (m *lostResponseClient) RemoveTorrent(context.Context, string, bool) error { return nil }
func (m *lostResponseClient) Test(context.Context) error                        { return nil }

func TestDownloadJobLostResponseNeverReadds(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := postgres.Queries(pool)
	gen := idgen.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	raw, hash := ordinaryTestTorrent("Test.2020.mkv", 32)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer origin.Close()
	credential, err := crypto.Encrypt(key, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	idx, candidate, clientID, root, _ := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "test", Url: origin.URL, ApiKeyEncrypted: credential, Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	link, err := crypto.Encrypt(key, []byte(origin.URL+"/test.torrent"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1, ID: candidate, IndexerID: idx, Guid: "test", Title: "Test.2020.1080p.Bluray", LinkEncrypted: link, InfoHash: pgtype.Text{String: hash, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)})
	if err != nil {
		t.Fatal(err)
	}
	pass, err := crypto.Encrypt(key, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: clientID, Name: "test", ClientType: "qbittorrent", Url: "http://127.0.0.1:8080", Username: "test", PasswordEncrypted: pass, RemotePathMap: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: "/data/movies", MediaType: "movie", Name: "movies"})
	if err != nil {
		t.Fatal(err)
	}
	media, unit := uuid.New(), uuid.New()
	if _, err = q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Test", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewQueue(pool, gen, clock.New())
	svc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, clock.New(), acquisition.SelectMovieRound))
	res, err := svc.AcquireMovie(ctx, pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}), acquisition.AcquireMovieCommand{RootID: root, UnitIDs: []uuid.UUID{unit}, CandidateID: candidate, DownloaderID: &clientID})
	if err != nil {
		t.Fatal(err)
	}
	op := res.OperationID
	mock := &lostResponseClient{}
	repo := downloadstore.NewRepository(pool, gen, key)
	repo.SetTorrentSource(ordinaryTestSource, torrent.Inspector{})
	mgr := download.NewManager(repo)
	mgr.SetSubmissionRepository(postgres.NewDownloadSubmissionStore(pool, gen))
	mgr.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return mock, nil })
	job, err := queue.Claim(ctx, []string{"download_submission"}, "lost-response-owner", 5*time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if _, err := handleDownloadJob(ctx, q, mgr, job, clientID); err != nil {
		t.Fatal(err)
	}
	task, err := q.GetDownloadTaskByOpID(ctx, op)
	if err != nil || task.State != "unknown" {
		t.Fatalf("task must remain unknown: %+v %v", task, err)
	}
	// Restart: create a new manager to demonstrate that recovery does not rely
	// on in-memory client registration and does not repeat the external Add.
	restarted := download.NewManager(downloadstore.NewRepository(pool, gen, key))
	restarted.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return mock, nil })
	if _, err := handleDownloadJob(ctx, q, restarted, job, clientID); err != nil {
		t.Fatal(err)
	}
	if mock.adds != 1 {
		t.Fatalf("external Add called %d times", mock.adds)
	}
	task, err = q.GetDownloadTaskByOpID(ctx, op)
	if err != nil || task.State != "tracking" {
		t.Fatalf("expected reconciliation after response loss: %+v %v", task, err)
	}
}
