package settings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/command"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

const migrationsDir = "../../../migrations"

func setupSettingsTest(t *testing.T) (*settings.Service, *db.Queries, *ports.Actor, *ports.Actor) {
	_, service, queries, admin, member := setupSettingsPoolTest(t)
	return service, queries, admin, member
}

func setupSettingsPoolTest(t *testing.T) (*pgxpool.Pool, *settings.Service, *db.Queries, *ports.Actor, *ports.Actor) {
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
	svc := settings.NewService(postgres.NewSettingsStore(pool, gen), crypto.NewSettingsCipher(bytes.Repeat([]byte{0x42}, 32)))
	queries := postgres.Queries(pool)

	adminActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   uuid.New().String(),
		Name: "admin",
		Role: ports.RoleAdmin,
	}

	memberActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   uuid.New().String(),
		Name: "member",
		Role: ports.RoleMember,
	}

	for _, actor := range []*ports.Actor{adminActor, memberActor} {
		_, err := queries.CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(actor.ID), Username: actor.Name, PasswordHash: "unusable-fixture-hash", Role: actor.Role, IsActive: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	return pool, svc, queries, adminActor, memberActor
}

func TestSettingsTransactionAndIdempotency(t *testing.T) {
	svc, queries, adminActor, memberActor := setupSettingsTest(t)
	ctx := context.Background()

	// 1. Initial write with Idempotency Key 1
	key := "app.name"
	req1 := settings.UpdateSettingRequest{
		Value:           map[string]any{"title": "PulseMedia Core"},
		ExpectedVersion: 0,
	}

	res1, err := svc.UpdateSetting(ctx, adminActor, key, req1, "idemp_key_001")
	if err != nil {
		t.Fatalf("expected initial update to succeed: %v", err)
	}
	if res1.FromCache {
		t.Fatal("expected first execution to not be from cache")
	}
	if res1.Data.Version != 1 {
		t.Fatalf("expected version 1, got %d", res1.Data.Version)
	}

	// Verify audit log was recorded in same transaction
	logs, err := queries.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if err != nil || len(logs) != 1 {
		t.Fatalf("expected 1 audit log, got %d (err: %v)", len(logs), err)
	}
	if logs[0].Action != "setting.update" || logs[0].TargetID != key {
		t.Fatalf("unexpected audit log: %+v", logs[0])
	}

	// 2. Idempotent replay: same actor, same idempotency key, same request
	resReplay, err := svc.UpdateSetting(ctx, adminActor, key, req1, "idemp_key_001")
	if err != nil {
		t.Fatalf("expected idempotent replay to succeed: %v", err)
	}
	if !resReplay.FromCache {
		t.Fatal("expected idempotent replay to return from cache")
	}
	if resReplay.Data.Version != 1 {
		t.Fatalf("version should not increment on replay: got %d", resReplay.Data.Version)
	}

	// Verify no second audit log was created on replay
	logsAfterReplay, _ := queries.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if len(logsAfterReplay) != 1 {
		t.Fatalf("expected still 1 audit log after replay, got: %d", len(logsAfterReplay))
	}

	// 3. Conflict: same actor, same idempotency key, DIFFERENT request (409 Conflict)
	reqDifferent := settings.UpdateSettingRequest{
		Value:           map[string]any{"title": "Tampered Payload"},
		ExpectedVersion: 0,
	}
	_, err = svc.UpdateSetting(ctx, adminActor, key, reqDifferent, "idemp_key_001")
	if !errors.Is(err, command.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict on payload mismatch, got: %v", err)
	}

	// 4. Failure rollback: version conflict rolls back entirely
	reqConflictVersion := settings.UpdateSettingRequest{
		Value:           map[string]any{"title": "Will Fail"},
		ExpectedVersion: 999, // Mismatched version!
	}
	_, err = svc.UpdateSetting(ctx, adminActor, key, reqConflictVersion, "idemp_key_fail")
	if !errors.Is(err, settings.ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict, got: %v", err)
	}

	// Confirm failed transaction wrote NO audit log and NO idempotency record
	logsAfterFail, _ := queries.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10, Offset: 0})
	if len(logsAfterFail) != 1 {
		t.Fatalf("expected no new audit logs on rollback, got %d", len(logsAfterFail))
	}
	_, err = queries.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{
		ActorType:      string(adminActor.Type),
		ActorID:        adminActor.ID,
		IdempotencyKey: "idemp_key_fail",
	})
	if err == nil {
		t.Fatal("expected no idempotency record persisted on rollback")
	}

	// 5. Permission check runs BEFORE reading idempotency cache
	// Member actor cannot read or execute admin setting update even if idempotency key exists
	_, err = svc.UpdateSetting(ctx, memberActor, key, req1, "idemp_key_001")
	if !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-admin actor, got: %v", err)
	}

	// Anonymous / nil actor returns ErrUnauthorized
	_, err = svc.UpdateSetting(ctx, nil, key, req1, "idemp_key_001")
	if !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for nil actor, got: %v", err)
	}
}

func TestTMDBSettingIsEncryptedAndRedacted(t *testing.T) {
	svc, queries, admin, member := setupSettingsTest(t)
	ctx := context.Background()
	const key = "tmdb.api_key"
	const plaintext = "test-key-must-not-persist"
	req := settings.UpdateSettingRequest{Value: map[string]any{"api_key": plaintext}}
	result, err := svc.UpdateSetting(ctx, admin, key, req, "tmdb-secure-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result.Data)
	if bytes.Contains(body, []byte(plaintext)) || bytes.Contains(body, []byte("ciphertext")) {
		t.Fatalf("setting response exposed secret: %s", body)
	}
	stored, err := queries.GetSetting(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.Value, []byte(plaintext)) {
		t.Fatal("database setting contains plaintext")
	}
	read, err := svc.GetSecret(ctx, key)
	if err != nil || read != plaintext {
		t.Fatalf("decryption failed: %v", err)
	}
	masked, err := svc.GetSetting(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	maskedBody, _ := json.Marshal(masked)
	if bytes.Contains(maskedBody, []byte(plaintext)) || bytes.Contains(maskedBody, []byte("ciphertext")) {
		t.Fatal("read exposed secret")
	}
	logs, err := queries.ListAuditLogs(ctx, db.ListAuditLogsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range logs {
		if bytes.Contains(log.Payload, []byte(plaintext)) {
			t.Fatal("audit exposed secret")
		}
	}
	cached, err := queries.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(admin.Type), ActorID: admin.ID, IdempotencyKey: "tmdb-secure-1"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cached.ResponseBody, []byte(plaintext)) || bytes.Contains(cached.ResponseBody, []byte("ciphertext")) {
		t.Fatal("idempotency result exposed secret")
	}
	replay, err := svc.UpdateSetting(ctx, admin, key, req, "tmdb-secure-1")
	if err != nil || !replay.FromCache {
		t.Fatalf("secure replay failed: %v", err)
	}
	for _, actor := range []*ports.Actor{nil, member} {
		if _, err := svc.ListSettings(ctx, actor); !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("non-administrator read allowed")
		}
	}
	items, err := svc.ListSettings(ctx, admin)
	if err != nil || len(items) == 0 {
		t.Fatal("real administrator setting list failed", err)
	}
	listBody, _ := json.Marshal(items)
	if bytes.Contains(listBody, []byte(plaintext)) || bytes.Contains(listBody, []byte("ciphertext")) {
		t.Fatal("list exposed secret")
	}
	if !bytes.Contains(listBody, []byte(`"configured":true`)) {
		t.Fatal("configured secret state missing")
	}
}
