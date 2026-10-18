package subscription

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var (
	ErrInvalidState = errors.New("invalid subscription state (must be active, paused, or archived)")
	ErrInvalidScope = errors.New("invalid scope type (must be all or selected)")
)

type SubscriptionMediaInput = ports.SubscriptionMediaInput
type CreateSubscriptionCommand = ports.CreateSubscriptionCommand

type Service struct {
	intents ports.SubscriptionIntentUoW
	media   ports.SubscriptionMediaRepository
	control ports.SubscriptionControlRepository
	details ports.SubscriptionDetailReader
}

func NewService(intents ports.SubscriptionIntentUoW, media ports.SubscriptionMediaRepository, control ports.SubscriptionControlRepository, details ports.SubscriptionDetailReader) *Service {
	return &Service{intents, media, control, details}
}
func intentActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleMember && a.Role != ports.RoleAdmin) {
		return ports.ErrForbidden
	}
	if id, e := uuid.Parse(a.ID); e != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}

// CreateSubscription decides ownership, defaults and scope against locked material.
// The intent, optional media, wanted rows and publication commit together.
func (s *Service) CreateSubscription(ctx context.Context, actor *ports.Actor, cmd CreateSubscriptionCommand) (sub *ports.SubscriptionRecord, wanted []*ports.SubscriptionWanted, err error) {
	if err = intentActor(actor); err != nil {
		return nil, nil, err
	}
	owner, _ := uuid.Parse(actor.ID)
	if cmd.UserID != nil {
		if !actor.IsAdmin() && *cmd.UserID != owner {
			return nil, nil, ports.ErrForbidden
		}
		owner = *cmd.UserID
	}
	if owner == uuid.Nil || cmd.RootID == uuid.Nil {
		return nil, nil, ErrInvalidScope
	}
	if cmd.ScopeType == "" {
		cmd.ScopeType = "all"
	}
	if cmd.ScopeType != "all" && cmd.ScopeType != "selected" {
		return nil, nil, ErrInvalidScope
	}
	if cmd.QualityPolicyID == "" {
		cmd.QualityPolicyID = "default"
	}
	seasons := map[int]bool{}
	for _, n := range cmd.Seasons {
		if n < 0 || n > 999 || seasons[n] || (n == 0 && !cmd.IncludeSpecials) {
			return nil, nil, ErrInvalidScope
		}
		seasons[n] = true
	}
	var specs []ports.SubscriptionMediaUnitSpec
	if cmd.MediaInput != nil {
		if cmd.MediaID != uuid.Nil {
			return nil, nil, ErrInvalidScope
		}
		specs, err = subscriptionMediaUnits(*cmd.MediaInput)
		if err != nil {
			return nil, nil, err
		}
	} else if cmd.MediaID == uuid.Nil {
		return nil, nil, ErrInvalidScope
	}
	err = s.intents.Within(ctx, func(tx ports.SubscriptionIntentTx) error {
		frozen, e := tx.LockCreate(ctx, ports.SubscriptionCreateRequest{Actor: *actor, OwnerID: owner, Command: cmd, MediaUnits: specs})
		if e != nil {
			return e
		}
		if frozen.RootHealth != "online" || frozen.RootMediaType != frozen.MediaType || len(frozen.Units) == 0 {
			return ErrInvalidScope
		}
		cmd.MediaID = frozen.MediaID
		cmd.QualityPolicyRevision = frozen.PolicyRevision
		if frozen.Existing != nil {
			if !actor.CanAccessUser(frozen.Existing.UserID.String()) {
				return ports.ErrForbidden
			}
			if cmd.IndexerIDs == nil {
				cmd.IndexerIDs = frozen.Existing.IndexerIDs
			}
			if cmd.DownloaderID == nil {
				cmd.DownloaderID = frozen.Existing.DownloaderID
			}
		}
		bindings := map[uuid.UUID]bool{}
		for _, id := range cmd.IndexerIDs {
			if id == uuid.Nil || bindings[id] {
				return ports.ErrInvalidSubscriptionBindings
			}
			bindings[id] = true
		}
		if cmd.DownloaderID != nil && *cmd.DownloaderID == uuid.Nil {
			return ports.ErrInvalidSubscriptionBindings
		}
		if cmd.IndexerIDs == nil {
			cmd.IndexerIDs = []uuid.UUID{}
		}
		if cmd.Seasons == nil {
			cmd.Seasons = []int{}
		}
		selected := map[uuid.UUID]bool{}
		for _, id := range cmd.SelectedUnitIDs {
			if id == uuid.Nil || selected[id] {
				return ErrInvalidScope
			}
			selected[id] = true
		}
		keys := map[string]bool{}
		for _, key := range cmd.EpisodeKeys {
			if key == "" || keys[key] {
				return ErrInvalidScope
			}
			keys[key] = true
		}
		available := map[uuid.UUID]bool{}
		for _, unit := range frozen.Units {
			if unit.Retired {
				continue
			}
			if unit.SeasonNumber != nil && ((*unit.SeasonNumber == 0 && !cmd.IncludeSpecials) || (len(seasons) > 0 && !seasons[int(*unit.SeasonNumber)])) {
				continue
			}
			available[unit.ID] = true
			if keys[unit.UnitKey] {
				if !selected[unit.ID] {
					cmd.SelectedUnitIDs = append(cmd.SelectedUnitIDs, unit.ID)
				}
				selected[unit.ID] = true
				delete(keys, unit.UnitKey)
			}
		}
		if len(keys) > 0 {
			return ErrInvalidScope
		}
		for id := range selected {
			if !available[id] {
				return ErrInvalidScope
			}
		}
		if cmd.ScopeType == "all" && len(selected) > 0 {
			return ErrInvalidScope
		}
		if cmd.ScopeType == "selected" && len(selected) == 0 {
			return ErrInvalidScope
		}
		if cmd.SelectedUnitIDs == nil {
			cmd.SelectedUnitIDs = []uuid.UUID{}
		}
		decisions := make([]ports.SubscriptionWantedDecision, 0, len(available))
		for _, unit := range frozen.Units {
			if available[unit.ID] {
				decisions = append(decisions, ports.SubscriptionWantedDecision{UnitID: unit.ID, ScopeExcluded: cmd.ScopeType == "selected" && !selected[unit.ID]})
			}
		}
		if len(decisions) == 0 {
			return ErrInvalidScope
		}
		sub, wanted, e = tx.ApplyCreate(ctx, ports.SubscriptionCreateDecision{Command: cmd, Wanted: decisions})
		return e
	})
	if err != nil {
		return nil, nil, err
	}
	return sub, wanted, nil
}

func (s *Service) UpdateState(ctx context.Context, actor *ports.Actor, id uuid.UUID, state string) (out *ports.SubscriptionRecord, err error) {
	if err = intentActor(actor); err != nil {
		return nil, err
	}
	if state != "active" && state != "paused" && state != "archived" {
		return nil, ErrInvalidState
	}
	err = s.intents.Within(ctx, func(tx ports.SubscriptionIntentTx) error {
		current, e := tx.LockOwned(ctx, actor, id)
		if e != nil {
			return e
		}
		if !actor.CanAccessUser(current.Subscription.UserID.String()) {
			return ports.ErrForbidden
		}
		out, e = tx.ApplyState(ctx, state)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Service) SetWantedSkipped(ctx context.Context, actor *ports.Actor, id, unitID uuid.UUID, skipped bool) error {
	if e := intentActor(actor); e != nil {
		return e
	}
	return s.intents.Within(ctx, func(tx ports.SubscriptionIntentTx) error {
		current, e := tx.LockOwned(ctx, actor, id)
		if e != nil {
			return e
		}
		if !actor.CanAccessUser(current.Subscription.UserID.String()) {
			return ports.ErrForbidden
		}
		for _, w := range current.Wanted {
			if w.UnitID == unitID {
				return tx.ApplySkipped(ctx, unitID, skipped)
			}
		}
		return ports.ErrNotFound
	})
}

// UpdateTVScope uses the existing versioned command and an authorized snapshot.
func (s *Service) UpdateTVScope(ctx context.Context, a *ports.Actor, id uuid.UUID, scopeType string, seasons []int, specials bool, expectedVersion int64, key string) (*ports.SubscriptionRecord, []*ports.SubscriptionWanted, error) {
	if scopeType != "all" {
		return nil, nil, ErrInvalidScope
	}
	if seasons == nil {
		seasons = []int{}
	}
	selected := []uuid.UUID{}
	_, e := NewControlService(s.control).Apply(ctx, a, id, ports.SubscriptionControlCommand{Action: "scope", ExpectedVersion: expectedVersion, ScopeType: &scopeType, Seasons: &seasons, IncludeSpecials: &specials, SelectedUnitIDs: &selected}, key)
	if e != nil {
		return nil, nil, e
	}
	return s.details.ReadSubscriptionDetail(ctx, a, id)
}
