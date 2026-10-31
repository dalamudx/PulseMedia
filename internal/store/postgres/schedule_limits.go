package postgres

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// ScheduleLimits only controls local polling, never execution authorization.
// Each store owns a separate instance, accessed under its existing scan mutex.
type ScheduleLimits struct {
	clock    ports.Clock
	failures map[uuid.UUID]scheduleFailure
}
type scheduleFailure struct {
	version  int64
	attempts uint8
	retryAt  time.Time
}

// NewScheduleLimits enables bounded polling for a production scanner. Recreating
// the scanner clears local cooldowns; database fences still govern every attempt.
func NewScheduleLimits(clock ports.Clock) *ScheduleLimits {
	if clock == nil {
		panic("schedule limits require a clock")
	}
	return &ScheduleLimits{clock: clock, failures: make(map[uuid.UUID]scheduleFailure)}
}

func optionalScheduleLimits(values []*ScheduleLimits) *ScheduleLimits {
	if len(values) > 1 {
		panic("one schedule control per store")
	}
	if len(values) == 1 {
		return values[0]
	}
	return nil
}
func (s *ScheduleLimits) run(ctx context.Context, id uuid.UUID, version int64, attempt func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.allowed(id, version) {
		return nil
	}
	err := attempt()
	if ctx.Err() == nil {
		s.record(id, version, err)
	}
	return err
}
func (s *ScheduleLimits) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if s == nil {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 30*time.Second)
}
func (s *ScheduleLimits) allowed(id uuid.UUID, version int64) bool {
	if s == nil {
		return true
	}
	f, ok := s.failures[id]
	return !ok || f.version != version || !s.clock.Now().Before(f.retryAt)
}
func (s *ScheduleLimits) record(id uuid.UUID, version int64, err error) {
	if s == nil {
		return
	}
	if err == nil {
		delete(s.failures, id)
		return
	}
	f := s.failures[id]
	if f.version != version {
		f = scheduleFailure{version: version}
	}
	if f.attempts < 5 {
		f.attempts++
	}
	if _, exists := s.failures[id]; !exists && len(s.failures) >= 4096 {
		var oldest uuid.UUID
		var earliest time.Time
		for key, value := range s.failures {
			if earliest.IsZero() || value.retryAt.Before(earliest) {
				oldest = key
				earliest = value.retryAt
			}
		}
		delete(s.failures, oldest)
	}
	delay := min(5*time.Second*time.Duration(1<<uint(f.attempts-1)), time.Minute)
	f.retryAt = s.clock.Now().Add(delay)
	s.failures[id] = f
}
