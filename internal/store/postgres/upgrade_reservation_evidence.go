package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

// This raw input vector deliberately includes complete protected rows. Only the
// two exact, verified owned INSERT effects on facts_version are normalized.
// It is hashed in memory; URLs, encrypted references and configuration never
// enter authority JSON, events, receipts or logs.
const upgradeEvidenceSQL = `
SELECT jsonb_build_object(
 'actor',(SELECT to_jsonb(x)-'password_hash' FROM app_user x WHERE id=$1),
 'subscription',(SELECT to_jsonb(x) FROM subscription x WHERE id=$2),
 'policy',(SELECT to_jsonb(x) FROM policy x WHERE id=$3),
 'revision',(SELECT to_jsonb(x) FROM policy_revision x WHERE policy_id=$3 AND revision=$4),
 'root',(SELECT to_jsonb(x) FROM library_root x WHERE id=$5),
 'media',(SELECT to_jsonb(x) FROM media x WHERE id=$6),
 'units',(SELECT jsonb_agg(jsonb_set(to_jsonb(x),'{facts_version}',to_jsonb(x.facts_version-CASE WHEN x.id=$7 THEN $10::bigint ELSE 0 END)) ORDER BY x.id) FROM media_unit x WHERE media_id=$6),
 'identities',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM media_identity x WHERE media_id=$6),
 'files',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM library_file x WHERE id IN (SELECT file_id FROM library_file_unit WHERE unit_id=$7) OR id=$8),
 'mappings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM library_file_unit x WHERE unit_id=$7 OR file_id=$8),
 'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE unit_id=$7 OR source_file_id=$8),
 'wanted',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM wanted_item x WHERE subscription_id=$2),
 'blocks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM release_block x WHERE subscription_id=$2),
 'candidate',(SELECT to_jsonb(x) FROM release_candidate x WHERE id=$9),
 'indexer',(SELECT to_jsonb(x) FROM indexer x WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=$9)),
 'queries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM indexer_query x WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=$9)),
 'query_candidates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM indexer_query_candidate x WHERE candidate_id=$9),
 'client',(SELECT to_jsonb(x) FROM download_client x WHERE id=(SELECT downloader_id FROM subscription WHERE id=$2)),
 'lanes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE unit_id=$7 AND operation_id<>$11),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE unit_id=$7 AND operation_id<>$11),
 'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE unit_id=$7 AND operation_id<>$11)
),clock_timestamp(),
LEAST(
 (SELECT max(q.finished_at)+interval '30 minutes' FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id JOIN release_candidate c ON c.id=qc.candidate_id AND c.indexer_id=q.indexer_id AND c.source_config_version=q.config_version WHERE c.id=$9 AND q.state='succeeded'),
 (SELECT min(expires_at) FROM unit_fact WHERE unit_id=$7 AND source_file_id=$8 AND stage='imported' AND revoked_at IS NULL),
 (SELECT min(projection_valid_until) FROM wanted_item WHERE subscription_id=$2)
)`

type upgradeFrozenSnapshot struct {
	FrozenIdentities json.RawMessage              `json:"frozen_identities"`
	Kind             string                       `json:"kind"`
	Goal             string                       `json:"goal"`
	Confirmed        bool                         `json:"confirmed"`
	Mode             ports.UpgradeMode            `json:"mode"`
	Observation      ports.UpgradePreviewVersion  `json:"observation"`
	CurrentFileID    uuid.UUID                    `json:"current_file_id"`
	CurrentFactID    uuid.UUID                    `json:"current_fact_id"`
	CandidateID      uuid.UUID                    `json:"candidate_id"`
	SourceID         uuid.UUID                    `json:"source_id"`
	ClientID         uuid.UUID                    `json:"client_id"`
	ClientDigest     string                       `json:"client_digest"`
	ProtectedDigest  string                       `json:"protected_digest"`
	SourceStamp      string                       `json:"source_stamp"`
	ParserVersion    string                       `json:"parser_version"`
	EvaluatorVersion string                       `json:"evaluator_version"`
	RootGeneration   int64                        `json:"root_generation"`
	UnitVersion      int64                        `json:"unit_version"`
	Target           ports.UpgradePreviewSnapshot `json:"target"`
	ObservedAt       time.Time                    `json:"observed_at"`
	ValidUntil       time.Time                    `json:"valid_until"`
}

func upgradeEvidence(ctx context.Context, tx pgx.Tx, a *ports.Actor, v *ports.UpgradePreviewSnapshot, op uuid.UUID, bumps int) (string, time.Time, time.Time, error) {
	var raw json.RawMessage
	var now time.Time
	var until *time.Time
	x := v.Version
	e := tx.QueryRow(ctx, upgradeEvidenceSQL, uuid.MustParse(a.ID), x.SubscriptionID, x.PolicyID, x.PolicyRevision, x.RootID, x.MediaID, x.UnitID, v.CurrentFileID, v.CandidateID, bumps, op).Scan(&raw, &now, &until)
	if e != nil {
		return "", now, time.Time{}, e
	}
	if until == nil || !until.After(now) {
		return "", now, time.Time{}, ports.ErrUpgradeReservationConflict
	}
	return completeTVJSONDigest(raw), now, *until, nil
}

// No new transaction: all lock upgrades happen before the shared preview reader.
func lockUpgradeCommand(ctx context.Context, tx pgx.Tx, a *ports.Actor, id uuid.UUID) (*db.Queries, db.Subscription, error) {
	q := db.New(tx)
	if e := coverageAccess(ctx, q, a, id); e != nil {
		return nil, db.Subscription{}, e
	}
	hint, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return nil, hint, e
	}
	if _, e = q.LockSearchPolicy(ctx, hint.QualityPolicyID); e != nil {
		return nil, hint, e
	}
	sub, e := q.LockSearchSubscription(ctx, id)
	if e != nil {
		return nil, sub, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, sub, ports.ErrForbidden
	}
	if sub.QualityPolicyID != hint.QualityPolicyID || sub.MediaID != hint.MediaID || sub.RootID != hint.RootID {
		return nil, sub, ports.ErrUpgradeReservationConflict
	}
	return q, sub, nil
}
