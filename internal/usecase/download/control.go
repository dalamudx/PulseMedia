package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"time"
)

type ControlService struct {
	repo ports.DownloadControlRepository
}

func NewControlService(repo ports.DownloadControlRepository) *ControlService {
	return &ControlService{repo: repo}
}
func controlInput(a *ports.Actor, op uuid.UUID, key string) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return ports.ErrForbidden
	}
	if op == uuid.Nil || key == "" || len(key) > 160 || key != strings.TrimSpace(key) || strings.ContainsAny(key, "\x00\r\n") {
		return ports.ErrDownloadControlInvalid
	}
	return nil
}
func (s *ControlService) Get(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (*ports.DownloadControlReceipt, error) {
	if e := controlInput(a, op, key); e != nil {
		return nil, e
	}
	return s.repo.GetDownloadControl(ctx, a, op, key)
}
func (s *ControlService) Apply(ctx context.Context, a *ports.Actor, op uuid.UUID, key string, c ports.DownloadControlCommand) (*ports.DownloadControlReceipt, error) {
	if e := controlInput(a, op, key); e != nil {
		return nil, e
	}
	if !c.Confirmed || (c.Action != "pause" && c.Action != "resume") {
		return nil, ports.ErrDownloadControlInvalid
	}
	return s.execute(ctx, a, op, key, c, false)
}
func (s *ControlService) Observe(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (*ports.DownloadControlReceipt, error) {
	if e := controlInput(a, op, key); e != nil {
		return nil, e
	}
	return s.execute(ctx, a, op, key, ports.DownloadControlCommand{}, true)
}
func (s *ControlService) execute(ctx context.Context, a *ports.Actor, op uuid.UUID, key string, c ports.DownloadControlCommand, observe bool) (*ports.DownloadControlReceipt, error) {
	p, e := s.repo.PrepareDownloadControl(ctx, a, op, key, c, observe)
	if p == nil {
		if e == nil {
			e = ports.ErrUnavailable
		}
		return nil, e
	}
	if p.Release != nil {
		defer p.Release()
	}
	if e != nil {
		return p.Receipt, e
	}
	if p.Client == nil {
		return p.Receipt, nil
	}
	if p.Complete == nil || p.Release == nil {
		return nil, ports.ErrUnavailable
	}
	info, e := p.Client.ReadTorrentPaused(ctx, p.InfoHash)
	if !observe {
		if e != nil {
			return p.Receipt, e
		}
		if p.Arm == nil {
			return p.Receipt, ports.ErrUnavailable
		}
		if e = p.Arm(ctx, info); e != nil {
			return p.Receipt, e
		}
		// Never dispatch if cancellation was already visible after permit commit.
		e = ctx.Err()
		if e == nil {
			e = p.Client.SetTorrentPaused(ctx, p.InfoHash, c.Action == "pause")
		}
		info = nil
	}
	// Cancellation or HTTP disconnect cannot release the gate before writeback.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return p.Complete(finish, e, info)
}
