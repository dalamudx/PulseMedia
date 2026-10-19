package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func automaticTVObservationText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

type AutomaticTVObservationStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewAutomaticTVObservationStore(pool *pgxpool.Pool, ids ports.IDGen) *AutomaticTVObservationStore {
	return &AutomaticTVObservationStore{pool: pool, ids: ids}
}

var _ ports.AutomaticTVObservationRepository = (*AutomaticTVObservationStore)(nil)

type automaticTVObservationPayload struct {
	OperationID uuid.UUID `json:"operation_id"`
	TaskID      uuid.UUID `json:"task_id"`
	Authority   uuid.UUID `json:"automatic_tv_authority"`
}

func automaticTVObservationPayloadBytes(r ports.AutomaticTVObservationRequest) []byte {
	return mustAutomaticTVJSON(automaticTVObservationPayload{r.OperationID, r.TaskID, r.OperationID})
}
func automaticTVObservationError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		return ports.ErrAutomaticTVNeedsObservation
	}
	if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, ports.ErrAutomaticTVObservationStale) || errors.Is(e, ports.ErrAutomaticTVSubmissionReview) || errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		return ports.ErrAutomaticTVObservationStale
	}
	return ports.ErrUnavailable
}
func (s *AutomaticTVObservationStore) ready(ctx context.Context) error {
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	return RequireRestoreReconciled(ctx, s.pool)
}
func automaticTVObservationJob(ctx context.Context, tx pgx.Tx, r ports.AutomaticTVObservationRequest, finished bool, until time.Time) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT kind='automatic_tv_observation' AND dedupe_key=$2::uuid::text
 AND payload=$3::jsonb AND lease_token=$5 AND
 CASE WHEN $6 THEN state='succeeded' AND lease_owner IS NULL AND lease_until IS NULL AND $7::timestamptz>clock_timestamp()
 ELSE state='running' AND lease_owner=$4 AND lease_until>clock_timestamp() END
 FROM job WHERE id=$1`, r.Lease.ID, r.OperationID, automaticTVObservationPayloadBytes(r), r.Lease.Owner, r.Lease.Token, finished, until).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ports.ErrAutomaticTVObservationStale
	}
	return nil
}
func automaticTVObservationPhysical(ctx context.Context, tx pgx.Tx, r ports.AutomaticTVObservationRequest, send uuid.UUID) (string, error) {
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + r.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	var ok bool
	var identity string
	e := tx.QueryRow(ctx, `SELECT p.operation_id=$2 AND p.task_id=$3 AND p.job_id=$4 AND p.lease_owner=$5 AND p.lease_token=$6
 AND p.send_attempt_id=$7 AND p.finished_at IS NULL AND p.advisory_key=$8
 AND x.finished_at IS NOT NULL AND x.operation_id=p.operation_id
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=p.operation_id AND active.finished_at IS NULL)
 AND EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=p.backend_pid AND granted AND mode='ExclusiveLock' AND objsubid=1
 AND classid=($8::bigint>>32 & 4294967295)::oid AND objid=($8::bigint & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database())),
 p.id::text||':'||p.backend_pid::text||':'||p.advisory_key::text
 FROM automatic_tv_observation_permit p JOIN automatic_tv_physical_attempt x ON x.id=p.send_attempt_id WHERE p.id=$1 FOR UPDATE OF p`, r.PhysicalPermitID, r.OperationID, r.TaskID, r.Lease.ID, r.Lease.Owner, r.Lease.Token, send, key).Scan(&ok, &identity)
	if e != nil {
		return "", e
	}
	if !ok {
		return "", ports.ErrAutomaticTVObservationStale
	}
	return identity, nil
}

// Locks follow the original sender's owner/job/policy/subscription/root/unit/
// wanted/operation/media/source/candidate/client/protections order. Current
// ineligibility is a separate boolean; it does not erase original physical facts.
func automaticTVObservationCurrent(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVObservationRequest, a ports.AutomaticTVExecutionAuthority) (bool, string, error) {
	t := a.Target
	owner, e := q.LockAcquisitionCommandUser(ctx, t.OwnerID)
	if e != nil {
		return false, "", e
	}
	if _, e = q.LockJobForWrite(ctx, r.Lease.ID); e != nil {
		return false, "", e
	}
	if e = automaticTVObservationJob(ctx, tx, r, false, time.Time{}); e != nil {
		return false, "", e
	}
	policy, e := q.LockSearchPolicy(ctx, t.PolicyID)
	if e != nil {
		return false, "", e
	}
	sub, e := q.LockSearchSubscription(ctx, t.SubscriptionID)
	if e != nil {
		return false, "", e
	}
	root, e := q.LockAcquisitionRoot(ctx, t.RootID)
	if e != nil {
		return false, "", e
	}
	unit, e := q.LockAcquisitionUnit(ctx, t.UnitID)
	if e != nil {
		return false, "", e
	}
	wanted, e := q.LockSearchWanted(ctx, t.WantedID)
	if e != nil {
		return false, "", e
	}
	if e = q.LockDownloadAcquisition(ctx, a.OperationID); e != nil {
		return false, "", e
	}
	m, e := q.LockSearchMedia(ctx, t.MediaID)
	if e != nil {
		return false, "", e
	}
	idx, e := q.LockSearchIndexer(ctx, a.IndexerID)
	if e != nil {
		return false, "", e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, a.CandidateID)
	if e != nil {
		return false, "", e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM download_client WHERE id=$1 FOR UPDATE`, t.ClientID); e != nil {
		return false, "", e
	}
	client, e := q.GetDownloadClientByID(ctx, t.ClientID)
	if e != nil {
		return false, "", e
	}
	stamp := episodeClientStamp(client)
	eligible := owner.IsActive && (owner.Role == ports.RoleMember || owner.Role == ports.RoleAdmin) &&
		sub.UserID == t.OwnerID && sub.State == "active" && sub.IntentVersion == t.IntentVersion && sub.MediaID == t.MediaID && sub.RootID == t.RootID &&
		sub.QualityPolicyID == t.PolicyID && sub.QualityPolicyRevision == t.PolicyRevision && policy.CurrentRevision == t.PolicyRevision && sub.UpgradeMode == "disabled" &&
		sub.DownloaderID.Valid && uuid.UUID(sub.DownloaderID.Bytes) == t.ClientID && sub.EpisodeMappingRevision.Int64 == t.MappingRevision && sub.EpisodeMappingRevision.Valid == (t.MappingRevision > 0) &&
		root.MediaType == "tv" && root.Health == "online" && root.BaselineCompleted && root.Generation == t.RootGeneration && root.HealthRevision == t.RootHealthRevision && automaticTVDigest([]byte(root.Path)) == a.RootPathDigest &&
		unit.MediaID == t.MediaID && unit.MetadataVersion == t.UnitVersion && unit.UnitKey == t.UnitKey && !unit.Retired && unit.SeasonNumber.Valid && unit.SeasonNumber.Int32 == t.Season && unit.EpisodeNumber.Valid && unit.EpisodeNumber.Int32 == t.Episode && !unit.AbsoluteNumber.Valid && unit.AirDate.Valid && unit.AirDate.Time.Equal(t.AirDate) &&
		wanted.SubscriptionID == sub.ID && wanted.UnitID == unit.ID && !wanted.IsSkipped && !wanted.UserSkipped && !wanted.ScopeExcluded && wanted.IntentVersion == t.IntentVersion &&
		m.MediaType == "tv" && m.MetadataVersion == t.MediaVersion && m.Title == t.Title && m.OriginalTitle.String == t.OriginalTitle && m.Year.Valid && m.Year.Int32 == t.Year && t.MappingMode == "standard" && stamp == t.ClientStamp
	rev, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: t.PolicyID, Revision: t.PolicyRevision})
	if e != nil {
		return false, "", e
	}
	eligible = eligible && completeTVJSONDigest(rev.Rules) == completeTVJSONDigest(t.Rules)
	aired, e := q.AutomaticTVAired(ctx, unit.ID)
	if e != nil {
		return false, "", e
	}
	eligible = eligible && aired.Valid && aired.Bool
	identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return false, "", e
	}
	identity := false
	for _, i := range identities {
		identity = identity || (i.Namespace == "tmdb.tv" && i.ExternalID == strconv.FormatInt(t.TMDBID, 10))
	}
	eligible = eligible && identity
	ep, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return false, "", e
	}
	eligible = eligible && e == nil && ep.ExternalID == t.EpisodeID && ep.TmdbSeriesID == t.TMDBID
	if t.MappingRevision > 0 {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return false, "", e
		}
		eligible = eligible && e == nil && mapping.Revision == t.MappingRevision && mapping.Mode == "standard" && mapping.MediaID == m.ID && mapping.MediaVersion == m.MetadataVersion
	}
	var bound []uuid.UUID
	eligible = eligible && json.Unmarshal(sub.IndexerIds, &bound) == nil && slices.Contains(bound, a.IndexerID) && t.IndexerVersions[a.IndexerID.String()] == a.SourceConfigVersion
	current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: idx.ID, ConfigVersion: a.SourceConfigVersion})
	if e != nil {
		return false, "", e
	}
	eligible = eligible && current
	row := ports.AutomaticTVRoundCandidate{Candidate: automaticTVSearchCandidate(candidate), QueryID: a.QueryID, QueryParametersHash: a.QueryParametersHash, QuerySourceStamp: a.SourceStamp}
	_, candidateOK := automaticTVEligible(t, row)
	eligible = eligible && candidateOK && candidate.IndexerID == a.IndexerID
	var same bool
	e = tx.QueryRow(ctx, `SELECT f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes AND f.source_config_version=c.source_config_version FROM automatic_tv_query_candidate f JOIN release_candidate c ON c.id=f.candidate_id WHERE f.query_id=$1 AND f.candidate_id=$2`, a.QueryID, a.CandidateID).Scan(&same)
	if e != nil {
		return false, "", e
	}
	eligible = eligible && same
	var pathmap map[string]string
	eligible = eligible && json.Unmarshal(client.RemotePathMap, &pathmap) == nil && naming.TranslateRemotePath(pathmap, root.Path) != "" && (client.ClientType == "qbittorrent" || client.ClientType == "transmission")
	var clear bool
	e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=$1 AND released_at IS NULL AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched') AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM download_requirement WHERE unit_id=$1 AND state='pending' AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM library_file_unit fu JOIN library_file f ON f.id=fu.file_id WHERE fu.unit_id=$1 AND fu.retired_at IS NULL AND f.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM library_reuse_unit WHERE unit_id=$1 AND active)`, t.UnitID, a.OperationID).Scan(&clear)
	if e != nil {
		return false, "", e
	}
	return eligible && clear, stamp, nil
}

func automaticTVObservationFacts(ctx context.Context, tx pgx.Tx, r ports.AutomaticTVObservationRequest, load bool) (*ports.AutomaticTVObservationSnapshot, string, bool, error) {
	fail := ports.ErrAutomaticTVObservationStale
	if !automaticTVObservationRequestValid(r) {
		return nil, "", false, fail
	}
	q := db.New(tx)
	a, e := automaticTVSubmissionAuthority(ctx, q, r.OperationID)
	if e != nil {
		return nil, "", false, e
	}
	eligible, stamp, e := automaticTVObservationCurrent(ctx, tx, q, r, a)
	if e != nil {
		return nil, "", false, e
	}
	if load && stamp != a.Target.ClientStamp {
		return nil, "", false, ports.ErrAutomaticTVNeedsObservation
	}
	task, e := q.LockDownloadTask(ctx, r.TaskID)
	if e != nil {
		return nil, "", false, e
	}
	if task.OperationID != a.OperationID || task.ClientID != a.Target.ClientID || task.CandidateID != a.CandidateID || (task.State != "submitting" && task.State != "unknown" && task.State != "tracking") {
		return nil, "", false, fail
	}
	p, e := q.AutomaticTVSubmissionPreparation(ctx, a.OperationID)
	if e != nil {
		return nil, "", false, e
	}
	var proof automaticTVPreparedProof
	if json.Unmarshal(p.Proof, &proof) != nil || p.TaskID != task.ID || p.AuthorityHash != a.DecisionStamp || p.InfoHash != task.InfoHash || p.ProofHash != completeTVJSONDigest(p.Proof) || proof.Kind != "automatic_tv_prepared_v1" || proof.InfoHash != task.InfoHash || proof.SavePath != task.SavePath || proof.TotalBytes <= 0 || !bytes.Equal(p.TorrentSha256, proof.SHA256[:]) || completeTVJSONDigest(mustAutomaticTVJSON(proof.Authority)) != completeTVJSONDigest(mustAutomaticTVJSON(a)) {
		return nil, "", false, fail
	}
	var total int64
	for _, f := range proof.Files {
		if f.Size < 0 || f.Size > proof.TotalBytes-total {
			return nil, "", false, fail
		}
		total += f.Size
	}
	if total != proof.TotalBytes {
		return nil, "", false, fail
	}
	video, e := media.ValidateAutomaticTVSingleEpisodeFiles(proof.Files, automaticTVObservationTarget(a))
	if e != nil || *video != proof.Video {
		return nil, "", false, fail
	}
	var original bool
	e = tx.QueryRow(ctx, `SELECT x.operation_id=$1 AND x.job_id=$2 AND x.lease_owner=$3 AND x.lease_token=$4 AND x.finished_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=$1 AND active.finished_at IS NULL)
 FROM automatic_tv_physical_attempt x WHERE x.id=$5`, a.OperationID, p.JobID, p.LeaseOwner, p.LeaseToken, p.PhysicalAttemptID).Scan(&original)
	if e != nil {
		return nil, "", false, e
	}
	if !original {
		return nil, "", false, ports.ErrAutomaticTVNeedsObservation
	}
	if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
		return nil, "", false, e
	}
	if e = automaticTVSubmissionProtections(ctx, tx, q, a, task.ID); e != nil {
		return nil, "", false, e
	}
	permit, e := automaticTVObservationPhysical(ctx, tx, r, p.PhysicalAttemptID)
	if e != nil {
		return nil, "", false, e
	}
	var linked bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_observation_job WHERE job_id=$1 AND operation_id=$2 AND task_id=$3 AND send_attempt_id=$4)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result WHERE job_id=$1)`, r.Lease.ID, a.OperationID, task.ID, p.PhysicalAttemptID).Scan(&linked)
	if e != nil {
		return nil, "", false, e
	}
	if !linked {
		return nil, "", false, fail
	}
	originalStamp := automaticTVDigest(mustAutomaticTVJSON([]any{p.ProofHash, p.JobID, p.LeaseOwner, p.LeaseToken, task.State, task.TotalBytes, task.DownloadedBytes, task.ContentPath, permit}))
	out := &ports.AutomaticTVObservationSnapshot{Request: r, Authority: a, SendPhysicalAttemptID: p.PhysicalAttemptID, ClientID: a.Target.ClientID, ClientStamp: a.Target.ClientStamp, InfoHash: proof.InfoHash, OperationTag: "pulsemedia:op:" + a.OperationID.String(), SavePath: proof.SavePath, TotalBytes: proof.TotalBytes, MetainfoSHA256: proof.SHA256, Files: slices.Clone(proof.Files), Video: proof.Video}
	out.Seal = automaticTVObservationSeal(*out, originalStamp)
	if e = automaticTVObservationJob(ctx, tx, r, false, time.Time{}); e != nil {
		return nil, "", false, e
	}
	return automaticTVObservationClone(*out), originalStamp, eligible, nil
}

func (s *AutomaticTVObservationStore) LoadAutomaticTVObservation(ctx context.Context, r ports.AutomaticTVObservationRequest) (out *ports.AutomaticTVObservationSnapshot, err error) {
	defer func() {
		err = automaticTVObservationError(ctx, err)
		if err != nil {
			out = nil
		}
	}()
	if e := s.ready(ctx); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	out, _, _, e = automaticTVObservationFacts(ctx, tx, r, true)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *AutomaticTVObservationStore) CommitAutomaticTVObservation(ctx context.Context, snapshot ports.AutomaticTVObservationSnapshot, remote ports.AutomaticTVDownloadObservation) (out *ports.AutomaticTVObservationReceipt, err error) {
	defer func() {
		err = automaticTVObservationError(ctx, err)
		if err != nil {
			out = nil
		}
	}()
	if e := s.ready(ctx); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	actual, original, eligible, e := automaticTVObservationFacts(ctx, tx, snapshot.Request, false)
	if e != nil {
		return nil, e
	}
	if snapshot.Seal == "" || snapshot.Seal != actual.Seal || automaticTVObservationSeal(snapshot, original) != actual.Seal {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	q := db.New(tx)
	job, e := q.GetJobByID(ctx, snapshot.Request.Lease.ID)
	if e != nil {
		return nil, e
	}
	known, complete, files := automaticTVObservationEvidence(*actual, remote)
	state := "unknown"
	review := true
	if known {
		state = "tracking"
		review = false
	}
	if complete {
		state = "completed"
		review = !eligible
	}
	// The canonical evidence is typed and deterministic; it includes both native
	// statements and all inventory rows, never transport material or credentials.
	statement := mustAutomaticTVJSON(struct {
		Kind          string    `json:"kind"`
		Seal          string    `json:"seal"`
		ClientID      uuid.UUID `json:"client_id"`
		ClientStamp   string    `json:"client_stamp"`
		Before, After []ports.AutomaticTVRemoteTask
		Files         []ports.AutomaticTVRemoteFile
		Complete      bool `json:"complete"`
		Review        bool `json:"review_required"`
	}{"automatic_tv_observation_v1", actual.Seal, remote.ClientID, remote.ClientStamp, remote.Before, remote.After, files, complete, review})
	evidence := automaticTVDigest(statement)
	prepared, e := q.AutomaticTVSubmissionPreparation(ctx, actual.Request.OperationID)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO automatic_tv_observation_result(job_id,operation_id,task_id,permit_id,send_attempt_id,lease_owner,lease_token,authority_hash,proof_hash,state,complete,review_required,evidence_stamp,statement,inventory,next_poll_at,created_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,CASE WHEN $10='tracking' AND NOT $12 THEN stamp.now+interval '2 seconds' END,stamp.now FROM (SELECT clock_timestamp() AS now) stamp`, actual.Request.Lease.ID, actual.Request.OperationID, actual.Request.TaskID, actual.Request.PhysicalPermitID, actual.SendPhysicalAttemptID, actual.Request.Lease.Owner, actual.Request.Lease.Token, actual.Authority.DecisionStamp, prepared.ProofHash, state, complete, review, evidence, string(statement), mustAutomaticTVJSON(files))
	if e != nil {
		return nil, e
	}
	content := ""
	downloaded := int64(0)
	if known {
		content = remote.After[0].ContentPath
		downloaded = remote.After[0].VerifiedBytes
	}
	organization := "none"
	if complete {
		organization = "review_required"
		if eligible {
			organization = "queued"
		}
	}
	tag, e := tx.Exec(ctx, `UPDATE download_task SET state=$2,total_bytes=$3,downloaded_bytes=$4,content_path=NULLIF($5,''),progress=CASE WHEN $6 THEN 1 ELSE progress END,
 organization_state=CASE WHEN $6 THEN $7 ELSE organization_state END,error=CASE WHEN $8 THEN 'automatic_tv_observation_review' ELSE NULL END,
 finished_at=CASE WHEN $6 THEN clock_timestamp() ELSE finished_at END,updated_at=clock_timestamp()
 WHERE id=$1 AND state IN ('submitting','unknown','tracking')`, actual.Request.TaskID, state, actual.TotalBytes, downloaded, content, complete, organization, review)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if complete && eligible {
		payload := mustAutomaticTVJSON(map[string]any{"operation_id": actual.Request.OperationID, "task_id": actual.Request.TaskID, "automatic_tv_authority": actual.Request.OperationID, "observation_job_id": actual.Request.Lease.ID, "inventory_stamp": evidence})
		if _, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: s.ids.NewUUID(), Kind: "download_organization", DedupeKey: automaticTVObservationText("organize:" + actual.Request.TaskID.String()), Payload: payload, MaxAttempts: 20, NotBefore: job.NotBefore}); e != nil {
			return nil, e
		}
	}
	out = &ports.AutomaticTVObservationReceipt{TaskID: actual.Request.TaskID, State: state, ReviewRequired: review, Complete: complete, EvidenceStamp: evidence}
	safe := mustAutomaticTVJSON(out)
	if e = (&AutomaticTVSubmissionStore{ids: s.ids}).automaticTVSubmissionEvent(ctx, q, actual.Authority, "automatic_tv.observation_recorded", actual.Request.TaskID, safe); e != nil {
		return nil, e
	}
	finished, e := q.FinishJob(ctx, db.FinishJobParams{Column1: actual.Request.Lease.ID, Column2: actual.Request.Lease.Owner, Column3: actual.Request.Lease.Token, Column4: safe})
	if e != nil {
		return nil, e
	}
	if finished.ID != actual.Request.Lease.ID || finished.State != "succeeded" {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	// Flush deferred publication checks and triggers before the final database
	// clock check, so commit cannot hide work past the original lease.
	if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
		return nil, e
	}
	if _, e = automaticTVObservationPhysical(ctx, tx, actual.Request, actual.SendPhysicalAttemptID); e != nil {
		return nil, e
	}
	if e = automaticTVObservationJob(ctx, tx, actual.Request, true, job.LeaseUntil.Time); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *AutomaticTVObservationStore) ScheduleAutomaticTVObservations(ctx context.Context, limit int32) (n int, err error) {
	defer func() { err = automaticTVObservationError(ctx, err) }()
	if limit < 1 || limit > 100 {
		return 0, ports.ErrAutomaticTVObservationStale
	}
	if e := s.ready(ctx); e != nil {
		return 0, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer managementRollback(ctx, tx)
	rows, e := tx.Query(ctx, `SELECT p.operation_id,p.task_id,p.physical_attempt_id FROM automatic_tv_submission_preparation p JOIN download_task t ON t.id=p.task_id
 JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id JOIN job original ON original.id=p.job_id
 WHERE t.state IN ('submitting','unknown','tracking') AND x.finished_at IS NOT NULL
 AND (original.state<>'running' OR original.lease_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=p.operation_id AND active.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit active WHERE active.operation_id=p.operation_id AND active.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_job b JOIN job j ON j.id=b.job_id WHERE b.operation_id=p.operation_id AND (j.state<>'succeeded' OR NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result prior WHERE prior.job_id=j.id AND prior.state='tracking' AND NOT prior.review_required AND NOT prior.complete)))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result r WHERE r.operation_id=p.operation_id AND (r.review_required OR r.complete))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result r WHERE r.operation_id=p.operation_id AND r.next_poll_at>clock_timestamp())
 ORDER BY p.created_at,p.operation_id LIMIT $1 FOR UPDATE OF p SKIP LOCKED`, limit)
	if e != nil {
		return 0, e
	}
	type candidate struct{ op, task, send uuid.UUID }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.op, &c.task, &c.send); e != nil {
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
	q := db.New(tx)
	for _, c := range candidates {
		id := s.ids.NewUUID()
		r := ports.AutomaticTVObservationRequest{OperationID: c.op, TaskID: c.task}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return 0, e
		}
		if _, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: id, Kind: ports.AutomaticTVObservationJobKind, DedupeKey: automaticTVObservationText(c.op.String()), Payload: automaticTVObservationPayloadBytes(r), MaxAttempts: 3, NotBefore: pgtype.Timestamptz{Time: now, Valid: true}}); e != nil {
			return 0, e
		}
		if e = q.AutomaticTVObservationInsertJob(ctx, db.AutomaticTVObservationInsertJobParams{JobID: id, OperationID: c.op, TaskID: c.task, SendAttemptID: c.send}); e != nil {
			return 0, e
		}
		n++
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return n, nil
}

func (s *AutomaticTVObservationStore) RecoverAutomaticTVObservations(ctx context.Context, limit int32) (n int, err error) {
	defer func() { err = automaticTVObservationError(ctx, err) }()
	if limit < 1 || limit > 100 {
		return 0, ports.ErrAutomaticTVObservationStale
	}
	if e := s.ready(ctx); e != nil {
		return 0, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer managementRollback(ctx, tx)
	rows, e := tx.Query(ctx, `WITH locked AS MATERIALIZED (
 SELECT j.id FROM job j JOIN automatic_tv_observation_job b ON b.job_id=j.id
 JOIN automatic_tv_submission_preparation p ON p.operation_id=b.operation_id
 JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id
 WHERE j.kind='automatic_tv_observation' AND j.state='running' AND j.lease_until<=clock_timestamp() AND x.finished_at IS NOT NULL
 AND j.dedupe_key=b.operation_id::text AND j.payload=jsonb_build_object('operation_id',b.operation_id,'task_id',b.task_id,'automatic_tv_authority',b.operation_id)
 AND pg_try_advisory_xact_lock(('x'||substr(encode(sha256(convert_to('pulsemedia.automatic-tv.physical.v1:'||b.operation_id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=b.operation_id AND active.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit active WHERE active.operation_id=b.operation_id AND active.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result r WHERE r.job_id=j.id)
 ORDER BY j.id LIMIT $1 FOR UPDATE OF j SKIP LOCKED)
 UPDATE job j SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'queued' END,lease_token = j.lease_token + 1,lease_owner=NULL,lease_until=NULL,
 error=CASE WHEN attempts>=max_attempts THEN 'automatic_tv_observation_review' ELSE NULL END,updated_at=clock_timestamp()
 FROM locked WHERE j.id=locked.id AND j.state='running' AND j.lease_until<=clock_timestamp() RETURNING j.id`, limit)
	if e != nil {
		return 0, e
	}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		n++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return n, nil
}
