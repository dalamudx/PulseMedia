package app

import (
	"context"
	"log/slog"
)

// waitRuntime retains physical ownership after the logical drain budget ends.
func waitRuntime(ctx context.Context, logger *slog.Logger, done ...<-chan struct{}) {
	exceeded := false
	for _, completed := range done {
		select {
		case <-completed:
			continue
		default:
		}
		if !exceeded {
			select {
			case <-completed:
				continue
			case <-ctx.Done():
				exceeded = true
				logger.Warn("worker drain budget elapsed; waiting for owned runtime")
			}
		}
		<-completed
	}
}
