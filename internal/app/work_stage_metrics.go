package app

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"sync"
	"time"
)

// Owned by an App, with no background work or object-identity labels.
type workStageRecorder struct {
	mu     sync.Mutex
	values [ports.WorkStageCount]ports.WorkStageMetrics
}

func (r *workStageRecorder) begin(stage ports.WorkStage) func() {
	started := time.Now()
	r.mu.Lock()
	r.values[stage].InFlight++
	r.mu.Unlock()
	return sync.OnceFunc(func() {
		seconds := time.Since(started).Seconds()
		r.mu.Lock()
		defer r.mu.Unlock()
		s := &r.values[stage]
		s.InFlight--
		s.Returned++
		s.Seconds += seconds
		for i, bound := range ports.WorkStageDurationBounds() {
			if seconds <= bound {
				s.Buckets[i]++
			}
		}
	})
}
func (r *workStageRecorder) snapshot() [ports.WorkStageCount]ports.WorkStageMetrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values
}
