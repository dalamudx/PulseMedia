package httpapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the historical organization DDL, independent of the newest migration.
// Current-schema Goose lifecycle is covered separately by TestMigrationsLifecycle.
func applyCompleteTVOrganizationDDL(t *testing.T, ctx context.Context, pool *pgxpool.Pool, up bool) error {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(migrationsPath, "00063_complete_tv_organization.sql"))
	if err != nil {
		return err
	}
	upSQL, downSQL, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		return errors.New("organization migration has no Down section")
	}
	sql := downSQL
	if up {
		sql = upSQL
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback organization DDL fixture: %v", err)
		}
	}()
	if _, err = tx.Exec(ctx, sql); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
