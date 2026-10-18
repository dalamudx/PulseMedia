package library

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// EnsureDefaultRoots prepares files outside the single atomic registration.
// An existing root of any health is retained rather than silently replaced.
func EnsureDefaultRoots(ctx context.Context, repo ports.LibraryBootstrapRepository, files ports.LibraryBootstrapFilesystem, gen ports.IDGen, dataDir string) error {
	types, err := repo.ExistingMediaTypes(ctx)
	if err != nil {
		return fmt.Errorf("read configured library roots: %w", err)
	}
	exists := map[string]bool{}
	for _, kind := range types {
		exists[kind] = true
	}
	var initial []ports.InitialLibraryRoot
	for _, kind := range []string{"movie", "tv"} {
		if exists[kind] {
			continue
		}
		path, err := files.PrepareDefaultRoot(ctx, dataDir, kind)
		if err != nil {
			return fmt.Errorf("prepare default %s library: %w", kind, err)
		}
		name := "默认电影存储库"
		if kind == "tv" {
			name = "默认电视剧存储库"
		}
		initial = append(initial, ports.InitialLibraryRoot{ID: gen.NewUUID(), Path: path, MediaType: kind, Name: name})
	}
	if len(initial) == 0 {
		return ctx.Err()
	}
	return repo.RegisterInitialRoots(ctx, initial)
}
