package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func externalTransferIdentity(a, b ports.FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode
}

// Reopening proves that the pinned directory still occupies its configured name.
func externalTransferNamespace(ctx context.Context, files ports.TransferFilesystem, scope ports.UntrackedScope) error {
	for _, item := range []struct {
		path     string
		identity ports.FileIdentity
		marker   string
	}{{scope.LocalRoot, scope.SourceIdentity, ""}, {scope.TargetRootPath, scope.TargetIdentity, scope.TargetMarker}} {
		root, err := files.OpenRoot(ctx, item.path)
		if err != nil {
			return err
		}
		identity, err := root.Identity(ctx)
		if err == nil && !externalTransferIdentity(identity, item.identity) {
			err = ports.ErrUntrackedOrganizationStale
		}
		if err == nil && item.marker != "" {
			_, err = root.Inspect(ctx, item.marker)
		}
		root.Close()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// This function never writes. A persisted permit can only reach this observer.
func observeExternalTransfer(ctx context.Context, files ports.VerifiedTransferFilesystem, roots map[string]ports.TransferRoot, material ports.UntrackedConfirmationMaterial, content ports.TransferContent, stepID uuid.UUID) (ports.VerifiedTransferResult, error) {
	var result ports.VerifiedTransferResult
	scope := material.Scope
	if err := externalTransferNamespace(ctx, files, scope); err != nil {
		return result, err
	}
	source, target := roots[scope.LocalRoot], roots[scope.TargetRootPath]
	if source == nil || target == nil || content.File != material.File.Identity || stepID == uuid.Nil {
		return result, ports.ErrTransferReviewRequired
	}
	for _, item := range []struct {
		root     ports.TransferRoot
		identity ports.FileIdentity
	}{{source, scope.SourceIdentity}, {target, scope.TargetIdentity}} {
		identity, err := item.root.Identity(ctx)
		if err != nil {
			return result, err
		}
		if !externalTransferIdentity(identity, item.identity) {
			return result, ports.ErrUntrackedOrganizationStale
		}
	}
	if material.Input.Action == "hardlink" {
		identity, err := source.Inspect(ctx, material.File.RelativePath)
		if err != nil {
			return result, err
		}
		if identity != content.File {
			return result, ports.ErrTransferReviewRequired
		}
		identity, err = target.Inspect(ctx, material.Input.TargetRelativePath)
		if err != nil {
			return result, err
		}
		if identity != content.File {
			return result, ports.ErrTransferReviewRequired
		}
		proof, err := files.InspectContent(ctx, target, material.Input.TargetRelativePath, identity)
		if err != nil {
			return result, err
		}
		if proof != content {
			return result, ports.ErrTransferReviewRequired
		}
		result.Target = proof
	} else {
		var err error
		result, err = files.ObserveTransfer(ctx, source, material.File.RelativePath, target, material.Input.TargetRelativePath, content, material.Input.Action, stepID)
		if err != nil {
			return result, err
		}
		if result.Target.SHA256 != content.SHA256 || result.Target.File.Size != content.File.Size || result.SourceRemoved != (material.Input.Action == "move") {
			return result, ports.ErrTransferReviewRequired
		}
	}
	if err := externalTransferNamespace(ctx, files, scope); err != nil {
		return result, err
	}
	return result, ctx.Err()
}
