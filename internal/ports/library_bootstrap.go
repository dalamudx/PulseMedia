package ports

import (
	"context"
	"github.com/google/uuid"
)

// InitialLibraryRoot is a startup default, never proof of a completed scan.
type InitialLibraryRoot struct {
	ID                    uuid.UUID
	Path, MediaType, Name string
}
type LibraryBootstrapRepository interface {
	ExistingMediaTypes(context.Context) ([]string, error)
	RegisterInitialRoots(context.Context, []InitialLibraryRoot) error
}
type LibraryBootstrapFilesystem interface {
	PrepareDefaultRoot(context.Context, string, string) (string, error)
}
