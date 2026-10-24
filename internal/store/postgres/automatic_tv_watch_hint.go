package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const automaticTVWatchTTL = 5 * time.Second

type automaticTVWatchCapture struct {
	request  ports.AutomaticTVObservationHintRequest
	store    *AutomaticTVWatchStore
	digest   string
	deadline time.Time
	nextPoll time.Time
}

func (c *automaticTVWatchCapture) AutomaticTVObservationHintRequest() ports.AutomaticTVObservationHintRequest {
	return c.request
}

func automaticTVWatchLocalPath(raw []byte, remote string) string {
	mapping, e := watchMapping(raw)
	if e != nil {
		return ""
	}
	return naming.TranslateRemotePath(mapping, remote)
}

func automaticTVWatchBinding(ctx context.Context, q *db.Queries, b ports.DownloadWatchBinding) error {
	b.ClientVersion = b.ClientVersion.UTC()
	if b.MediaType != "tv" {
		return ports.ErrDownloadWatchStale
	}
	return watchCurrentBinding(ctx, q, b)
}

func (s *AutomaticTVWatchStore) remaining(r ports.AutomaticTVObservationHintRequest) (time.Duration, error) {
	if s == nil || s.pool == nil || s.clock == nil || s.ids == nil {
		return 0, ports.ErrUnavailable
	}
	if r.EventID == uuid.Nil || r.InstanceID == uuid.Nil || r.Sequence <= 0 || r.SourceIdentity.Inode <= 0 || r.SourceIdentity.Device < 0 || r.ReceivedAt.IsZero() || r.ManifestAt.IsZero() || r.Hint.Metadata != (ports.RootChangeMetadata{}) || !watchHint(r.Hint) || (r.Hint.Kind != ports.RootChangeCloseWrite && r.Hint.Kind != ports.RootChangeMovedTo) {
		return 0, ports.ErrRejected
	}
	if r.Binding.LocalRoot == r.Binding.RootPath && (r.SourceIdentity.Device != r.Binding.RootIdentity.Device || r.SourceIdentity.Inode != r.Binding.RootIdentity.Inode) {
		return 0, ports.ErrRejected
	}
	age := s.clock.Since(r.ReceivedAt)
	if age < 0 {
		return 0, ports.ErrRejected
	}
	if age >= automaticTVWatchTTL {
		return 0, ports.ErrAutomaticTVObservationStale
	}
	return automaticTVWatchTTL - age, nil
}

func automaticTVWatchPriorJob(ctx context.Context, tx pgx.Tx, r ports.AutomaticTVObservationHintRequest) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT state='succeeded' AND lease_owner IS NULL AND lease_until IS NULL AND kind='automatic_tv_observation' AND dedupe_key=$2::uuid::text AND payload=$3::jsonb AND updated_at<=$4 FROM job WHERE id=$1`, r.PriorJobID, r.OperationID, automaticTVObservationPayloadBytes(ports.AutomaticTVObservationRequest{OperationID: r.OperationID, TaskID: r.TaskID}), r.ManifestAt).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ports.ErrAutomaticTVObservationStale
	}
	return nil
}

// Validate exclusions again after the preparation mutex and after deferred triggers.
// Only the exact newly queued job in this transaction can be ignored.
func automaticTVWatchHistory(ctx context.Context, tx pgx.Tx, r ports.AutomaticTVObservationHintRequest, own uuid.UUID) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT
 (SELECT id FROM job WHERE kind='automatic_tv_observation' AND payload->>'operation_id'=$1::uuid::text AND id<>$5 ORDER BY created_at DESC,id DESC LIMIT 1)=$3
 AND EXISTS(SELECT 1 FROM automatic_tv_observation_job b JOIN automatic_tv_observation_result o ON o.job_id=b.job_id WHERE b.job_id=$3 AND b.operation_id=$1 AND b.task_id=$2 AND b.send_attempt_id=$4 AND o.operation_id=$1 AND o.task_id=$2 AND o.send_attempt_id=$4 AND o.state='tracking' AND NOT o.complete AND NOT o.review_required AND o.next_poll_at IS NOT NULL AND o.created_at<=$6)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt x WHERE x.operation_id=$1 AND x.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit x WHERE x.operation_id=$1 AND x.finished_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM job j WHERE j.kind='automatic_tv_download_submission' AND j.payload->>'operation_id'=$1::uuid::text AND (j.state='queued' OR (j.state='running' AND j.lease_until>clock_timestamp())))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_job b JOIN job j ON j.id=b.job_id LEFT JOIN automatic_tv_observation_result o ON o.job_id=j.id WHERE b.operation_id=$1 AND j.id<>$5 AND (j.state<>'succeeded' OR o.job_id IS NULL OR o.state<>'tracking' OR o.review_required OR o.complete))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result o WHERE o.operation_id=$1 AND (o.review_required OR o.complete OR o.state<>'tracking' OR o.next_poll_at IS NULL))
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_observation_result o WHERE o.operation_id=$1 AND o.next_poll_at>(SELECT next_poll_at FROM automatic_tv_observation_result WHERE job_id=$3))
 AND ($5::uuid='00000000-0000-0000-0000-000000000000' OR EXISTS(SELECT 1 FROM job j JOIN automatic_tv_observation_job b ON b.job_id=j.id WHERE j.id=$5 AND j.state='queued' AND j.kind='automatic_tv_observation' AND j.dedupe_key=$1::uuid::text AND j.payload=jsonb_build_object('operation_id',$1::uuid,'task_id',$2::uuid,'automatic_tv_authority',$1::uuid) AND b.operation_id=$1 AND b.task_id=$2 AND b.send_attempt_id=$4))`, r.OperationID, r.TaskID, r.PriorJobID, r.SendAttemptID, own, r.ManifestAt).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ports.ErrAutomaticTVObservationStale
	}
	return nil
}

func (s *AutomaticTVWatchStore) facts(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVObservationHintRequest, own uuid.UUID) (string, time.Time, error) {
	fail := ports.ErrAutomaticTVObservationStale
	if r.OperationID == uuid.Nil || r.TaskID == uuid.Nil || r.PriorJobID == uuid.Nil || r.SendAttemptID == uuid.Nil {
		return "", time.Time{}, fail
	}
	if _, e := s.remaining(r); e != nil {
		return "", time.Time{}, e
	}
	// Stable SQL JSON timestamp representation without rewriting frozen JSON bytes.
	if _, e := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); e != nil {
		return "", time.Time{}, e
	}
	a, e := automaticTVSubmissionAuthority(ctx, q, r.OperationID)
	if e != nil {
		return "", time.Time{}, e
	}
	eligible, _, e := automaticTVWatchCurrent(ctx, tx, q, r, a)
	if e != nil {
		return "", time.Time{}, e
	}
	if !eligible {
		return "", time.Time{}, fail
	}
	task, e := q.LockDownloadTask(ctx, r.TaskID)
	if e != nil {
		return "", time.Time{}, e
	}
	if task.OperationID != a.OperationID || task.ClientID != a.Target.ClientID || task.CandidateID != a.CandidateID || (task.State != "submitting" && task.State != "unknown" && task.State != "tracking") || r.Binding.ClientID != task.ClientID || r.Binding.RootID != a.Target.RootID {
		return "", time.Time{}, fail
	}
	p, e := q.AutomaticTVSubmissionPreparation(ctx, a.OperationID)
	if e != nil {
		return "", time.Time{}, e
	}
	var proof automaticTVPreparedProof
	if json.Unmarshal(p.Proof, &proof) != nil || p.TaskID != task.ID || p.PhysicalAttemptID != r.SendAttemptID || p.AuthorityHash != a.DecisionStamp || p.InfoHash != task.InfoHash || p.ProofHash != completeTVJSONDigest(p.Proof) || proof.Kind != "automatic_tv_prepared_v1" || proof.InfoHash != task.InfoHash || proof.SavePath != task.SavePath || proof.TotalBytes <= 0 || !bytes.Equal(p.TorrentSha256, proof.SHA256[:]) || completeTVJSONDigest(mustAutomaticTVJSON(proof.Authority)) != completeTVJSONDigest(mustAutomaticTVJSON(a)) {
		return "", time.Time{}, fail
	}
	var total int64
	for _, f := range proof.Files {
		if f.Size < 0 || f.Size > proof.TotalBytes-total {
			return "", time.Time{}, fail
		}
		total += f.Size
	}
	if total != proof.TotalBytes {
		return "", time.Time{}, fail
	}
	video, e := media.ValidateAutomaticTVSingleEpisodeFiles(proof.Files, automaticTVObservationTarget(a))
	if e != nil || video == nil || *video != proof.Video {
		return "", time.Time{}, fail
	}
	var original bool
	e = tx.QueryRow(ctx, `SELECT operation_id=$1 AND job_id=$2 AND lease_owner=$3 AND lease_token=$4 AND finished_at IS NOT NULL FROM automatic_tv_physical_attempt WHERE id=$5`, a.OperationID, p.JobID, p.LeaseOwner, p.LeaseToken, p.PhysicalAttemptID).Scan(&original)
	if e != nil {
		return "", time.Time{}, e
	}
	if !original {
		return "", time.Time{}, fail
	}
	if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
		return "", time.Time{}, e
	}
	if e = automaticTVSubmissionProtections(ctx, tx, q, a, task.ID); e != nil {
		return "", time.Time{}, e
	}
	if e = automaticTVWatchBinding(ctx, q, r.Binding); e != nil {
		return "", time.Time{}, e
	}
	raw, e := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: task.ClientID, RootID: a.Target.RootID})
	if e != nil {
		return "", time.Time{}, e
	}
	var root watchRootMaterial
	if e = statsDecode(raw, &root); e != nil {
		return "", time.Time{}, e
	}
	c := automaticTVWatchCandidate{watchTaskMaterial: watchTaskMaterial{TaskID: task.ID, OperationID: a.OperationID, ClientID: task.ClientID, SavePath: task.SavePath, Root: root}}
	for _, f := range proof.Files {
		c.PreparedPaths = append(c.PreparedPaths, f.Path)
	}
	if !automaticTVWatchPreparedPath(c, r.Binding, r.Hint) {
		return "", time.Time{}, fail
	}
	if e = automaticTVWatchHistory(ctx, tx, r, own); e != nil {
		return "", time.Time{}, e
	}
	prior, e := q.AutomaticTVObservationReadResult(ctx, r.PriorJobID)
	if e != nil {
		return "", time.Time{}, e
	}
	if prior.AuthorityHash != a.DecisionStamp || prior.ProofHash != p.ProofHash || !prior.NextPollAt.Valid || prior.NextPollAt.Time.Sub(prior.CreatedAt.Time) != 2*time.Second {
		return "", time.Time{}, fail
	}
	var protection []byte
	e = tx.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x WHERE operation_id=$1),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x WHERE operation_id=$1))`, r.OperationID).Scan(&protection)
	if e != nil {
		return "", time.Time{}, e
	}
	digest := watchDigest([]any{completeTVJSONDigest(mustAutomaticTVJSON(a)), completeTVJSONDigest(mustAutomaticTVJSON(p)), completeTVJSONDigest(protection), prior, task.ID, task.OperationID, task.ClientID, task.CandidateID, task.InfoHash, task.SavePath, root})
	return digest, prior.NextPollAt.Time, nil
}

func (s *AutomaticTVWatchStore) resolve(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVObservationHintRequest) (*automaticTVWatchCapture, error) {
	// Start the DB deadline before validation so a slow resolution cannot extend it.
	var now time.Time
	if e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, e
	}
	remaining, e := s.remaining(r)
	if e != nil {
		return nil, e
	}
	if r.ManifestAt.After(now) {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	digest, next, e := s.facts(ctx, tx, q, r, uuid.Nil)
	if e != nil {
		return nil, e
	}
	return &automaticTVWatchCapture{request: r, store: s, digest: digest, deadline: now.Add(remaining), nextPoll: next}, nil
}

func (s *AutomaticTVWatchStore) ResolveAutomaticTVObservationHint(ctx context.Context, r ports.AutomaticTVObservationHintRequest) (out ports.AutomaticTVObservationHintCapture, err error) {
	defer func() { err = automaticTVWatchError(ctx, err) }()
	if _, e := s.remaining(r); e != nil {
		return nil, e
	}
	tx, q, e := (&DownloadWatchStore{pool: s.pool}).read(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	c, e := s.resolve(ctx, tx, q, r)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return c, nil
}

func automaticTVWatchError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrDownloadWatchStale) || errors.Is(e, ErrRestoreIsolated) {
		return e
	}
	return automaticTVObservationError(ctx, e)
}

func (s *AutomaticTVWatchStore) ScheduleAutomaticTVObservationHint(ctx context.Context, capture ports.AutomaticTVObservationHintCapture) (out *ports.AutomaticTVObservationHintScheduleResult, err error) {
	defer func() {
		err = automaticTVWatchError(ctx, err)
		if err != nil {
			out = nil
		}
	}()
	c, ok := capture.(*automaticTVWatchCapture)
	if !ok || c == nil || c.store != s {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if _, e := s.remaining(c.request); e != nil {
		return nil, e
	}
	tx, q, e := (&DownloadWatchStore{pool: s.pool}).read(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	digest, next, e := s.facts(ctx, tx, q, c.request, uuid.Nil)
	if e != nil {
		return nil, e
	}
	if digest != c.digest || !next.Equal(c.nextPoll) {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	// The periodic scheduler takes only this mutex; never add business locks to it.
	var op uuid.UUID
	if e = tx.QueryRow(ctx, `SELECT operation_id FROM automatic_tv_submission_preparation WHERE operation_id=$1 FOR UPDATE`, c.request.OperationID).Scan(&op); e != nil {
		return nil, e
	}
	if e = automaticTVWatchHistory(ctx, tx, c.request, uuid.Nil); e != nil {
		return nil, e
	}
	var now time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, e
	}
	if !now.Before(c.deadline) {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	remaining, e := s.remaining(c.request)
	if e != nil {
		return nil, e
	}
	wait := next.Sub(now)
	if wait > 0 {
		if wait > 2*time.Second || wait >= remaining || !next.Before(c.deadline) {
			return nil, ports.ErrAutomaticTVObservationStale
		}
		return &ports.AutomaticTVObservationHintScheduleResult{RetryAfter: wait}, nil
	}
	id := s.ids.NewUUID()
	job, e := q.EnqueueJob(ctx, db.EnqueueJobParams{ID: id, Kind: ports.AutomaticTVObservationJobKind, DedupeKey: automaticTVObservationText(op.String()), Payload: automaticTVObservationPayloadBytes(ports.AutomaticTVObservationRequest{OperationID: op, TaskID: c.request.TaskID}), MaxAttempts: 3, NotBefore: pgtype.Timestamptz{Time: now, Valid: true}})
	if e != nil {
		return nil, e
	}
	if job.ID != id {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if e = q.AutomaticTVObservationInsertJob(ctx, db.AutomaticTVObservationInsertJobParams{JobID: id, OperationID: op, TaskID: c.request.TaskID, SendAttemptID: c.request.SendAttemptID}); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
		return nil, e
	}
	digest, next, e = s.facts(ctx, tx, q, c.request, id)
	if e != nil {
		return nil, e
	}
	if digest != c.digest || !next.Equal(c.nextPoll) {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, e
	}
	if now.Before(next) || !now.Before(c.deadline) {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if _, e = s.remaining(c.request); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.AutomaticTVObservationHintScheduleResult{JobID: id, Queued: true}, nil
}
