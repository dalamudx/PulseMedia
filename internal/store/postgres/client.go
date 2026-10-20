package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

const (
	// MigrationAdvisoryLockID is a stable 64-bit int for PulseMedia migration advisory locks.
	MigrationAdvisoryLockID = 0x50756c73654d // "PulseM"
	ExpectedM0Version       = 1
	ExpectedM1Version       = 2
	CurrentMigrationVersion = 98
)

var (
	ErrSchemaMismatch = errors.New("database schema version mismatch")
)

type PoolConfig struct {
	MinConns int32
	MaxConns int32
}

func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MinConns: 2,
		MaxConns: 8,
	}
}

// NewPool connects to PostgreSQL with bounded connection pool and statement/lock timeouts.
func NewPool(ctx context.Context, dsn string, cfg PoolConfig) (*pgxpool.Pool, error) {
	pgxCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database DSN: %w", err)
	}

	pgxCfg.MinConns = cfg.MinConns
	pgxCfg.MaxConns = cfg.MaxConns
	pgxCfg.MaxConnIdleTime = 5 * time.Minute
	pgxCfg.MaxConnLifetime = 30 * time.Minute

	// Configure connection settings per architecture baseline:
	// statement_timeout=30s, lock_timeout=2s, idle_in_transaction_session_timeout=30s
	pgxCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET statement_timeout = '30s'; SET lock_timeout = '2s'; SET idle_in_transaction_session_timeout = '30s';")
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

// Queries creates a typed sqlc Queries instance bound to the pool.
func Queries(pool *pgxpool.Pool) *db.Queries {
	return db.New(pool)
}

// Migrate applies SQL migrations under one physical PostgreSQL session lock.
func Migrate(ctx context.Context, dsn string, migrationsDir string) (int64, error) {
	return runMigration(ctx, dsn, migrationsDir, true)
}

// Rollback rolls back one SQL migration under the same session ownership contract.
func Rollback(ctx context.Context, dsn string, migrationsDir string) (int64, error) {
	return runMigration(ctx, dsn, migrationsDir, false)
}

// CheckSchemaVersion verifies that the database has applied up to the expected migration version.
func CheckSchemaVersion(ctx context.Context, pool *pgxpool.Pool, expectedVersion int64) error {
	var currentVersion int64
	err := pool.QueryRow(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied = true ORDER BY id DESC LIMIT 1").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("%w: unable to read goose_db_version: %v", ErrSchemaMismatch, err)
	}
	if currentVersion != expectedVersion {
		return fmt.Errorf("%w: expected %d, got %d", ErrSchemaMismatch, expectedVersion, currentVersion)
	}
	return nil
}
