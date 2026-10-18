// Package migration composes the offline, explicit PostgreSQL schema installer.
package migration

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInput      = errors.New("data-dir, connection-file and migrations are required; recovery archives and identity must be supplied together")
	ErrConnection = errors.New("connection-file must be a bounded regular 0600 file containing an explicit PostgreSQL URI; use verify-full, or disable only for local development")
	ErrLocked     = errors.New("offline data directory lock unavailable; stop application and backup tools first")
	ErrDatabase   = errors.New("PostgreSQL 18 database inspection failed; verify explicit connection and DDL role")
	ErrRecovery   = errors.New("existing schema requires authenticated database/key recovery archives matching its schema and original master key")
	ErrState      = errors.New("database must be empty or have a supported PulseMedia schema; inspect database before retrying")
	ErrMigration  = errors.New("migration failed; inspect database before retrying; committed earlier migrations may remain")
)

type Options struct {
	DataDir, ConnectionFile, Migrations string
	Archive, KeyArchive, IdentityFile   string
}

type Result struct{ PreviousVersion, Version int64 }

// Run holds the application's data-directory lock through preflight, recovery
// validation, migration and physical connection closure. The supported offline
// deployment uses one data directory for an application/database pair.
// It never creates a key, changes restore isolation, or retries uncertain DDL.
func Run(ctx context.Context, o Options) (result Result, err error) {
	if o.DataDir == "" || o.ConnectionFile == "" || o.Migrations == "" {
		return result, ErrInput
	}
	recovery := o.Archive != "" || o.KeyArchive != "" || o.IdentityFile != ""
	if recovery && (o.Archive == "" || o.KeyArchive == "" || o.IdentityFile == "") {
		return result, ErrInput
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	dsn, err := connection(o.ConnectionFile)
	if err != nil {
		return result, ErrConnection
	}
	info, err := os.Stat(o.Migrations)
	if err != nil || !info.IsDir() {
		return result, ErrInput
	}
	l, err := lockfile.Acquire(o.DataDir)
	if err != nil {
		return result, ErrLocked
	}
	defer func() {
		if e := l.Release(); e != nil {
			err = ErrLocked
		}
	}()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return result, ErrDatabase
	}
	// Independent cleanup context also applies after SIGTERM or a canceled query.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Close(closeCtx)
	}()
	var server int
	if err = c.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&server); err != nil || server/10000 != 18 {
		return result, ErrDatabase
	}
	var hasVersion bool
	if err = c.QueryRow(ctx, "SELECT to_regclass('public.goose_db_version') IS NOT NULL").Scan(&hasVersion); err != nil {
		return result, ErrDatabase
	}
	if hasVersion {
		if err = c.QueryRow(ctx, "SELECT version_id FROM public.goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1").Scan(&result.PreviousVersion); err != nil {
			return result, ErrState
		}
		if result.PreviousVersion < 1 || result.PreviousVersion > postgres.CurrentMigrationVersion {
			return result, ErrState
		}
		if err = validateRecovery(ctx, o, result.PreviousVersion); err != nil {
			return result, ErrRecovery
		}
	} else {
		// Refuse unversioned user objects: only an empty public schema and the
		// standard plpgsql extension qualify for installation without a recovery point.
		var occupied bool
		err = c.QueryRow(ctx, `SELECT
   EXISTS(SELECT 1 FROM pg_namespace WHERE nspname <> 'public' AND nspname <> 'information_schema' AND nspname !~ '^pg_') OR
   EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname <> 'information_schema' AND n.nspname !~ '^pg_') OR
   EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname <> 'information_schema' AND n.nspname !~ '^pg_') OR
   EXISTS(SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname <> 'information_schema' AND n.nspname !~ '^pg_') OR
   EXISTS(SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql')`).Scan(&occupied)
		if err != nil {
			return result, ErrDatabase
		}
		if occupied {
			return result, ErrState
		}
		if recovery {
			return result, ErrState
		}
	}
	// No ordinary connection is retained while Migrate owns its exclusive session.
	if err = c.Close(ctx); err != nil {
		return result, ErrDatabase
	}
	result.Version, err = postgres.Migrate(ctx, dsn, o.Migrations)
	if err != nil {
		return result, ErrMigration
	}
	if result.Version != postgres.CurrentMigrationVersion {
		return result, ErrState
	}
	return result, nil
}
