package mediaserver

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"path/filepath"
	"sync/atomic"
)

type RefreshPermit struct {
	server   uuid.UUID
	released atomic.Bool
	release  func()
}

func (p *RefreshPermit) Release() {
	if p != nil && p.released.CompareAndSwap(false, true) {
		p.release()
	}
}
func (s *SyncService) AcquireRefreshPermit(ctx context.Context, server uuid.UUID) (*RefreshPermit, error) {
	release, e := s.confirmations.AcquireMediaRefreshPermit(ctx, server)
	if e != nil {
		return nil, e
	}
	return &RefreshPermit{server: server, release: release}, nil
}
func (s *SyncService) ProcessRefreshDelivery(ctx context.Context, id, server uuid.UUID, owner string, token int64, localPath string) error {
	permit, e := s.AcquireRefreshPermit(ctx, server)
	if e != nil {
		return e
	}
	defer permit.Release()
	return s.ProcessReservedRefreshDelivery(ctx, permit, id, server, owner, token, localPath)
}
func (s *SyncService) ProcessReservedRefreshDelivery(ctx context.Context, permit *RefreshPermit, id, server uuid.UUID, owner string, token int64, localPath string) error {
	if permit == nil || permit.server != server || permit.released.Load() {
		return ports.ErrUnavailable
	}
	input, e := s.confirmations.LoadMediaRefresh(ctx, id, server, owner, token)
	if e != nil {
		return e
	}
	if localPath != "" && filepath.Clean(localPath) != input.Confirmation.LocalPath {
		return ports.ErrBadResponse
	}
	client, e := s.clientForConnection(input.Confirmation.Server)
	if e != nil {
		return e
	}
	if e = s.confirmations.ValidateMediaRefresh(ctx, *input); e != nil {
		return e
	}
	if e = client.RefreshPaths(ctx, []string{input.Confirmation.RemotePath}); e != nil {
		if fail := s.outbox.Fail(ctx, id, owner, token, "media server refresh failed", 0); fail != nil {
			return fail
		}
		return ErrServerUnreachable
	}
	return s.confirmations.FinishMediaRefresh(ctx, *input)
}
