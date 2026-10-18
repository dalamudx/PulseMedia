package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

var (
	ErrMigrationBusy           = errors.New("another migration is running")
	ErrMigrationConnectionLost = errors.New("migration connection cannot be replaced; inspect database before retrying")
	ErrDatabaseConfiguration   = errors.New("invalid database connection configuration")
)

func runMigration(ctx context.Context, dsn, migrationsDir string, up bool) (int64, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return 0, ErrDatabaseConfiguration
	}
	// Even the provider's version-table preflight runs on the lock-owning session.
	// A dead connection must never be transparently replaced within this operation.
	var connected atomic.Bool
	database := stdlib.OpenDB(*cfg,
		stdlib.OptionBeforeConnect(func(context.Context, *pgx.ConnConfig) error {
			if connected.Swap(true) {
				return ErrMigrationConnectionLost
			}
			return nil
		}),
		stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			accepted := false
			defer func() {
				if !accepted {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = conn.Close(cleanupCtx)
				}
			}()
			var acquired bool
			if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", MigrationAdvisoryLockID).Scan(&acquired); err != nil {
				return err
			}
			if !acquired {
				return ErrMigrationBusy
			}
			accepted = true
			return nil
		}),
	)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer func() { _ = database.Close() }() // Physical disconnection releases ownership after all work returns.
	provider, err := goose.NewProvider(goose.DialectPostgres, database, os.DirFS(migrationsDir), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return 0, fmt.Errorf("prepare migration: %w", err)
	}
	if up {
		_, err = provider.Up(ctx)
	} else {
		_, err = provider.Down(ctx)
	}
	if err != nil {
		return 0, fmt.Errorf("database migration failed: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read migration result: %w", err)
	}
	return version, nil
}
