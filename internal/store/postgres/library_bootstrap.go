package postgres

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LibraryBootstrapStore struct{ pool *pgxpool.Pool }

var _ ports.LibraryBootstrapRepository = (*LibraryBootstrapStore)(nil)

func NewLibraryBootstrapStore(pool *pgxpool.Pool) *LibraryBootstrapStore {
	return &LibraryBootstrapStore{pool: pool}
}
func (s *LibraryBootstrapStore) ExistingMediaTypes(ctx context.Context) ([]string, error) {
	return Queries(s.pool).ListConfiguredRootMediaTypes(ctx)
}
func (s *LibraryBootstrapStore) RegisterInitialRoots(ctx context.Context, roots []ports.InitialLibraryRoot) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err = q.LockLibraryRootBootstrap(ctx); err != nil {
		return err
	}
	for _, root := range roots {
		if root.ID == [16]byte{} || root.Path == "" || (root.MediaType != "movie" && root.MediaType != "tv") {
			return fmt.Errorf("invalid initial library root")
		}
		exists, err := q.HasConfiguredRootForType(ctx, root.MediaType)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root.ID, Path: root.Path, MediaType: root.MediaType, Name: root.Name}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
