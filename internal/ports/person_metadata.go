package ports

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/domain/personfollow"
)

// PersonCredits are provider relationship evidence, not permission to subscribe.
type PersonCredits struct {
	PersonID int64
	Role     string
	Works    []PersonWork
}

type PersonWork = personfollow.Work

type PersonCreditsProvider interface {
	GetPersonCredits(ctx context.Context, personID int64, role string) (PersonCredits, error)
}
