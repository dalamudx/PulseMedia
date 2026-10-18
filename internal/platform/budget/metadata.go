package budget

import (
	"context"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const metadataCapacity = 4
const metadataTokenInterval = 250 * time.Millisecond

// MetadataBudget bounds physical requests and shares a token bucket and cooldown.
// It owns no goroutines. Applications retain it across credential rotation.
type MetadataBudget struct {
	slots      chan struct{}
	clock      ports.Clock
	mu         sync.Mutex
	tokens     float64
	refilledAt time.Time
	cooldown   time.Time
}

var _ ports.MetadataRequestBudget = (*MetadataBudget)(nil)

func NewMetadataBudget(clock ports.Clock) *MetadataBudget {
	b := &MetadataBudget{slots: make(chan struct{}, metadataCapacity), clock: clock, tokens: metadataCapacity}
	b.refilledAt = b.now()
	return b
}

func (b *MetadataBudget) now() time.Time {
	if b.clock != nil {
		return b.clock.Now()
	}
	return time.Now().UTC()
}

func (b *MetadataBudget) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-b.slots }) }
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		b.mu.Lock()
		now := b.now()
		if now.Before(b.cooldown) {
			remaining := b.cooldown.Sub(now)
			b.mu.Unlock()
			release()
			return nil, ports.NewRateLimitError(remaining, "metadata provider cooldown")
		}
		if now.After(b.refilledAt) {
			b.tokens = min(float64(metadataCapacity), b.tokens+float64(now.Sub(b.refilledAt))/float64(metadataTokenInterval))
			b.refilledAt = now
		}
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return release, nil
		}
		delay := max(time.Nanosecond, time.Duration((1-b.tokens)*float64(metadataTokenInterval)))
		b.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *MetadataBudget) RecordRateLimit(retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = 5 * time.Second
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until := b.now().Add(retryAfter)
	if until.After(b.cooldown) {
		b.cooldown = until
	}
}
