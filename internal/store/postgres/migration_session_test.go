package postgres_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"fmt"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"testing"
)

func writeSessionMigration(t *testing.T, up, down string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_session.sql"), []byte("-- +goose Up\n"+up+"\n-- +goose Down\n"+down+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func owningSessionAssertion() string {
	return fmt.Sprintf(`-- +goose StatementBegin
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND granted AND classid::bigint=%d AND objid::bigint=%d AND objsubid=1) THEN
 RAISE EXCEPTION 'DDL executed without owning migration session lock';
 END IF;
END $$;
-- +goose StatementEnd`, postgres.MigrationAdvisoryLockID>>32, postgres.MigrationAdvisoryLockID&0xffffffff)
}
func TestMigrationDDLUsesLockOwningSession(t *testing.T) {
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	dir := writeSessionMigration(t, owningSessionAssertion()+"\nCREATE TABLE session_probe(id bigint);", owningSessionAssertion()+"\nDROP TABLE session_probe;")
	v, err := postgres.Migrate(context.Background(), dsn, dir)
	if err != nil || v != 1 {
		t.Fatalf("same-session migration failed: version=%d err=%v", v, err)
	}
	v, err = postgres.Rollback(context.Background(), dsn, dir)
	if err != nil || v != 0 {
		t.Fatalf("same-session rollback failed: version=%d err=%v", v, err)
	}
}
func TestRollbackDDLUsesLockOwningSession(t *testing.T) {
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	dir := writeSessionMigration(t, "CREATE TABLE session_probe(id bigint);", owningSessionAssertion()+"\nDROP TABLE session_probe;")
	if _, err := postgres.Migrate(context.Background(), dsn, dir); err != nil {
		t.Fatal(err)
	}
	v, err := postgres.Rollback(context.Background(), dsn, dir)
	if err != nil || v != 0 {
		t.Fatalf("same-session rollback failed: version=%d err=%v", v, err)
	}
}

func TestMigrationConnectionLossAndCancellationDoNotReplay(t *testing.T) {
	for _, failure := range []string{"terminate", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			dsn := pgtest.GetCluster(t).NewTestDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			observer, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = observer.Close(context.Background()) }()
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			params := u.Query()
			params.Set("application_name", "pulse_migration_failure_probe")
			u.RawQuery = params.Encode()
			up := owningSessionAssertion() + "\nCREATE TABLE migration_hold(id bigint);\nSELECT pg_sleep(60);\nINSERT INTO migration_hold VALUES(1);"
			dir := writeSessionMigration(t, up, "DROP TABLE migration_hold;")
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { _, e := postgres.Migrate(runCtx, u.String(), dir); done <- e }()
			completed := false
			defer func() {
				stop()
				if !completed {
					<-done
				}
			}()
			var pid int32
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				err = observer.QueryRow(ctx, "SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND application_name='pulse_migration_failure_probe' AND state='active' AND query LIKE '%pg_sleep(60)%'").Scan(&pid)
				if err == nil {
					break
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					completed = true
					t.Fatalf("migration did not reach held SQL: %v", result)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			var locks int
			if err = observer.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND pid=$1 AND granted", pid).Scan(&locks); err != nil || locks != 1 {
				t.Fatalf("DDL does not own exactly one lock: %d %v", locks, err)
			}
			if _, err = postgres.Migrate(ctx, u.String(), dir); !errors.Is(err, postgres.ErrMigrationBusy) {
				t.Fatalf("competing migrator did not fail with busy: %v", err)
			}
			if failure == "terminate" {
				var killed bool
				if err = observer.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
					t.Fatalf("terminate failed: %v", err)
				}
			} else {
				stop()
			}
			select {
			case result := <-done:
				completed = true
				if result == nil {
					t.Fatal("failed physical operation reported success")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var absent bool
			var applied int
			if err = observer.QueryRow(ctx, "SELECT to_regclass('public.migration_hold') IS NULL").Scan(&absent); err != nil || !absent {
				t.Fatalf("failed DDL did not roll back: %v", err)
			}
			if err = observer.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id>0 AND is_applied").Scan(&applied); err != nil || applied != 0 {
				t.Fatalf("failed migration wrote success metadata: %d %v", applied, err)
			}
			for {
				if err = observer.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='pulse_migration_failure_probe'").Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if applied == 0 {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatalf("failed migration reconnected or retained session: %d", applied)
				}
			}
			if err = os.WriteFile(filepath.Join(dir, "00001_session.sql"), []byte("-- +goose Up\n"+strings.Replace(up, "SELECT pg_sleep(60);\n", "", 1)+"\n-- +goose Down\nDROP TABLE migration_hold;\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if v, e := postgres.Migrate(ctx, u.String(), dir); e != nil || v != 1 {
				t.Fatalf("explicit recovery failed: %d %v", v, e)
			}
			if v, e := postgres.Migrate(ctx, u.String(), dir); e != nil || v != 1 {
				t.Fatalf("repeated migration failed: %d %v", v, e)
			}
			if err = observer.QueryRow(ctx, "SELECT count(*) FROM migration_hold").Scan(&applied); err != nil || applied != 1 {
				t.Fatalf("recovery duplicated data: %d %v", applied, err)
			}
		})
	}
}
func TestMigrationsInIndependentDatabasesHaveLocalDialect(t *testing.T) {
	for i := 0; i < 4; i++ {
		t.Run(fmt.Sprintf("database-%d", i), func(t *testing.T) {
			t.Parallel()
			dsn := pgtest.GetCluster(t).NewTestDB(t)
			dir := writeSessionMigration(t, owningSessionAssertion()+"\nCREATE TABLE migration_parallel(id bigint);", "DROP TABLE migration_parallel;")
			if v, err := postgres.Migrate(context.Background(), dsn, dir); err != nil || v != 1 {
				t.Fatalf("parallel migration: %d %v", v, err)
			}
		})
	}
}

func TestBusinessRoleCannotMigrateOrMarkDDLSuccess(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	adminDSN := cluster.NewTestDB(t)
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	ddl, dml := cluster.CreateRoles(t, strings.TrimPrefix(parsed.Path, "/"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := writeSessionMigration(t, "CREATE TABLE role_probe(id bigint);", "DROP TABLE role_probe;")
	if v, e := postgres.Migrate(ctx, ddl, dir); e != nil || v != 1 {
		t.Fatalf("DDL initial migration: %d %v", v, e)
	}
	second := "-- +goose Up\nCREATE TABLE role_pending(id bigint);\n-- +goose Down\nDROP TABLE role_pending;\n"
	if err = os.WriteFile(filepath.Join(dir, "00002_role.sql"), []byte(second), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.Migrate(ctx, dml, dir); err == nil {
		t.Fatal("business role migrated schema")
	}
	observer, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observer.Close(context.Background()) }()
	var count int
	var absent bool
	if err = observer.QueryRow(ctx, "SELECT to_regclass('role_pending') IS NULL").Scan(&absent); err != nil || !absent {
		t.Fatalf("business-role DDL survived: %v", err)
	}
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=2 AND is_applied").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected DDL marked success: %d %v", count, err)
	}
	if v, e := postgres.Migrate(ctx, ddl, dir); e != nil || v != 2 {
		t.Fatalf("DDL recovery: %d %v", v, e)
	}
	if _, err = postgres.Rollback(ctx, dml, dir); err == nil {
		t.Fatal("business role rolled back schema")
	}
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=2 AND is_applied").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected rollback changed metadata: %d %v", count, err)
	}
	if v, e := postgres.Rollback(ctx, ddl, dir); e != nil || v != 1 {
		t.Fatalf("DDL rollback recovery: %d %v", v, e)
	}
}
