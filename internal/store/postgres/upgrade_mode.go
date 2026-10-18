package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *UpgradeReservationStore) SetUpgradeMode(ctx context.Context, a *ports.Actor, c ports.SetUpgradeModeCommand, key string) (out *ports.UpgradeModeResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	conflict := ports.ErrUpgradeReservationConflict
	if !upgradeKey(key) || !c.Confirmed || c.SubscriptionID == uuid.Nil || c.ExpectedIntent < 1 || (c.Mode != ports.UpgradeDisabled && !c.Mode.Enabled()) {
		return nil, conflict
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	q, sub, e := lockUpgradeCommand(ctx, tx, a, c.SubscriptionID)
	if e != nil {
		return nil, e
	}
	key = "upgrade.mode:" + key
	hash := coverageDigest([]any{"upgrade.mode.v1", c})
	var old ports.UpgradeModeResult
	if found, e := upgradeReceipt(ctx, q, a, key, hash, &old); e != nil {
		return nil, e
	} else if found {
		if old.SubscriptionID != sub.ID || old.Mode != c.Mode || old.IntentVersion <= c.ExpectedIntent {
			return nil, conflict
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &old, nil
	}
	if sub.IntentVersion != c.ExpectedIntent || sub.State != "active" {
		return nil, conflict
	}
	revision, e := q.GetPolicyRevision(ctx, struct {
		PolicyID string
		Revision int64
	}{sub.QualityPolicyID, sub.QualityPolicyRevision})
	if e != nil {
		return nil, e
	}
	var rules quality.PolicyRules
	if json.Unmarshal(revision.Rules, &rules) != nil || quality.ValidateRules(rules) != nil {
		return nil, ports.ErrUnavailable
	}
	var published int64
	if e = tx.QueryRow(ctx, "SELECT current_revision FROM policy WHERE id=$1", sub.QualityPolicyID).Scan(&published); e != nil {
		return nil, e
	}
	if c.Mode.Enabled() && (published != sub.QualityPolicyRevision || (c.Mode == ports.UpgradeStopAtCutoff && rules.CutoffScore <= 0)) {
		return nil, conflict
	}
	// Freeze every subscription field except the three deliberately changed here.
	var before, after json.RawMessage
	if e = tx.QueryRow(ctx, "SELECT to_jsonb(x)-ARRAY['upgrade_mode','intent_version','updated_at'] FROM subscription x WHERE id=$1", sub.ID).Scan(&before); e != nil {
		return nil, e
	}
	out = &ports.UpgradeModeResult{SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion + 1, Mode: c.Mode}
	_, e = tx.Exec(ctx, "UPDATE subscription SET upgrade_mode=$2,intent_version=intent_version+1,updated_at=clock_timestamp() WHERE id=$1", sub.ID, c.Mode)
	if e != nil {
		return nil, e
	}
	own := ownedUpgradeRows{}
	if e = own.remember(ctx, tx, "subscription", sub.ID); e != nil {
		return nil, e
	}
	check := func() error {
		if e := own.verify(ctx, tx); e != nil {
			return e
		}
		if e := lockAcquisitionUser(ctx, q, a); e != nil {
			return e
		}
		var mode ports.UpgradeMode
		var intent int64
		if e := tx.QueryRow(ctx, "SELECT upgrade_mode,intent_version,to_jsonb(x)-ARRAY['upgrade_mode','intent_version','updated_at'] FROM subscription x WHERE id=$1", sub.ID).Scan(&mode, &intent, &after); e != nil {
			return e
		}
		if mode != c.Mode || intent != out.IntentVersion || completeTVJSONDigest(before) != completeTVJSONDigest(after) {
			return conflict
		}
		var current int64
		if e := tx.QueryRow(ctx, "SELECT current_revision FROM policy WHERE id=$1", sub.QualityPolicyID).Scan(&current); e != nil {
			return e
		}
		if current != published {
			return conflict
		}
		return nil
	}
	if e = check(); e != nil {
		return nil, e
	}
	resource := s.ids.NewUUID()
	if e = s.upgradeSuccess(ctx, tx, a, sub.ID, key, hash, "subscription.upgrade_mode", resource, out, own, check); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
