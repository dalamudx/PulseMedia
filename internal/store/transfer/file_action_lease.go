package transfer

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func (s *Service) renewTransferAuthority(ctx context.Context, plan db.TransferPlan, authority []ports.JobLease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if err = guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return ErrExecutionRejected
	}
	if err = guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The caller retains the physical roots while work runs synchronously. The
// owned renewer cancels work on fence loss and is always stopped and joined;
// even cancellation cannot release slots while an I/O call is still returning.
func withTransferLease(ctx context.Context, renew func(context.Context) error, work func(context.Context) error, interval time.Duration) error {
	if err := renew(ctx); err != nil {
		return err
	}
	workCtx, stopWork := context.WithCancel(ctx)
	defer stopWork()
	renewCtx, stopRenew := context.WithCancel(workCtx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := renew(renewCtx); err != nil {
					if renewCtx.Err() != nil {
						done <- nil
						return
					}
					stopWork()
					done <- err
					return
				}
			}
		}
	}()
	err := work(workCtx)
	stopRenew()
	if renewalErr := <-done; renewalErr != nil {
		return renewalErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
