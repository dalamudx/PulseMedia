package idgen

import (
	"sync"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.IDGen = (*UUIDv7Generator)(nil)
var _ ports.IDGen = (*MockIDGen)(nil)

// UUIDv7Generator generates time-ordered UUIDv7 identifiers.
type UUIDv7Generator struct{}

func New() *UUIDv7Generator {
	return &UUIDv7Generator{}
}

func (g *UUIDv7Generator) NewUUID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails if crypto/rand fails, fallback to V4
		return uuid.New()
	}
	return id
}

func (g *UUIDv7Generator) NewUUIDString() string {
	return g.NewUUID().String()
}

// MockIDGen generates predetermined or sequential UUIDs for testing.
type MockIDGen struct {
	mu       sync.Mutex
	queue    []uuid.UUID
	fallback uuid.UUID
}

func NewMock(ids ...uuid.UUID) *MockIDGen {
	return &MockIDGen{queue: ids}
}

func (m *MockIDGen) NewUUID() uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) > 0 {
		id := m.queue[0]
		m.queue = m.queue[1:]
		m.fallback = id
		return id
	}
	if m.fallback != uuid.Nil {
		return m.fallback
	}
	return uuid.Nil
}

func (m *MockIDGen) NewUUIDString() string {
	return m.NewUUID().String()
}

func (m *MockIDGen) Append(ids ...uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, ids...)
}
