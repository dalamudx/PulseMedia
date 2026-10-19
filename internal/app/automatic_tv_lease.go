package app

import (
	"context"
	"time"
)

// runAutomaticTVLeased owns its heartbeat and synchronously joins the executor.
// Cancellation or a lost lease never detaches I/O that ignores its context.
func runAutomaticTVLeased(ctx context.Context, budget, interval time.Duration, renew func(context.Context) error, execute func(context.Context) error) error {
	work, cancel := context.WithTimeout(ctx, budget)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				if err := renew(work); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := execute(work)
	observed := work.Err()
	cancel()
	<-done
	if err != nil {
		return err
	}
	return observed
}
