package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ControlService struct {
	repo ports.SubscriptionControlRepository
}

func NewControlService(repo ports.SubscriptionControlRepository) *ControlService {
	return &ControlService{repo}
}
func (s *ControlService) Apply(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.SubscriptionControlCommand, key string) (*ports.SubscriptionControlReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || c.ExpectedVersion < 1 || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if c.Action != "scope" && (c.ScopeType != nil || c.Seasons != nil || c.IncludeSpecials != nil || c.SelectedUnitIDs != nil) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if c.Action != "release_block" && (c.CandidateID != nil || c.Blocked != nil) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	switch c.Action {
	case "state":
		if (c.State != "active" && c.State != "paused" && c.State != "archived") || c.UnitID != nil || c.Skipped != nil {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	case "skip":
		if c.UnitID == nil || *c.UnitID == uuid.Nil || c.Skipped == nil || c.State != "" {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	case "recheck":
		if c.UnitID == nil || *c.UnitID == uuid.Nil || c.Skipped != nil || c.State != "" {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	case "release_block":
		if c.CandidateID == nil || *c.CandidateID == uuid.Nil || c.Blocked == nil || c.UnitID != nil || c.Skipped != nil || c.State != "" {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	case "scope":
		if c.ScopeType == nil || (*c.ScopeType != "all" && *c.ScopeType != "selected") || c.Seasons == nil || c.IncludeSpecials == nil || c.SelectedUnitIDs == nil || c.UnitID != nil || c.Skipped != nil || c.State != "" {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
		seen := map[int]bool{}
		for _, season := range *c.Seasons {
			if season < 0 || season > 999 || seen[season] || season == 0 && !*c.IncludeSpecials {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
			seen[season] = true
		}
		ids := map[uuid.UUID]bool{}
		for _, id := range *c.SelectedUnitIDs {
			if id == uuid.Nil || ids[id] {
				return nil, ports.ErrInvalidSubscriptionBindings
			}
			ids[id] = true
		}
		if *c.ScopeType == "all" && len(*c.SelectedUnitIDs) > 0 {
			return nil, ports.ErrInvalidSubscriptionBindings
		}
	default:
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	raw, _ := json.Marshal([]any{"subscription.control", id, c})
	hash := sha256.Sum256(raw)
	return s.repo.ControlSubscription(ctx, a, id, c, key, hex.EncodeToString(hash[:]))
}
