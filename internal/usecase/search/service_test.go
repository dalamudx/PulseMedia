package search_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

const migrationsDir = "../../../migrations"

type mockIndexerClient struct {
	releases []*ports.ReleaseItem
}

func (m *mockIndexerClient) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	return m.releases, nil
}

func (m *mockIndexerClient) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	return []byte("torrent-content"), nil
}

func setupSearchTest(t *testing.T) (*search.Service, *pgxpool.Pool, *db.Queries, []byte, uuid.UUID) {
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

	svc := search.NewService(postgres.NewSearchExecutionStore(pool), gen, crypto.NewSecretProtector(masterKey), 0)
	queries := postgres.Queries(pool)

	// Create test indexer in DB
	indexerID, _ := uuid.NewV7()
	encKey, _ := crypto.Encrypt(masterKey, []byte("passkey"))
	_, err = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Mock Tracker",
		Url:             "https://tracker.example.com",
		ApiKeyEncrypted: encKey,
		Categories:      []byte("[]"),
	})
	if err != nil {
		t.Fatalf("failed to create indexer: %v", err)
	}

	return svc, pool, queries, masterKey, indexerID
}

func TestSearchExecutionAndDeduplication(t *testing.T) {
	svc, pool, _, masterKey, indexerID := setupSearchTest(t)
	ctx := context.Background()

	mockClient := &mockIndexerClient{
		releases: []*ports.ReleaseItem{
			{
				Title:       "Fight.Club.1999.1080p.BluRay.x264-SPARKS",
				GUID:        "guid-1234",
				DownloadURL: "https://tracker.example.com/dl/1234.torrent?passkey=topsecret",
				InfoHash:    "9a3f12c45b6d7e8f0123456789abcdef01234567",
				SizeBytes:   8589934592,
				Seeders:     50,
			},
		},
	}
	svc.RegisterClient(indexerID, mockClient)

	// 1. Initial search: must succeed, create release candidate with encrypted link
	res1, err := svc.ExecuteSearch(ctx, indexerID, "Fight Club", 0)
	if err != nil {
		t.Fatalf("search 1 failed: %v", err)
	}
	if res1.FromShared || res1.ResultsCount != 1 || res1.State != "succeeded" {
		t.Fatalf("unexpected search result: %+v", res1)
	}

	// Verify candidate saved and download link encrypted
	var encLink []byte
	err = pool.QueryRow(ctx, "SELECT link_encrypted FROM release_candidate WHERE guid = $1", "guid-1234").Scan(&encLink)
	if err != nil {
		t.Fatalf("failed to query saved candidate: %v", err)
	}

	decryptedURL, err := crypto.Decrypt(masterKey, encLink)
	if err != nil {
		t.Fatalf("failed to decrypt saved link: %v", err)
	}
	if string(decryptedURL) != "https://tracker.example.com/dl/1234.torrent?passkey=topsecret" {
		t.Fatalf("decrypted link mismatch: %s", string(decryptedURL))
	}

	// 2. Query again for the same keyword: must succeed as a new query run (同名搜索可再次执行)
	res2, err := svc.ExecuteSearch(ctx, indexerID, "Fight Club", 0)
	if err != nil {
		t.Fatalf("repeated search failed: %v", err)
	}
	if res2.FromShared {
		t.Fatal("subsequent search after completion should not be shared from old finished query")
	}
	if res2.QueryID == res1.QueryID {
		t.Fatalf("subsequent search must have distinct query ID: %s vs %s", res2.QueryID, res1.QueryID)
	}
}
