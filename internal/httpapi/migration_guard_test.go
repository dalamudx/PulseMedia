package httpapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Exercise the shipped Down SQL against current business evidence. Rolling back
// unrelated later migrations first can hit their independent retention guards.
// Always roll back this probe, including an unexpectedly accepted downgrade.
func attemptProtectedMigrationDown(ctx context.Context, pool *pgxpool.Pool, migration string) error {
	source, err := os.ReadFile(filepath.Join("..", "..", "migrations", migration))
	if err != nil {
		return err
	}
	_, down, ok := strings.Cut(string(source), "-- +goose Down")
	if !ok {
		return errors.New("migration Down section missing")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, down)
	return err
}
