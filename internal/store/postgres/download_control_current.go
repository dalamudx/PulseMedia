package postgres

import (
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func controlMovieCurrent(ctx context.Context, tx pgx.Tx, task db.DownloadTask, descriptor ports.MovieUpgradeTrackingDescriptor) error {
	var raw []byte
	if e := tx.QueryRow(ctx, `SELECT snapshot FROM movie_upgrade_authority WHERE operation_id=$1 FOR SHARE`, task.OperationID).Scan(&raw); e != nil {
		return e
	}
	var frozen upgradeFrozenSnapshot
	if json.Unmarshal(raw, &frozen) != nil || frozen.Kind != "frozen_movie_upgrade_v1" || frozen.Goal != "upgrade" || !frozen.Confirmed {
		return ports.ErrDownloadControlConflict
	}
	v := frozen.Observation
	if e := controlCurrentTarget(ctx, tx, v.SubscriptionID, v.ActorID, v.MediaID, v.RootID, frozen.ClientID, v.IntentVersion, frozen.RootGeneration, frozen.Target.Candidate.MediaVersion, v.PolicyID, v.PolicyRevision); e != nil {
		return e
	}
	q := db.New(tx)
	sub, e := q.GetSubscriptionByID(ctx, v.SubscriptionID)
	if e != nil {
		return e
	}
	if string(sub.UpgradeMode) != string(frozen.Mode) {
		return ports.ErrDownloadControlConflict
	}
	target, e := preparationMovieTarget(ctx, q, frozen)
	if e != nil {
		return e
	}
	if coverageDigest(target) != coverageDigest(descriptor.Target) {
		return ports.ErrDownloadControlConflict
	}
	idx, e := q.LockSearchIndexer(ctx, frozen.SourceID)
	if e != nil {
		return e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, frozen.CandidateID)
	if e != nil {
		return e
	}
	c := frozen.Target.Candidate
	if candidate.IndexerID != idx.ID || candidate.Title != c.Title || candidate.SizeBytes != c.SizeBytes || candidate.SourceConfigVersion != c.SourceVersion || idx.ConfigVersion != c.SourceVersion || completeTVJSONDigest(candidate.QualityProfile) != completeTVJSONDigest([]byte(c.RawProfile)) {
		return ports.ErrDownloadControlConflict
	}
	var oldValid bool
	e = tx.QueryRow(ctx, `SELECT f.retired_at IS NULL AND f.root_id=$3 AND u.unit_id=$4 AND u.retired_at IS NULL AND fact.id=$2 AND fact.unit_id=$4 AND fact.stage='imported' AND fact.source_file_id=f.id AND fact.revoked_at IS NULL AND (fact.expires_at IS NULL OR fact.expires_at>clock_timestamp()) AND (SELECT count(*) FROM library_file_unit m WHERE m.file_id=f.id AND m.retired_at IS NULL)=1 FROM library_file f JOIN library_file_unit u ON u.file_id=f.id JOIN unit_fact fact ON fact.source_file_id=f.id AND fact.id=$2 WHERE f.id=$1`, frozen.CurrentFileID, frozen.CurrentFactID, v.RootID, v.UnitID).Scan(&oldValid)
	if e != nil {
		return e
	}
	if !oldValid {
		return ports.ErrDownloadControlConflict
	}
	for _, table := range []string{"acquisition_reservation", "unit_reservation", "download_requirement"} {
		var id uuid.UUID
		if e = tx.QueryRow(ctx, "SELECT id FROM "+table+" WHERE operation_id=$1 FOR UPDATE", task.OperationID).Scan(&id); e != nil {
			return e
		}
		if e = upgradeValidateOwned(ctx, tx, table, id); e != nil {
			return e
		}
	}
	return nil
}

// Hash full current rows, including membership, rather than just the versions
// needed for eligibility. Only this digest enters immutable command history.
func controlContextDigest(ctx context.Context, tx pgx.Tx, task db.DownloadTask, scope string) (string, error) {
	table, owner := "automatic_tv_execution_authority", "owner_id"
	switch scope {
	case "complete-tv":
		table = "complete_tv_execution_authority"
		owner = "actor_id"
	case "tv-episode-upgrade":
		var raw []byte
		if e := tx.QueryRow(ctx, `SELECT snapshot FROM tv_episode_upgrade_authority WHERE operation_id=$1`, task.OperationID).Scan(&raw); e != nil {
			return "", e
		}
		return completeTVJSONDigest(raw), nil
	case "movie-upgrade":
		table = "movie_upgrade_authority"
		owner = "actor_id"
	}
	var subID, ownerID, rootID, candidateID uuid.UUID
	e := tx.QueryRow(ctx, "SELECT subscription_id,"+owner+",root_id,candidate_id FROM "+table+" WHERE operation_id=$1", task.OperationID).Scan(&subID, &ownerID, &rootID, &candidateID)
	if e != nil {
		return "", e
	}
	q := db.New(tx)
	hint, e := q.GetCandidateByID(ctx, candidateID)
	if e != nil {
		return "", e
	}
	if _, e = q.LockSearchIndexer(ctx, hint.IndexerID); e != nil {
		return "", e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, candidateID)
	if e != nil {
		return "", e
	}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'owner',(SELECT to_jsonb(u)-'password_hash' FROM app_user u WHERE id=$2),
 'subscription',to_jsonb(s),'policy',(SELECT to_jsonb(p) FROM policy p WHERE id=s.quality_policy_id),
 'rules',(SELECT to_jsonb(p) FROM policy_revision p WHERE policy_id=s.quality_policy_id AND revision=s.quality_policy_revision),
 'root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$3),
 'media',(SELECT to_jsonb(m) FROM media m WHERE id=s.media_id),
 'units',(SELECT jsonb_agg(to_jsonb(u) ORDER BY u.id) FROM media_unit u WHERE u.media_id=s.media_id),
 'identities',(SELECT jsonb_agg(to_jsonb(i) ORDER BY to_jsonb(i)::text) FROM media_identity i WHERE i.media_id=s.media_id),
 'episodes',(SELECT jsonb_agg(to_jsonb(i) ORDER BY to_jsonb(i)::text) FROM media_unit_identity i JOIN media_unit u ON u.id=i.unit_id WHERE u.media_id=s.media_id),
 'mapping',(SELECT to_jsonb(r) FROM episode_mapping_revision r WHERE r.subscription_id=s.id AND r.revision=s.episode_mapping_revision),
 'wanted',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM wanted_item w WHERE w.subscription_id=s.id),
 'candidate',(SELECT to_jsonb(c) FROM release_candidate c WHERE c.id=$4),
 'source',(SELECT to_jsonb(i) FROM indexer i WHERE i.id=$5),
 'queries',(SELECT jsonb_agg(to_jsonb(q) ORDER BY q.id) FROM indexer_query q WHERE q.id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=$4)),
 'query_candidates',(SELECT jsonb_agg(to_jsonb(q) ORDER BY to_jsonb(q)::text) FROM indexer_query_candidate q WHERE q.candidate_id=$4)
 ) FROM subscription s WHERE s.id=$1`, subID, ownerID, rootID, candidateID, candidate.IndexerID).Scan(&raw)
	if e != nil {
		return "", e
	}
	return completeTVJSONDigest(raw), nil
}
