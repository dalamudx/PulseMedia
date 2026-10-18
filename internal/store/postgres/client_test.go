package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

const migrationsDir = "../../../migrations"

func TestMigrationsLifecycle(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)
	ctx := context.Background()

	// 1. Initial migration on empty database
	v1, err := postgres.Migrate(ctx, dbDSN, migrationsDir)
	if err != nil {
		t.Fatalf("expected successful migration on empty db: %v", err)
	}
	if v1 != postgres.CurrentMigrationVersion {
		t.Fatalf("expected version %d, got %d", postgres.CurrentMigrationVersion, v1)
	}

	// 2. Connect pool and check schema version
	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	if err := postgres.CheckSchemaVersion(ctx, pool, postgres.CurrentMigrationVersion); err != nil {
		t.Fatalf("expected valid schema version: %v", err)
	}

	// Check mismatch rejection
	if err := postgres.CheckSchemaVersion(ctx, pool, 999); !errors.Is(err, postgres.ErrSchemaMismatch) {
		t.Fatalf("expected ErrSchemaMismatch for wrong version, got: %v", err)
	}

	// 3. Rollback
	vDown, err := postgres.Rollback(ctx, dbDSN, migrationsDir)
	if err != nil {
		t.Fatalf("expected successful rollback: %v", err)
	}
	if vDown != postgres.CurrentMigrationVersion-1 {
		t.Fatalf("expected rolled back to version %d, got %d", postgres.CurrentMigrationVersion-1, vDown)
	}

	// 4. Migrate back up
	vUp, err := postgres.Migrate(ctx, dbDSN, migrationsDir)
	if err != nil {
		t.Fatalf("expected successful re-migration: %v", err)
	}
	if vUp != postgres.CurrentMigrationVersion {
		t.Fatalf("expected version %d after re-migration, got %d", postgres.CurrentMigrationVersion, vUp)
	}
}

func TestDDLvsDMLRolePermissions(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	adminDSN := cluster.NewTestDB(t)

	// Extract dbName from DSN
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("failed to parse DSN: %v", err)
	}
	dbName := strings.TrimPrefix(parsed.Path, "/")

	ddlDSN, dmlDSN := cluster.CreateRoles(t, dbName)
	ctx := context.Background()

	// 1. DDL role runs migrations
	v, err := postgres.Migrate(ctx, ddlDSN, migrationsDir)
	if err != nil {
		t.Fatalf("DDL role failed to run migrations: %v", err)
	}
	if v != postgres.CurrentMigrationVersion {
		t.Fatalf("expected version %d, got %d", postgres.CurrentMigrationVersion, v)
	}

	// 2. DML role connects via pool and performs DML
	dmlPool, err := postgres.NewPool(ctx, dmlDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("DML role failed to connect pool: %v", err)
	}
	defer dmlPool.Close()

	queries := postgres.Queries(dmlPool)
	userID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("failed to create uuid: %v", err)
	}

	// DML role can insert user
	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		Username:     "testuser",
		PasswordHash: "hashed_password",
		Role:         "member",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("DML role failed to insert user: %v", err)
	}
	if user.Username != "testuser" {
		t.Fatalf("unexpected username: %s", user.Username)
	}

	// 3. DML role CANNOT execute DDL (e.g. CREATE TABLE or DROP TABLE)
	_, err = dmlPool.Exec(ctx, "CREATE TABLE forbidden_table (id INT PRIMARY KEY);")
	if err == nil {
		t.Fatal("expected DML role to be denied permission to CREATE TABLE")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" { // 42501 = insufficient_privilege
		t.Fatalf("expected SQLSTATE 42501 (insufficient_privilege), got %v", err)
	}
}

func TestQueriesFunctionality(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)
	ctx := context.Background()

	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	queries := postgres.Queries(pool)

	// 1. User creation and case-insensitive uniqueness
	userID, _ := uuid.NewV7()
	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		Username:     "Administrator",
		PasswordHash: "secret_hash",
		Role:         "admin",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Query by lower casing
	byLower, err := queries.GetUserByUsername(ctx, "administrator")
	if err != nil {
		t.Fatalf("failed to get user by lowercase: %v", err)
	}
	if byLower.ID != user.ID {
		t.Fatalf("user ID mismatch: %v vs %v", byLower.ID, user.ID)
	}

	// Unique lower(username) violation
	otherID, _ := uuid.NewV7()
	_, err = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           otherID,
		Username:     "ADMINISTRATOR",
		PasswordHash: "other_hash",
		Role:         "admin",
		IsActive:     true,
	})
	if err == nil {
		t.Fatal("expected duplicate case-insensitive username to fail")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" { // 23505 = unique_violation
		t.Fatalf("expected unique violation 23505, got: %v", err)
	}

	// 2. Session creation and lookup
	tokenHash := sha256.Sum256([]byte("session_secret_token"))
	csrfHash := sha256.Sum256([]byte("csrf_secret_token"))
	sessionID, _ := uuid.NewV7()

	sess, err := queries.CreateSession(ctx, db.CreateSessionParams{
		ID:            sessionID,
		UserID:        user.ID,
		TokenHash:     tokenHash[:],
		CsrfTokenHash: csrfHash[:],
		ExpiresAt:     pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	loadedSess, err := queries.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loadedSess.ID != sess.ID || loadedSess.Username != "Administrator" {
		t.Fatalf("unexpected loaded session: %+v", loadedSess)
	}

	// 3. Settings versioning
	s1, err := queries.SetSetting(ctx, db.SetSettingParams{
		Key:   "site.title",
		Value: []byte(`"PulseMedia"`),
	})
	if err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}
	if s1.Version != 1 {
		t.Fatalf("expected version 1 for initial setting, got %d", s1.Version)
	}

	s2, err := queries.SetSetting(ctx, db.SetSettingParams{
		Key:   "site.title",
		Value: []byte(`"PulseMedia Updated"`),
	})
	if err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}
	if s2.Version != 2 {
		t.Fatalf("expected version 2 for updated setting, got %d", s2.Version)
	}

	// 4. Idempotency record uniqueness
	recID, _ := uuid.NewV7()
	_, err = queries.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{
		ID:             recID,
		ActorType:      "user",
		ActorID:        user.ID.String(),
		IdempotencyKey: "req_key_1",
		RequestHash:    "sha256:abcd",
		ResponseCode:   200,
		ResponseBody:   []byte(`{"ok":true}`),
		ResourceID:     pgtype.Text{String: user.ID.String(), Valid: true},
	})
	if err != nil {
		t.Fatalf("failed to create idempotency record: %v", err)
	}

	// Duplicate idempotency key for same actor must fail with 23505
	recID2, _ := uuid.NewV7()
	_, err = queries.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{
		ID:             recID2,
		ActorType:      "user",
		ActorID:        user.ID.String(),
		IdempotencyKey: "req_key_1",
		RequestHash:    "sha256:efgh",
		ResponseCode:   200,
		ResponseBody:   []byte(`{"ok":true}`),
		ResourceID:     pgtype.Text{String: user.ID.String(), Valid: true},
	})
	if err == nil {
		t.Fatal("expected duplicate idempotency key to fail")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected unique violation 23505, got: %v", err)
	}
}
