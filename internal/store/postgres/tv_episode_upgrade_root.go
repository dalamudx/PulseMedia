package postgres

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (x *tvUpgradeSession) holdRoot(ctx context.Context, m *tvEpisodeUpgradeMaterial) (func(), error) {
	if x.store.files == nil {
		return nil, ports.ErrUnavailable
	}
	roots, release, e := x.store.files.LockRoots(ctx, m.Root.Path)
	if e != nil {
		return nil, e
	}
	root := roots[m.Root.Path]
	if root == nil {
		release()
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if e = x.checkRootCapability(ctx, m, root); e != nil {
		release()
		return nil, e
	}
	if e = x.checkRoot(ctx, m); e != nil {
		release()
		return nil, e
	}
	return release, nil
}

// Reopen the configured path around I/O: a retained FD alone cannot prove the
// downloader's configured directory still refers to that same physical root.
func (x *tvUpgradeSession) checkRoot(ctx context.Context, m *tvEpisodeUpgradeMaterial) error {
	root, e := x.store.files.OpenRoot(ctx, m.Root.Path)
	if e != nil {
		return e
	}
	defer root.Close()
	return x.checkRootCapability(ctx, m, root)
}
func (x *tvUpgradeSession) checkRootCapability(ctx context.Context, m *tvEpisodeUpgradeMaterial, root ports.TransferRoot) error {
	physical, e := root.Identity(ctx)
	if e != nil {
		return e
	}
	var device, inode int64
	e = x.conn.QueryRow(ctx, `SELECT sf.root_device,sf.root_inode FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id WHERE sf.root_id=$1 AND sr.generation=$2 AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1`, m.Root.ID, m.Root.Generation).Scan(&device, &inode)
	if e != nil {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if physical.Device != device || physical.Inode != inode {
		return ports.ErrFileIdentity
	}
	marker := m.Root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if _, e = root.Inspect(ctx, marker); e != nil {
		return e
	}
	old, e := root.Inspect(ctx, m.OldRel)
	if e != nil {
		return e
	}
	var fingerprint string
	if e = x.conn.QueryRow(ctx, "SELECT COALESCE(file_fingerprint,'') FROM library_file WHERE id=$1", x.authority.Request.CurrentFileID).Scan(&fingerprint); e != nil {
		return e
	}
	if old.Device != physical.Device || old.Inode <= 0 || old.Size != m.OldSize || old.Mtime != m.OldMtime || fingerprint != fmt.Sprintf("stat.v1:%d:%d:%d", old.Inode, old.Size, old.Mtime) {
		return ports.ErrFileIdentity
	}
	return nil
}