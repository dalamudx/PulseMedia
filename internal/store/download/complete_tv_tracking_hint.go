package download

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// This dependency is explicitly owned by application assembly, never inferred
// from a downloader's returned save path.
func (r *Repository) SetCompleteTVTrackingHintFilesystem(files ports.RootFilesystem) {
	r.execution.mu.Lock()
	defer r.execution.mu.Unlock()
	r.execution.trackingHintFiles = files
}

func (m *Manager) completeTVHintProbe(ctx context.Context, repository ports.CompleteTVTrackingRepository, request ports.CompleteTVTrackingRequest) (func(context.Context) error, error) {
	reader, ok := repository.(ports.CompleteTVTrackingHintReader)
	if !ok {
		return func(context.Context) error { return nil }, nil
	}
	evidence, e := reader.ReadCompleteTVTrackingHintForLease(ctx, request.Lease)
	if e != nil {
		return nil, e
	}
	if evidence == nil {
		return func(context.Context) error { return nil }, nil
	}
	if evidence.Digest == "" || evidence.Receipt.JobID != request.Lease.ID || evidence.Receipt.OperationID != request.OperationID || evidence.Receipt.TaskID != request.TaskID {
		return nil, ports.ErrCompleteTVObservationStale
	}
	frozen := *evidence
	m.mu.RLock()
	files := m.trackingHintFiles
	m.mu.RUnlock()
	if files == nil {
		return nil, ports.ErrUnavailable
	}
	return func(callCtx context.Context) error {
		// An event-bound result may not escape cancellation through fact writeback.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := callCtx.Err(); err != nil {
			return err
		}
		current, err := reader.ReadCompleteTVTrackingHintForLease(callCtx, request.Lease)
		if err != nil {
			return err
		}
		if current == nil || current.Digest != frozen.Digest {
			return ports.ErrCompleteTVObservationStale
		}
		return probeCompleteTVHintRoots(callCtx, files, frozen)
	}, nil
}

func probeCompleteTVHintRoots(ctx context.Context, files ports.RootFilesystem, e ports.CompleteTVTrackingHintEvidence) error {
	b := e.Binding
	canonical := func(p string) bool {
		return filepath.IsAbs(p) && p != "/" && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
	}
	if files == nil || !canonical(b.RootPath) || !canonical(b.LocalRoot) || b.RootIdentity.Inode <= 0 || e.SourceIdentity.Inode <= 0 {
		return ports.ErrCompleteTVObservationStale
	}
	root, err := files.OpenRoot(ctx, b.RootPath)
	if err != nil {
		return ports.ErrCompleteTVObservationStale
	}
	defer root.Close()
	id, err := root.Identity(ctx)
	if err != nil || id.Device != b.RootIdentity.Device || id.Inode != b.RootIdentity.Inode {
		return ports.ErrCompleteTVObservationStale
	}
	marker := b.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if marker == "." || marker == ".." || filepath.Base(marker) != marker || strings.ContainsAny(marker, "\x00\r\n\\") {
		return ports.ErrCompleteTVObservationStale
	}
	if _, err = root.Inspect(ctx, marker); err != nil {
		return ports.ErrCompleteTVObservationStale
	}
	source, err := files.OpenRoot(ctx, b.LocalRoot)
	if err != nil {
		return ports.ErrCompleteTVObservationStale
	}
	defer source.Close()
	id, err = source.Identity(ctx)
	if err != nil || id.Device != e.SourceIdentity.Device || id.Inode != e.SourceIdentity.Inode {
		return ports.ErrCompleteTVObservationStale
	}
	return ctx.Err()
}

type completeTVHintReadClient struct {
	ports.CompleteTVTrackingClient
	probe func(context.Context) error
}

func (c completeTVHintReadClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	if err := c.probe(ctx); err != nil {
		return nil, err
	}
	rows, err := c.CompleteTVTrackingClient.ListTorrents(ctx, tag)
	if fence := c.probe(ctx); fence != nil {
		return nil, fence
	}
	return rows, err
}
func (c completeTVHintReadClient) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	if err := c.probe(ctx); err != nil {
		return nil, err
	}
	files, err := c.CompleteTVTrackingClient.GetTorrentFiles(ctx, hash)
	if fence := c.probe(ctx); fence != nil {
		return nil, fence
	}
	return files, err
}
func (c completeTVHintReadClient) GetTorrentMetainfo(ctx context.Context, hash string) ([]byte, error) {
	if err := c.probe(ctx); err != nil {
		return nil, err
	}
	metainfo, err := c.CompleteTVTrackingClient.GetTorrentMetainfo(ctx, hash)
	if fence := c.probe(ctx); fence != nil {
		return nil, fence
	}
	return metainfo, err
}
