package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Only the serial owned forwarder allocates event identity and sequence. A
// continuation retains the original hint rather than refreshing its receipt.
func (a *App) decorateDownloadWatchHint(s *downloadWatchSourceRun, identity ports.FileIdentity, hint ports.RootChangeHint) (ports.RootChangeHint, error) {
	hint.Metadata = ports.RootChangeMetadata{}
	s.metadataMu.RLock()
	manifest := s.manifestAt
	s.metadataMu.RUnlock()
	if manifest.IsZero() || hint.Kind == ports.RootChangeRescan {
		return hint, nil
	}
	if a == nil || a.clock == nil || a.idgen == nil || s.instanceID == uuid.Nil || identity.Inode <= 0 {
		return ports.RootChangeHint{}, ports.ErrUnavailable
	}
	s.sequence++
	if s.sequence <= 0 {
		return ports.RootChangeHint{}, ports.ErrUnavailable
	}
	id := a.idgen.NewUUID()
	now := a.clock.Now()
	if id == uuid.Nil || now.IsZero() {
		return ports.RootChangeHint{}, ports.ErrUnavailable
	}
	hint.Metadata = ports.RootChangeMetadata{EventID: id, InstanceID: s.instanceID, Sequence: s.sequence, ReceivedAt: now, ManifestAt: manifest, SourceIdentity: ports.FileIdentity{Device: identity.Device, Inode: identity.Inode}}
	return hint, nil
}
