package ports

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/google/uuid"
)

var ErrPersonFollowConflict = errors.New("person follow configuration or original command conflicts")

type PersonFollowInput struct {
	Settings        personfollow.Settings `json:"settings"`
	ExpectedVersion int64                 `json:"expected_version"`
}

type PersonFollowConfig struct {
	ID        uuid.UUID             `json:"id"`
	OwnerID   uuid.UUID             `json:"owner_id"`
	Settings  personfollow.Settings `json:"settings"`
	Version   int64                 `json:"version"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// A receipt proves only persisted follow settings; it never proves a subscription.
type PersonFollowReceipt struct {
	CommandKey string             `json:"command_key"`
	Config     PersonFollowConfig `json:"config"`
}

type PersonFollowRepository interface {
	ReadPersonFollow(context.Context, *Actor, uuid.UUID) (*PersonFollowConfig, error)
	ListPersonFollows(context.Context, *Actor) ([]PersonFollowConfig, error)
	SavePersonFollow(context.Context, *Actor, uuid.UUID, PersonFollowInput, string) (*PersonFollowReceipt, error)
	ReadPersonFollowReceipt(context.Context, *Actor, string) (*PersonFollowReceipt, error)
}
