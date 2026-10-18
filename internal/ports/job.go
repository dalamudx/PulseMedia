package ports

import "github.com/google/uuid"

// JobLease is explicit execution authority, never hidden in context.
type JobLease struct {
	ID    uuid.UUID
	Owner string
	Token int64
}
