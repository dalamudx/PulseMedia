package ports

import (
	"context"
	"github.com/google/uuid"
)

type SubscriptionControlCommand struct {
	ExpectedVersion int64        `json:"expected_version"`
	Action          string       `json:"action"`
	State           string       `json:"state,omitempty"`
	CandidateID     *uuid.UUID   `json:"candidate_id,omitempty"`
	Blocked         *bool        `json:"blocked,omitempty"`
	UnitID          *uuid.UUID   `json:"unit_id,omitempty"`
	Skipped         *bool        `json:"skipped,omitempty"`
	ScopeType       *string      `json:"scope_type,omitempty"`
	Seasons         *[]int       `json:"seasons,omitempty"`
	IncludeSpecials *bool        `json:"include_specials,omitempty"`
	SelectedUnitIDs *[]uuid.UUID `json:"selected_unit_ids,omitempty"`
}
type SubscriptionControlReceipt struct {
	ID            uuid.UUID  `json:"id"`
	State         string     `json:"state"`
	IntentVersion int64      `json:"intent_version"`
	UnitID        *uuid.UUID `json:"unit_id,omitempty"`
	Skipped       *bool      `json:"skipped,omitempty"`
	CandidateID   *uuid.UUID `json:"candidate_id,omitempty"`
	Blocked       *bool      `json:"blocked,omitempty"`
}
type SubscriptionControlRepository interface {
	ControlSubscription(context.Context, *Actor, uuid.UUID, SubscriptionControlCommand, string, string) (*SubscriptionControlReceipt, error)
}
