package mediaserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
)

const migrationsDir = "../../../migrations"

type mockMediaServerClient struct {
	failRefresh bool
	indexed     map[string]bool
}

func (m *mockMediaServerClient) RefreshPaths(ctx context.Context, paths []string) error {
	if m.failRefresh {
		return ports.ErrUnavailable // simulate offline server
	}
	for _, p := range paths {
		m.indexed[p] = true
	}
	return nil
}

func (m *mockMediaServerClient) FindByPath(ctx context.Context, path string) (string, bool, error) {
	if m.failRefresh {
		return "", false, ports.ErrUnavailable
	}
	if m.indexed[path] {
		return "item-id-123", true, nil
	}
	return "", false, nil
}

func (m *mockMediaServerClient) Test(ctx context.Context) error {
	if m.failRefresh {
		return ports.ErrUnavailable
	}
	return nil
}

func setupSyncTest(t *testing.T) (*mediaserver.SyncService, *outbox.Outbox, *db.Queries, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
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
	c := clock.New()
	ob := outbox.New(pool, gen, c)
	svc := mediaserver.NewSyncService(ob, postgres.NewMediaConfirmationRepository(pool, gen))
	queries := postgres.Queries(pool)

	// Create root in DB
	rootID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/data/media/movies",
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	// Create media & unit
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        mediaID,
		Title:     "Fight Club",
		MediaType: "movie",
	})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:      unitID,
		MediaID: mediaID,
		UnitKey: "movie",
	})

	// Create active library file
	fileID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileID,
		RootID:          rootID,
		RelPath:         "Fight Club (1999)/Fight Club.mkv",
		SizeBytes:       8000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	if err != nil {
		t.Fatalf("failed to create library file: %v", err)
	}

	if err := queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileID, UnitID: unitID}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: gen.NewUUID(), UnitID: unitID, Stage: "imported", RootID: pgtype.UUID{Bytes: rootID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: fileID, Valid: true}, QualityProfile: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	// Create media server in DB
	serverID, _ := uuid.NewV7()
	_, err = queries.CreateMediaServer(ctx, db.CreateMediaServerParams{
		ID:              serverID,
		Name:            "Jellyfin Main",
		ServerType:      "jellyfin",
		Url:             "http://localhost:8096",
		ApiKeyEncrypted: []byte("token"),
		PathMapping:     []byte(`{"/data/media/movies":"/movies"}`),
	})
	if err != nil {
		t.Fatalf("failed to create media server: %v", err)
	}

	return svc, ob, queries, rootID, unitID, fileID, serverID
}

func TestSyncServerOfflineDoesNotRevokeImportedFact(t *testing.T) {
	svc, ob, queries, rootID, unitID, fileID, serverID := setupSyncTest(t)
	ctx := context.Background()

	mockClient := &mockMediaServerClient{
		failRefresh: true, // simulate Jellyfin offline
		indexed:     make(map[string]bool),
	}
	svc.RegisterClient(serverID, mockClient)

	// 1. Initial imported fact exists
	factID, _ := uuid.NewV7()
	_, err := queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factID,
		UnitID:         unitID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: fileID, Valid: true},
		RootID:         pgtype.UUID{Bytes: rootID, Valid: true},
		QualityProfile: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("failed to create imported fact: %v", err)
	}

	// 2. Publish outbox refresh event

	// 3. Process delivery when server is offline
	// Invariant: Server failure fails delivery but does NOT revoke imported fact!
	deliveryID, _ := uuid.NewV7()
	eventID := uuid.New()
	payload, _ := json.Marshal(map[string]any{"file_id": fileID, "unit_id": unitID})
	if _, err := queries.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: eventID, AggregateType: "media", AggregateID: unitID.String(), EventType: "media.imported", Payload: payload, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	consumer := "mediaserver.refresh:" + serverID.String()
	if _, err := queries.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: deliveryID, EventID: eventID, Consumer: consumer, AggregateType: "media", AggregateID: unitID.String(), Seq: 1, MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	delivery, err := ob.Claim(ctx, consumer, "worker_1", time.Minute)
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	err = svc.ProcessRefreshDelivery(ctx, delivery.ID, serverID, "worker_1", delivery.LeaseToken, "/data/media/movies/Fight Club (1999)/Fight Club.mkv")
	if !errors.Is(err, mediaserver.ErrServerUnreachable) {
		t.Fatalf("expected ErrServerUnreachable, got: %v", err)
	}

	// Imported fact must remain 100% active and untouched!
	facts, err := queries.ListActiveFactsForUnit(ctx, unitID)
	if err != nil || len(facts) == 0 {
		t.Fatalf("imported fact missing after server failure: %v", err)
	}
	hasImported := false
	for _, f := range facts {
		if f.Stage == "imported" {
			hasImported = true
		}
	}
	if !hasImported {
		t.Fatal("imported fact was falsely revoked when server went offline")
	}

	_ = ob
}

func TestConfirmFileOnlyAllowedOnActiveFiles(t *testing.T) {
	svc, _, queries, rootID, unitID, fileID, serverID := setupSyncTest(t)
	ctx := context.Background()

	mockClient := &mockMediaServerClient{
		indexed: map[string]bool{
			"/movies/Fight Club (1999)/Fight Club.mkv": true,
		},
	}
	svc.RegisterClient(serverID, mockClient)

	// 1. Confirm active file: succeeds and records stage='confirmed'
	confirmedFact, err := svc.ConfirmFile(ctx, serverID, fileID, unitID)
	if err != nil {
		t.Fatalf("confirm active file failed: %v", err)
	}
	if confirmedFact.Stage != "confirmed" {
		t.Fatalf("expected confirmed stage: %s", confirmedFact.Stage)
	}

	// 2. Retire the file (e.g. replaced by 4K upgrade)
	_ = queries.RetireFileByPath(ctx, db.RetireFileByPathParams{
		RootID:  rootID,
		RelPath: "Fight Club (1999)/Fight Club.mkv",
	})

	// 3. Confirming retired file MUST be rejected with ErrFileRetired!
	_, err = svc.ConfirmFile(ctx, serverID, fileID, unitID)
	if !errors.Is(err, mediaserver.ErrFileRetired) {
		t.Fatalf("expected ErrFileRetired on retired file confirmation, got: %v", err)
	}
}

func TestTranslatePath(t *testing.T) {
	mapping := map[string]string{
		"/data/media/movies": "/movies",
		"/data/media/tv":     "/tv",
	}

	res := mediaserver.TranslatePath(mapping, "/data/media/movies/Movie (2020)/Movie.mkv")
	expected := "/movies/Movie (2020)/Movie.mkv"
	if res != expected {
		t.Fatalf("expected %q, got %q", expected, res)
	}
}
