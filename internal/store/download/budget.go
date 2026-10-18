package download

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

const downloaderPhysicalCapacity = 2

// acquireClientBudget belongs to this explicitly constructed Manager. A client
// replacement retains the budget; cancellation only stops waiting, never an
// admitted physical callback. No goroutine is launched by admission.
func (m *Manager) acquireClientBudget(ctx context.Context, id uuid.UUID) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.slots == nil {
		m.slots = make(map[uuid.UUID]chan struct{})
	}
	slots := m.slots[id]
	if slots == nil {
		slots = make(chan struct{}, downloaderPhysicalCapacity)
		m.slots[id] = slots
	}
	m.mu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-slots }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
