package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/google/uuid"
)

func TestScheduleLimitsVersionRetryAndRecovery(t *testing.T) {
	c := clock.NewMock(time.Now())
	s := NewScheduleLimits(c)
	id := uuid.New()
	failure := errors.New("unavailable")
	for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
		if !s.allowed(id, 1) {
			t.Fatal("due retry blocked")
		}
		s.record(id, 1, failure)
		if s.allowed(id, 1) || !s.allowed(id, 2) {
			t.Fatal("cooldown ignored or configuration revision blocked")
		}
		c.Set(c.Now().Add(delay - time.Millisecond))
		if s.allowed(id, 1) {
			t.Fatal("early retry")
		}
		c.Set(c.Now().Add(time.Millisecond))
	}
	s.record(id, 2, failure)
	c.Set(c.Now().Add(5 * time.Second))
	if !s.allowed(id, 2) {
		t.Fatal("new version inherited old backoff")
	}
	s.record(id, 2, nil)
	if len(s.failures) != 0 || !s.allowed(id, 2) {
		t.Fatal("successful attempt retained failure")
	}
	s.record(id, 2, failure)
	if !NewScheduleLimits(c).allowed(id, 2) {
		t.Fatal("reconstruction must recheck database authority")
	}
}
func TestScheduleLimitsMemoryAndCancellation(t *testing.T) {
	c := clock.NewMock(time.Now())
	s := NewScheduleLimits(c)
	for range 4100 {
		s.record(uuid.New(), 1, errors.New("unavailable"))
		c.Set(c.Now().Add(time.Millisecond))
	}
	if len(s.failures) != 4096 {
		t.Fatal("unbounded failure memory", len(s.failures))
	}
	parent, cancel := context.WithCancel(t.Context())
	bounded, stop := s.bound(parent)
	defer stop()
	deadline, ok := bounded.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		t.Fatal("batch is not bounded")
	}
	cancel()
	if !errors.Is(bounded.Err(), context.Canceled) {
		t.Fatal("parent cancellation ignored")
	}
	short, release := context.WithTimeout(t.Context(), time.Second)
	defer release()
	bounded, stop = s.bound(short)
	defer stop()
	d, _ := short.Deadline()
	got, _ := bounded.Deadline()
	if !got.Equal(d) {
		t.Fatal("caller budget extended")
	}
}
