package budget

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var (
	ErrEndpointRateLimited = errors.New("endpoint is currently in cooldown due to remote rate limit")
)

type EndpointBudget struct {
	minInterval time.Duration
	cooldown    time.Time
	lastRequest time.Time
	mu          sync.Mutex
}

type Manager struct {
	budgets     map[string]*EndpointBudget
	minInterval time.Duration
	clock       ports.Clock
	mu          sync.RWMutex
}

func NewManager(defaultMinInterval time.Duration, clock ports.Clock) *Manager {
	if defaultMinInterval <= 0 {
		defaultMinInterval = 1 * time.Second
	}
	return &Manager{
		budgets:     make(map[string]*EndpointBudget),
		minInterval: defaultMinInterval,
		clock:       clock,
	}
}

func (m *Manager) getBudget(endpointID string) *EndpointBudget {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, ok := m.budgets[endpointID]
	if !ok {
		b = &EndpointBudget{
			minInterval: m.minInterval,
		}
		m.budgets[endpointID] = b
	}
	return b
}

// Acquire waits for rate limit interval and verifies endpoint is not in cooldown.
func (m *Manager) Acquire(ctx context.Context, endpointID string) error {
	b := m.getBudget(endpointID)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := m.now()

	// Check if in cooldown from a previous 429
	if now.Before(b.cooldown) {
		return ErrEndpointRateLimited
	}

	// Calculate wait time to enforce minimum interval
	elapsed := now.Sub(b.lastRequest)
	if elapsed < b.minInterval {
		wait := b.minInterval - elapsed
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}

	b.lastRequest = m.now()
	return nil
}

// RecordRateLimit puts only this specific endpoint into cooldown with backoff and jitter.
func (m *Manager) RecordRateLimit(endpointID string, retryAfter time.Duration) {
	b := m.getBudget(endpointID)
	b.mu.Lock()
	defer b.mu.Unlock()

	if retryAfter <= 0 {
		retryAfter = 5 * time.Second
	}

	// Add jitter (up to 20% random variation)
	jitter := time.Duration(rand.Int63n(int64(retryAfter / 5)))
	b.cooldown = m.now().Add(retryAfter + jitter)
}

func (m *Manager) now() time.Time {
	if m.clock != nil {
		return m.clock.Now()
	}
	return time.Now().UTC()
}
