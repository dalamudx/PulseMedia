package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestFullSchemaRolesBackupAndNewDBRestore(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	adminDSN := cluster.NewTestDB(t)

	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("failed to parse DSN: %v", err)
	}
	dbName := strings.TrimPrefix(parsed.Path, "/")

	ddlDSN, dmlDSN := cluster.CreateRoles(t, dbName)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. DDL role runs all migrations up to current version
	v, err := postgres.Migrate(ctx, ddlDSN, migrationsDir)
	if err != nil {
		t.Fatalf("DDL role failed to run migrations: %v", err)
	}
	if v != postgres.CurrentMigrationVersion {
		t.Fatalf("expected version %d, got %d", postgres.CurrentMigrationVersion, v)
	}

	// 2. DML role connects via pool and performs business DML
	dmlPool, err := postgres.NewPool(ctx, dmlDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("DML role failed to connect pool: %v", err)
	}
	defer dmlPool.Close()

	queries := postgres.Queries(dmlPool)
	userID, _ := uuid.NewV7()
	_, err = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		Username:     "backup_test_user",
		PasswordHash: "argon2_hash",
		Role:         "admin",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("DML role failed to insert user: %v", err)
	}

	// 3. DML role CANNOT perform DDL (42501 permission denied)
	_, err = dmlPool.Exec(ctx, "CREATE TABLE forbidden_test (id INT PRIMARY KEY);")
	if err == nil {
		t.Fatal("vulnerability: DML role was able to CREATE TABLE")
	}
	var pgErr *pgconn.PgError
	if !errorsAs(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("expected SQLSTATE 42501 for DDL by DML role, got: %v", err)
	}

	// 4. Create encrypted record to test decryption after restore
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 42)
	}
	originalSecret := "SUPER_CONFIDENTIAL_PASSKEY_RESTORE_TEST"
	encryptedSecret, err := crypto.Encrypt(masterKey, []byte(originalSecret))
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	indexerID, _ := uuid.NewV7()
	_, err = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Backup Indexer",
		Url:             "https://backup.indexer",
		ApiKeyEncrypted: encryptedSecret,
		Categories:      []byte("[]"),
	})
	if err != nil {
		t.Fatalf("failed to insert indexer: %v", err)
	}

	// 5. Execute pg_dump custom format (-Fc)
	// Resolve the pinned installation from the sole toolchain authority.
	prefix, err := exec.CommandContext(ctx, "mise", "where", "postgres").Output()
	if err != nil {
		t.Fatal("could not resolve pinned PostgreSQL client tools")
	}
	pgPrefix := strings.TrimSpace(string(prefix))
	if !filepath.IsAbs(pgPrefix) {
		t.Fatal("PostgreSQL installation must be an absolute path")
	}
	pgBin := filepath.Join(pgPrefix, "bin")
	clientEnv := append(os.Environ(), "LD_LIBRARY_PATH="+filepath.Join(pgPrefix, "lib"), "PGPASSWORD="+cluster.Password)
	dumpFile := filepath.Join(t.TempDir(), "backup.dump")

	dumpCmd := exec.CommandContext(ctx, filepath.Join(pgBin, "pg_dump"),
		"--no-password",
		"-h", cluster.Host,
		"-p", fmt.Sprintf("%d", cluster.Port),
		"-U", cluster.SuperUser,
		"-Fc",
		"-f", dumpFile,
		dbName)
	dumpCmd.Env = clientEnv

	if out, err := dumpCmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_dump failed: %s (%v)", string(out), err)
	}

	// Verify dump file exists and has size
	dumpInfo, err := os.Stat(dumpFile)
	if err != nil || dumpInfo.Size() == 0 {
		t.Fatalf("dump file invalid or empty: %v", err)
	}

	// 6. Restore to a completely separate brand new clean database (pulsemedia_restore_xxx)
	restoreDBName := "pulsemedia_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminDB, err := sql.Open("pgx", cluster.BaseDSN)
	if err != nil {
		t.Fatalf("failed to connect admin db: %v", err)
	}
	defer func() { _ = adminDB.Close() }()

	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{restoreDBName}.Sanitize()+" OWNER "+pgx.Identifier{cluster.SuperUser}.Sanitize()); err != nil {
		t.Fatalf("failed to create clean restore db: %v", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// A single standalone DROP avoids the forbidden implicit transaction.
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP DATABASE "+pgx.Identifier{restoreDBName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("could not clean up owned restore database %s: %v", restoreDBName, err)
		}
	}()

	restoreCmd := exec.CommandContext(ctx, filepath.Join(pgBin, "pg_restore"),
		"--no-password",
		"-h", cluster.Host,
		"-p", fmt.Sprintf("%d", cluster.Port),
		"-U", cluster.SuperUser,
		"--exit-on-error",
		"--no-owner",
		"--no-privileges",
		"-d", restoreDBName,
		dumpFile)
	restoreCmd.Env = clientEnv

	if out, err := restoreCmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_restore failed: %s (%v)", string(out), err)
	}

	// 7. Connect to restored database and verify data integrity
	restoreURL := *parsed
	restoreURL.Path = "/" + restoreDBName
	restorePool, err := postgres.NewPool(ctx, restoreURL.String(), postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect restore pool: %v", err)
	}
	defer restorePool.Close()

	// Verify schema version in restored DB
	if err := postgres.CheckSchemaVersion(ctx, restorePool, postgres.CurrentMigrationVersion); err != nil {
		t.Fatalf("restored db schema version mismatch: %v", err)
	}

	// Verify restored user
	restoreQueries := postgres.Queries(restorePool)
	restoredUser, err := restoreQueries.GetUserByID(ctx, userID)
	if err != nil || restoredUser.Username != "backup_test_user" {
		t.Fatalf("restored user verification failed: %v", err)
	}

	// 8. Verify Master Key Decryption on restored database
	restoredIndexer, err := restoreQueries.GetIndexerByID(ctx, indexerID)
	if err != nil {
		t.Fatalf("failed to load restored indexer: %v", err)
	}

	decrypted, err := crypto.Decrypt(masterKey, restoredIndexer.ApiKeyEncrypted)
	if err != nil {
		t.Fatalf("master key decryption on restored database failed: %v", err)
	}
	if string(decrypted) != originalSecret {
		t.Fatal("restored encrypted credential did not round-trip")
	}
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
