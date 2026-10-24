package cookiecloud

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type Runner struct {
	repo   ports.CookieCloudSyncRepository
	reader ports.CookieCloudReader
}

func NewRunner(repo ports.CookieCloudSyncRepository, reader ports.CookieCloudReader) *Runner {
	return &Runner{repo: repo, reader: reader}
}
func syncError(err error) error {
	if err == nil {
		return nil
	}
	if limit, ok := ports.IsRateLimited(err); ok {
		delay := limit.RetryAfter
		if delay <= 0 {
			delay = 10 * time.Second
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		return ports.NewRateLimitError(delay, "CookieCloud read limited")
	}
	for _, safe := range []error{context.Canceled, ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrNotFound, ports.ErrBadResponse, ports.ErrRejected, ports.ErrTimeout, ports.ErrUnavailable, ports.ErrForbidden, ports.ErrCookieCloudConfigConflict, ports.ErrCookieCloudSyncLeaseLost} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

// Execute issues at most one read and never releases exclusion while native I/O
// still executes. Cancellation withdraws positive writeback authority immediately.
func (s *Runner) Execute(ctx context.Context, lease ports.JobLease) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if lease.ID == uuid.Nil || strings.TrimSpace(lease.Owner) == "" || lease.Token < 1 {
		return ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.reader == nil {
		return ports.ErrUnavailable
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return syncError(err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	execution := permit.Snapshot()
	if execution.Config.OwnerID == uuid.Nil || execution.Config.Version < 1 || !execution.Config.Enabled || !execution.Config.HasConnection || execution.Connection.ServerURL == "" || execution.Connection.UserKey == "" || execution.Connection.Password == "" {
		return ports.ErrRejected
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = permit.Reserve(ctx); err != nil {
		return syncError(err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = permit.Check(ctx); err != nil {
		return syncError(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	backup, readErr := s.reader.ReadBackup(readCtx, execution.Connection.ServerURL, execution.Connection.UserKey, execution.Connection.Password)
	requestErr := readCtx.Err()
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if requestErr != nil {
		readErr = requestErr
	}
	if err = permit.Check(ctx); err != nil {
		return syncError(err)
	}
	if readErr == nil && (backup == nil || backup.Cookies == nil) {
		readErr = ports.ErrBadResponse
	}
	if readErr != nil {
		backup = nil
	}
	failure := syncError(readErr)
	if err = permit.Finish(ctx, backup, failure); err != nil {
		return syncError(err)
	}
	return failure
}
