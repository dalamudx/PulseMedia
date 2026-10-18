package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type upgradeOrganizationBasis struct {
	review ports.MovieUpgradeOrganizationReview
	raw    json.RawMessage
	verify func() error
}

func (s *UpgradePreparationStore) organizationBasis(ctx context.Context, tx pgx.Tx, a *ports.Actor, op, obs uuid.UUID) (*upgradeOrganizationBasis, error) {
	session := &movieUpgradeTrackingSession{base: &movieUpgradeSendSession{store: s, operation: op}}
	_, sub, verify, e := session.load(ctx, tx, a)
	if e != nil {
		return nil, e
	}
	if obs == uuid.Nil {
		e = tx.QueryRow(ctx, `SELECT o.observation_id FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE o.operation_id=$1 AND r.outcome='completed' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=o.observation_id)`, op).Scan(&obs)
		if e != nil {
			return nil, preparationConflict()
		}
	}
	if e = validateOrganizationTrackingReceipt(ctx, tx, a, sub, op, obs); e != nil {
		return nil, e
	}
	b := &upgradeOrganizationBasis{}
	var mode ports.UpgradeMode
	if e = tx.QueryRow(ctx, `WITH b AS(SELECT pulse_upgrade_organization_basis($1,$2) value) SELECT value,COALESCE(encode(sha256(convert_to(value::text,'UTF8')),'hex'),''),(SELECT snapshot->>'mode' FROM movie_upgrade_authority WHERE operation_id=$1) FROM b`, op, obs).Scan(&b.raw, &b.review.InputStamp, &mode); e != nil {
		return nil, e
	}
	if len(b.raw) == 0 || !completeTVStamp(b.review.InputStamp) {
		return nil, preparationConflict()
	}
	var fields struct {
		OperationID    uuid.UUID           `json:"operation_id"`
		ObservationID  uuid.UUID           `json:"observation_id"`
		TaskID         uuid.UUID           `json:"task_id"`
		SubscriptionID uuid.UUID           `json:"subscription_id"`
		RootID         uuid.UUID           `json:"root_id"`
		UnitID         uuid.UUID           `json:"unit_id"`
		OldFileID      uuid.UUID           `json:"old_file_id"`
		OldFactID      uuid.UUID           `json:"old_fact_id"`
		Rules          quality.PolicyRules `json:"rules"`
		Old            quality.Profile     `json:"old_profile"`
		Candidate      quality.Profile     `json:"candidate_profile"`
		Title          string              `json:"candidate_title"`
	}
	if json.Unmarshal(b.raw, &fields) != nil {
		return nil, preparationConflict()
	}
	d := session.descriptor
	video, e := media.ValidateMovieUpgradeTorrentFiles(d.Files, d.Target)
	if e != nil {
		return nil, preparationConflict()
	}
	decision := quality.EvaluateUpgradeForMode(fields.Rules, fields.Old, fields.Candidate, mode)
	if !quality.KnownProfile(fields.Old) || !quality.KnownProfile(fields.Candidate) || !decision.ShouldUpgrade || !quality.EvaluateCandidate(fields.Rules, fields.Title, fields.Candidate).Passed || d.Target.ParserVersion != media.EpisodeParserVersion || video.Size <= 0 {
		return nil, preparationConflict()
	}
	b.review.OperationID, b.review.ObservationID, b.review.SubscriptionID = fields.OperationID, fields.ObservationID, fields.SubscriptionID
	b.review.TaskID, b.review.RootID, b.review.UnitID = fields.TaskID, fields.RootID, fields.UnitID
	b.review.CurrentFileID, b.review.CurrentFactID = fields.OldFileID, fields.OldFactID
	b.review.SourceRelativePath, b.review.SourceSize = video.Path, video.Size
	b.review.CurrentScore, b.review.NewScore = decision.CurrentScore, decision.NewScore
	b.verify = func() error {
		if _, _, e := s.authorizePreparation(ctx, tx, a, op); e != nil {
			return e
		}
		if e := validateOrganizationTrackingReceipt(ctx, tx, a, sub, op, obs); e != nil {
			return e
		}
		if e := verify(); e != nil {
			return e
		}
		var current json.RawMessage
		if e := tx.QueryRow(ctx, `SELECT pulse_upgrade_organization_basis($1,$2)`, op, obs).Scan(&current); e != nil {
			return e
		}
		if !preparationSameJSON(current, b.raw) {
			return preparationConflict()
		}
		return nil
	}
	return b, nil
}

// Historical observation receipts have no current physical lease. Validate the
// immutable result and its complete owned success ledger, not a caller's flag.
func validateOrganizationTrackingReceipt(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op, obs uuid.UUID) error {
	var out ports.MovieUpgradeTrackingResult
	var raw json.RawMessage
	var key string
	e := tx.QueryRow(ctx, `SELECT o.task_id,r.request_key,r.outcome,r.reason,r.disposition,to_jsonb(r) FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE o.operation_id=$1 AND o.observation_id=$2`, op, obs).Scan(&out.TaskID, &key, &out.Outcome, &out.Reason, &out.Disposition, &raw)
	if e != nil {
		return e
	}
	if out.Outcome != "completed" || out.Disposition != "organization_review_required" {
		return preparationConflict()
	}
	out.OperationID, out.ObservationID = op, obs
	body, e := json.Marshal(out)
	if e != nil {
		return e
	}
	return validateOrganizationLedger(ctx, tx, a, sub, "upgrade.tracking-result:"+key, coverageDigest(json.RawMessage(raw)), "upgrade.tracking-result", obs, body)
}
func validateOrganizationLedger(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub uuid.UUID, key, hash, action string, id uuid.UUID, body json.RawMessage) error {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		var ids []uuid.UUID
		predicate := "action=$1 AND payload->>'authorization_id'=$2"
		if action == "upgrade.tracking-result" {
			predicate = "action=$1 AND payload->>'observation_id'=$2"
		}
		if action == organizationPrepareAction {
			predicate = "action=$1 AND payload->>'preparation_id'=$2"
		}
		args := []any{action, id.String()}
		if table == "outbox_event" {
			predicate = "event_type=$1 AND aggregate_id=$2"
		}
		if table == "idempotency_record" {
			predicate = "idempotency_key=$1 AND resource_id=$2"
			args[0] = key
		}
		rows, e := tx.Query(ctx, "SELECT id FROM "+table+" WHERE "+predicate+" FOR SHARE", args...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var got uuid.UUID
			if e = rows.Scan(&got); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, got)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) != 1 {
			return preparationConflict()
		}
		if e = upgradeValidateSuccess(ctx, tx, table, ids[0], a, sub, key, hash, action, id, body); e != nil {
			return e
		}
	}
	return nil
}
