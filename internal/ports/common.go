package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Clock provides time abstraction for testability.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

// IDGen provides unique identifier generation.
type IDGen interface {
	NewUUID() uuid.UUID
	NewUUIDString() string
}

// ActorType represents the initiator of an operation.
type ActorType string

const (
	ActorTypeUser    ActorType = "user"
	ActorTypeService ActorType = "service"
	ActorTypeSystem  ActorType = "system"
)

// Actor represents an authenticated identity executing a command or query.
type Actor struct {
	Type   ActorType `json:"type"`
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Role   string    `json:"role"` // "admin", "member"
	Scopes []string  `json:"scopes,omitempty"`
}

// HealthStatus represents liveness of the service.
type HealthStatus struct {
	Alive   bool   `json:"alive"`
	Version string `json:"version"`
}

// ReadyStatus represents readiness of the service and its critical dependencies.
type ReadyStatus struct {
	Ready  bool            `json:"ready"`
	Reason string          `json:"reason,omitempty"`
	Checks map[string]bool `json:"checks,omitempty"`
}

// ReadinessChecker checks whether a component or system is ready for traffic.
type ReadinessChecker interface {
	CheckReady(ctx context.Context) ReadyStatus
}
