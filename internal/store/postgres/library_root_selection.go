package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LibraryRootSelectionStore struct{ pool *pgxpool.Pool }

func NewLibraryRootSelectionStore(pool *pgxpool.Pool) *LibraryRootSelectionStore {
	return &LibraryRootSelectionStore{pool}
}
func (s *LibraryRootSelectionStore) ListLibraryRootSelections(ctx context.Context) ([]ports.LibraryRootSelection, error) {
	rows, e := db.New(s.pool).ListConfiguredLibraryRootSelections(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	items := []ports.LibraryRootSelection{}
	for _, r := range rows {
		items = append(items, ports.LibraryRootSelection{ID: r.ID, Name: r.Name, MediaType: r.MediaType, Health: r.Health, Generation: r.Generation, BaselineCompleted: r.BaselineCompleted})
	}
	return items, nil
}
