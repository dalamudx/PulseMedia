package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ ports.AutomaticTVSubmissionRecoveryRepository = (*AutomaticTVSubmissionStore)(nil)

// Recovery never infers physical completion from a lease or a backend PID.
func (s *AutomaticTVSubmissionStore) RecoverAutomaticTVSubmissions(ctx context.Context, limit int32) (n int, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrRejected) {
			err = automaticTVSubmissionError(ctx, err, false)
		}
	}()
	if limit < 1 || limit > 100 {
		return 0, ports.ErrRejected
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return 0, ports.ErrUnavailable
	}
	rows, e := s.pool.Query(ctx, `SELECT j.id,a.operation_id,a.owner_id FROM job j
 JOIN automatic_tv_execution_authority a ON a.operation_id::text=j.payload->>'operation_id'
 WHERE j.kind='automatic_tv_download_submission' AND j.state='running'
 AND j.lease_until<=clock_timestamp() ORDER BY j.lease_until,j.id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type candidate struct{ job, operation, owner uuid.UUID }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.job, &c.operation, &c.owner); e != nil {
			rows.Close()
			return 0, e
		}
		candidates = append(candidates, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, c := range candidates {
		changed, e := s.recoverAutomaticTVSubmission(ctx, c.job, c.operation, c.owner)
		if e != nil {
			return n, e
		}
		if changed {
			n++
		}
	}
	return n, nil
}

func (s *AutomaticTVSubmissionStore) recoverAutomaticTVSubmission(ctx context.Context, jobID, operation, ownerID uuid.UUID) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	// Use the non-authorizing owner lock: disabled owners must reach review, too.
	owner, e := q.LockAcquisitionCommandUser(ctx, ownerID)
	if e != nil {
		return false, e
	}
	if _, e = q.LockJobForWrite(ctx, jobID); e != nil {
		return false, e
	}
	job, e := q.GetJobByID(ctx, jobID)
	if e != nil {
		return false, e
	}
	a, e := automaticTVSubmissionAuthority(ctx, q, operation)
	if e != nil {
		return false, e
	}
	payload, _ := json.Marshal(automaticTVSubmissionPayload{OperationID: a.OperationID, RoundID: a.RoundID, Authority: a.OperationID, CandidateID: a.CandidateID, RootID: a.Target.RootID, ClientID: a.Target.ClientID})
	if a.Target.OwnerID != ownerID || job.Kind != "automatic_tv_download_submission" || !job.DedupeKey.Valid || job.DedupeKey.String != operation.String() || completeTVJSONDigest(job.Payload) != completeTVJSONDigest(payload) {
		return false, ports.ErrAutomaticTVSubmissionReview
	}
	var expired, active, prepared, task, finished bool
	e = tx.QueryRow(ctx, `SELECT j.state='running' AND j.lease_owner IS NOT NULL AND j.lease_token>0 AND j.lease_until<=clock_timestamp(),
 EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=$2 AND finished_at IS NULL),
 EXISTS(SELECT 1 FROM automatic_tv_submission_preparation WHERE operation_id=$2),
 EXISTS(SELECT 1 FROM download_task WHERE operation_id=$2),
 EXISTS(SELECT 1 FROM automatic_tv_physical_attempt x WHERE x.operation_id=$2 AND x.job_id=j.id AND x.finished_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_submission_preparation p WHERE p.operation_id=$2 AND (p.physical_attempt_id<>x.id OR p.job_id<>j.id OR p.lease_owner<>x.lease_owner OR p.lease_token<>x.lease_token)))
 FROM job j WHERE j.id=$1`, jobID, operation).Scan(&expired, &active, &prepared, &task, &finished)
	if e != nil {
		return false, e
	}
	if !expired || active {
		return false, nil
	}
	reason := ""
	if prepared || task {
		// Without original completion evidence, even observation takeover must wait.
		if !finished {
			return false, nil
		}
		reason = "awaiting_observation"
	} else {
		if !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) {
			reason = "owner_revoked"
		} else if job.Attempts >= job.MaxAttempts {
			reason = "retry_exhausted"
		} else if e = s.automaticTVSubmissionRecoveryTarget(ctx, tx, q, a); e != nil {
			if !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) && !errors.Is(e, pgx.ErrNoRows) {
				return false, e
			}
			reason = "target_changed"
		}
		var valid bool
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1::timestamptz`, a.SendValidUntil).Scan(&valid); e != nil {
			return false, e
		}
		if reason == "" && !valid {
			reason = "first_send_expired"
		}
	}
	state := "dead"
	if reason == "" {
		state = "queued"
		reason = "r0_requeued"
	}
	raw, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{reason})
	const event = "automatic_tv.submission_recovered"
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: a.RoundID.String(), Action: event, TargetType: "job", TargetID: jobID.String(), Payload: raw}); e != nil {
		return false, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "automatic_tv_submission", AggregateID: operation.String()}); e != nil {
		return false, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "automatic_tv_submission", AggregateID: operation.String()})
	if e != nil {
		return false, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "automatic_tv_submission", AggregateID: operation.String(), Seq: int64(seq), EventType: event, Payload: raw}); e != nil {
		return false, e
	}
	// Tail predicate runs after every lock and audit/event trigger. Attempts belong to Claim.
	var token int64
	e = tx.QueryRow(ctx, `UPDATE job SET state=$4,lease_owner=NULL,lease_until=NULL,lease_token=lease_token+1,
 not_before=clock_timestamp(),updated_at=clock_timestamp(),result=$5::jsonb,
 finished_at=CASE WHEN $4='dead' THEN clock_timestamp() ELSE NULL END
 WHERE id=$1 AND kind='automatic_tv_download_submission' AND state='running' AND lease_owner=$2 AND lease_token=$3
 AND lease_until<=clock_timestamp() AND payload=$6::jsonb AND dedupe_key=$7
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=$8 AND finished_at IS NULL)
 AND ($4='dead' OR (attempts<max_attempts AND clock_timestamp()<$9::timestamptz
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_submission_preparation WHERE operation_id=$8)
 AND NOT EXISTS(SELECT 1 FROM download_task WHERE operation_id=$8))) RETURNING lease_token`, jobID, job.LeaseOwner.String, job.LeaseToken, state, raw, payload, operation.String(), operation, a.SendValidUntil).Scan(&token)
	if e != nil {
		return false, e
	}
	var fenced bool
	e = tx.QueryRow(ctx, `SELECT state=$2 AND lease_token=$3 AND lease_owner IS NULL AND lease_until IS NULL
 AND kind='automatic_tv_download_submission' AND payload=$4::jsonb AND dedupe_key=$5
 AND attempts=$6 AND result=$7::jsonb AND ($2='dead' OR (attempts<max_attempts AND clock_timestamp()<$8::timestamptz))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt WHERE operation_id=$9 AND finished_at IS NULL)
 FROM job WHERE id=$1`, jobID, state, token, payload, operation.String(), job.Attempts, raw, a.SendValidUntil, operation).Scan(&fenced)
	if e != nil {
		return false, e
	}
	if !fenced || token != job.LeaseToken+1 {
		return false, ports.ErrAutomaticTVSubmissionReview
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return true, nil
}

// This reads current eligibility without requiring a live first-send lease or
// granting a new authority. Prepare still owns the complete send-time checks.
func (s *AutomaticTVSubmissionStore) automaticTVSubmissionRecoveryTarget(ctx context.Context, tx pgx.Tx, q *db.Queries, a ports.AutomaticTVExecutionAuthority) error {
	t := a.Target
	fail := ports.ErrAutomaticTVSubmissionReview
	policy, e := q.LockSearchPolicy(ctx, t.PolicyID)
	if e != nil {
		return e
	}
	sub, e := q.LockSearchSubscription(ctx, t.SubscriptionID)
	if e != nil {
		return e
	}
	if sub.UserID != t.OwnerID || sub.State != "active" || sub.IntentVersion != t.IntentVersion || sub.MediaID != t.MediaID || sub.RootID != t.RootID || sub.QualityPolicyID != t.PolicyID || sub.QualityPolicyRevision != t.PolicyRevision || policy.CurrentRevision != t.PolicyRevision || sub.UpgradeMode != "disabled" || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != t.ClientID || sub.EpisodeMappingRevision.Int64 != t.MappingRevision || sub.EpisodeMappingRevision.Valid != (t.MappingRevision > 0) {
		return fail
	}
	rev, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: t.PolicyID, Revision: t.PolicyRevision})
	if e != nil {
		return e
	}
	if completeTVJSONDigest(rev.Rules) != completeTVJSONDigest(t.Rules) {
		return fail
	}
	root, e := q.LockAcquisitionRoot(ctx, t.RootID)
	if e != nil {
		return e
	}
	if root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted || root.Generation != t.RootGeneration || root.HealthRevision != t.RootHealthRevision || automaticTVDigest([]byte(root.Path)) != a.RootPathDigest {
		return fail
	}
	unit, e := q.LockAcquisitionUnit(ctx, t.UnitID)
	if e != nil {
		return e
	}
	wanted, e := q.LockSearchWanted(ctx, t.WantedID)
	if e != nil {
		return e
	}
	if unit.MediaID != t.MediaID || unit.MetadataVersion != t.UnitVersion || unit.UnitKey != t.UnitKey || unit.Retired || !unit.SeasonNumber.Valid || unit.SeasonNumber.Int32 != t.Season || !unit.EpisodeNumber.Valid || unit.EpisodeNumber.Int32 != t.Episode || unit.AbsoluteNumber.Valid || !unit.AirDate.Valid || !unit.AirDate.Time.Equal(t.AirDate) || wanted.SubscriptionID != sub.ID || wanted.UnitID != unit.ID || wanted.IsSkipped || wanted.UserSkipped || wanted.ScopeExcluded || wanted.IntentVersion != t.IntentVersion {
		return fail
	}
	if e = q.LockDownloadAcquisition(ctx, a.OperationID); e != nil {
		return e
	}
	m, e := q.LockSearchMedia(ctx, t.MediaID)
	if e != nil {
		return e
	}
	if m.MediaType != "tv" || m.MetadataVersion != t.MediaVersion || m.Title != t.Title || m.OriginalTitle.String != t.OriginalTitle || !m.Year.Valid || m.Year.Int32 != t.Year || t.MappingMode != "standard" {
		return fail
	}
	identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return e
	}
	matched := false
	for _, identity := range identities {
		if identity.Namespace == "tmdb.tv" && identity.ExternalID == strconv.FormatInt(t.TMDBID, 10) {
			matched = true
		}
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil {
		return e
	}
	if !matched || episode.ExternalID != t.EpisodeID || episode.TmdbSeriesID != t.TMDBID {
		return fail
	}
	if t.MappingRevision > 0 {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil {
			return e
		}
		if mapping.Revision != t.MappingRevision || mapping.Mode != "standard" || mapping.MediaID != m.ID || mapping.MediaVersion != m.MetadataVersion {
			return fail
		}
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil || !slices.Contains(bound, a.IndexerID) || t.IndexerVersions[a.IndexerID.String()] != a.SourceConfigVersion {
		return fail
	}
	if _, e = q.LockSearchIndexer(ctx, a.IndexerID); e != nil {
		return e
	}
	current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: a.IndexerID, ConfigVersion: a.SourceConfigVersion})
	if e != nil {
		return e
	}
	if !current {
		return fail
	}
	candidate, e := q.LockSubmissionCandidate(ctx, a.CandidateID)
	if e != nil {
		return e
	}
	var same bool
	e = tx.QueryRow(ctx, `SELECT f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes AND f.source_config_version=c.source_config_version FROM automatic_tv_query_candidate f JOIN release_candidate c ON c.id=f.candidate_id WHERE f.query_id=$1 AND f.candidate_id=$2`, a.QueryID, a.CandidateID).Scan(&same)
	if e != nil {
		return e
	}
	row := ports.AutomaticTVRoundCandidate{Candidate: automaticTVSearchCandidate(candidate), QueryID: a.QueryID, QueryParametersHash: a.QueryParametersHash, QuerySourceStamp: a.SourceStamp}
	if _, ok := automaticTVEligible(t, row); !same || !ok || candidate.IndexerID != a.IndexerID {
		return fail
	}
	if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
		return e
	}
	if _, e = q.GetSearchDownloaderSnapshot(ctx, t.ClientID); e != nil {
		return e
	}
	client, e := q.GetDownloadClientByID(ctx, t.ClientID)
	if e != nil {
		return e
	}
	if episodeClientStamp(client) != t.ClientStamp {
		return fail
	}
	return automaticTVSubmissionProtections(ctx, tx, q, a, uuid.Nil)
}
