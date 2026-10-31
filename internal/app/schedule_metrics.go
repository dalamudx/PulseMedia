package app

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// A worker owns nextReport. Metrics retain every failed cycle while logs aggregate
// repeated failures without object identities, credentials or database errors.
func (a *App) enqueueScheduled(ctx context.Context, stage ports.WorkStage, nextReport *time.Time, enqueue func(context.Context) error) {
	end := a.stages.begin(stage)
	err := enqueue(ctx)
	end()
	if err == nil || ctx.Err() != nil {
		return
	}
	a.stages.mu.Lock()
	a.stages.values[stage].Failures++
	a.stages.mu.Unlock()
	now := time.Now()
	if !now.Before(*nextReport) {
		a.logger.Error("periodic scheduling unavailable", "stage", ports.WorkStageLabels()[stage])
		*nextReport = now.Add(time.Minute)
	}
}
