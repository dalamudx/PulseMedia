package ports

import (
	"context"
	"github.com/google/uuid"
)

// This connection material is internal and never part of a response or receipt.
type SeedingClientSource struct {
	ClientID    uuid.UUID `json:"-"`
	ConfigStamp string    `json:"-"`
	Kind        string    `json:"-"`
	URL         string    `json:"-"`
	Username    string    `json:"-"`
	Credential  string    `json:"-"`
}
type SeedingClientSourceReader interface {
	ReadSeedingClientSource(context.Context, uuid.UUID, string) (SeedingClientSource, error)
}
