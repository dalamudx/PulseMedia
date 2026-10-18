package library

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"os"
)

// MissingConfirmer applies only the negative evidence from two sealed scans.
// A root physical slot encloses all filesystem checks and database writebacks.
type MissingConfirmer struct {
	files ports.ScanFilesystem
	repo  ports.ScanMissingRepository
}

func NewMissingConfirmer(files ports.ScanFilesystem, repo ports.ScanMissingRepository) *MissingConfirmer {
	return &MissingConfirmer{files: files, repo: repo}
}
func (c *MissingConfirmer) Confirm(ctx context.Context, rootID, runID uuid.UUID) (int, error) {
	seal, err := c.repo.LoadMissingScan(ctx, rootID, runID)
	if err != nil {
		return 0, err
	}
	roots, release, err := c.files.LockRoots(ctx, seal.Path)
	if err != nil {
		return 0, err
	}
	defer release()
	root := roots[seal.Path]
	verify := func() error {
		current, err := c.files.OpenRoot(ctx, seal.Path)
		if err != nil {
			return err
		}
		defer current.Close()
		a, err := root.Identity(ctx)
		if err != nil {
			return err
		}
		b, err := current.Identity(ctx)
		if err != nil {
			return err
		}
		if a.Device != seal.RootIdentity.Device || a.Inode != seal.RootIdentity.Inode || a.Device != b.Device || a.Inode != b.Inode {
			return ErrScanInterrupted
		}
		marker := seal.MarkerFilename
		if marker == "" {
			marker = ".pulsemedia_root"
		}
		_, err = current.Inspect(ctx, marker)
		return err
	}
	if err = verify(); err != nil {
		return 0, err
	}
	count := 0
	after := uuid.Nil
	for {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		rows, err := c.repo.ListMissingChecks(ctx, seal, after)
		if err != nil {
			return count, err
		}
		if len(rows) == 0 {
			return count, nil
		}
		for _, file := range rows {
			if err = verify(); err != nil {
				return count, err
			}
			_, statErr := root.Inspect(ctx, file.RelPath)
			decision := ports.ScanFilePresent
			if statErr != nil {
				if !errors.Is(statErr, os.ErrNotExist) {
					return count, statErr
				}
				decision = ports.ScanFileMissing
			}
			if err = verify(); err != nil {
				return count, err
			}
			if err = c.repo.ResolveMissing(ctx, seal, file, decision); err != nil {
				return count, err
			}
			if decision == ports.ScanFileMissing {
				count++
			}
			after = file.FileID
		}
	}
}
