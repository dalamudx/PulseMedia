package clock

import (
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.Clock = (*RealClock)(nil)
var _ ports.Clock = (*MockClock)(nil)

// RealClock implements ports.Clock using the standard time library.
type RealClock struct{}

func New() *RealClock {
	return &RealClock{}
}

func (c *RealClock) Now() time.Time {
	return time.Now().UTC()
}

func (c *RealClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}

// MockClock provides a controllable clock for deterministic testing.
type MockClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewMock(t time.Time) *MockClock {
	return &MockClock{now: t.UTC()}
}

func (m *MockClock) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.now
}

func (m *MockClock) Since(t time.Time) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.now.Sub(t)
}

func (m *MockClock) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
}

func (m *MockClock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
