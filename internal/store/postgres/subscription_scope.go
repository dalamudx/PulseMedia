package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func (s *SubscriptionControlStore) applyScope(ctx context.Context, q *db.Queries, sub db.Subscription, c ports.SubscriptionControlCommand) (db.Subscription, error) {
	units, e := q.LockScopeMediaUnits(ctx, sub.MediaID)
	if e != nil {
		return sub, e
	}
	media, e := q.LockSubmissionMedia(ctx, sub.MediaID)
	if e != nil {
		return sub, e
	}
	known := map[uuid.UUID]bool{}
	for _, u := range units {
		known[u.ID] = true
	}
	for _, id := range *c.SelectedUnitIDs {
		if !known[id] {
			return sub, ports.ErrInvalidSubscriptionBindings
		}
	}
	if media.MediaType != "tv" && (*c.IncludeSpecials || len(*c.Seasons) > 0) {
		return sub, ports.ErrInvalidSubscriptionBindings
	}
	raw, _ := json.Marshal(*c.Seasons)
	sub, e = q.UpdateSubscriptionTVScope(ctx, db.UpdateSubscriptionTVScopeParams{ID: sub.ID, ScopeType: *c.ScopeType, Seasons: raw, IncludeSpecials: *c.IncludeSpecials})
	if e != nil {
		return sub, e
	}
	selected, _ := json.Marshal(*c.SelectedUnitIDs)
	if e = q.StoreSubscriptionSelectedUnits(ctx, db.StoreSubscriptionSelectedUnitsParams{ID: sub.ID, SelectedUnitIds: selected}); e != nil {
		return sub, e
	}
	scope := subscription.Scope{Type: *c.ScopeType, Seasons: *c.Seasons, IncludeSpecials: *c.IncludeSpecials, Selected: *c.SelectedUnitIDs}
	for _, u := range units {
		included := subscription.ScopeContains(scope, subscription.ScopeUnit{ID: u.ID, Season: int(u.SeasonNumber.Int32), TV: media.MediaType == "tv", Retired: u.Retired})
		if _, e = q.ApplyScopeWanted(ctx, db.ApplyScopeWantedParams{ID: s.ids.NewUUID(), SubscriptionID: sub.ID, UnitID: u.ID, IntentVersion: sub.IntentVersion, IsSkipped: !included}); e != nil {
			return sub, e
		}
	}
	return sub, nil
}
