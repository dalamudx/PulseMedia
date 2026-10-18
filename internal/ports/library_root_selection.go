package ports

import (
	"context"
	"github.com/google/uuid"
)

type LibraryRootSelection struct {
	ID                      uuid.UUID
	Name, MediaType, Health string
	Generation              int64
	BaselineCompleted       bool
}
type LibraryRootSelectionRepository interface {
	ListLibraryRootSelections(context.Context) ([]LibraryRootSelection, error)
}
