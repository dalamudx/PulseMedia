package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const completeTVHintMaxAge = 5 * time.Second

type completeTVHintCapture struct {
	request  ports.CompleteTVTrackingHintRequest
	deadline time.Time
	evidence ports.CompleteTVTrackingHintEvidence
	replay   bool
}

func (c completeTVHintCapture) TrackingHintRequest() ports.CompleteTVTrackingHintRequest {
	return c.request
}

var _ ports.CompleteTVTrackingHintScheduler = (*CompleteTVTrackingRuntimeStore)(nil)

type hintAuthorityState struct {
	task                   db.DownloadTask
	authority, preparation string
}

// Account -> watch root -> sorted frozen units -> operation -> task -> prior
// job -> immutable authority/preparation -> protections -> client. No physical
// permit is waited on or created here. Keep the original tracking proof checks.

func lockHintAuthority(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingHintRequest) (*hintAuthorityState, string, error) {
	stale := ports.ErrCompleteTVObservationStale
	if r.TaskID == uuid.Nil || r.OperationID == uuid.Nil {
		return nil, "", stale
	}
	q := db.New(tx)
	record, err := q.GetCompleteTVExecutionAuthority(ctx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || frozen.Kind != "reviewed_complete_tv_coverage_v1" || frozen.ActorID != record.ActorID || frozen.ReviewID != record.ReviewID || frozen.SubscriptionID != record.SubscriptionID || frozen.RootID != record.RootID || frozen.CandidateID != record.CandidateID || frozen.ClientID != record.ClientID || len(frozen.Units) < 2 || len(frozen.Units) > 1000 {
		return nil, "", stale
	}
	var id uuid.UUID
	// Lock even disabled accounts, without requiring present-day command rights.
	if err = tx.QueryRow(ctx, `SELECT id FROM app_user WHERE id=$1 FOR SHARE`, frozen.ActorID).Scan(&id); err != nil {
		return nil, "", err
	}
	if frozen.RootID != r.Binding.RootID || frozen.RootGeneration != r.Binding.Generation || frozen.ClientID != r.Binding.ClientID {
		return nil, "", stale
	}
	if _, err = q.LockDownloadWatchRoot(ctx, r.Binding.RootID); err != nil {
		return nil, "", err
	}
	if err = watchCurrentBinding(ctx, q, r.Binding); err != nil {
		return nil, "", err
	}
	units := append([]media.NumberingRow(nil), frozen.Units...)
	sort.Slice(units, func(i, j int) bool { return units[i].UnitID.String() < units[j].UnitID.String() })
	for i, u := range units {
		if u.UnitID == uuid.Nil || (i > 0 && units[i-1].UnitID == u.UnitID) {
			return nil, "", stale
		}
		if _, err = q.LockAcquisitionUnit(ctx, u.UnitID); err != nil {
			return nil, "", err
		}
	}
	if err = q.LockDownloadAcquisition(ctx, r.OperationID); err != nil {
		return nil, "", err
	}
	task, err := q.LockDownloadTask(ctx, r.TaskID)
	if err != nil {
		return nil, "", err
	}
	if task.OperationID != r.OperationID || task.ClientID != frozen.ClientID || task.CandidateID != frozen.CandidateID || task.State != "tracking" {
		return nil, "", stale
	}
	if _, err = q.LockJobForWrite(ctx, r.PriorJobID); err != nil {
		return nil, "", err
	}
	// Explicitly lock immutable evidence too; schema triggers are not a row lock.
	for _, query := range []string{`SELECT operation_id FROM complete_tv_execution_authority WHERE operation_id=$1 FOR SHARE`, `SELECT operation_id FROM complete_tv_submission_preparation WHERE operation_id=$1 FOR SHARE`} {
		if err = tx.QueryRow(ctx, query, r.OperationID).Scan(&id); err != nil {
			return nil, "", err
		}
	}
	lockedRecord, e := q.GetCompleteTVExecutionAuthority(ctx, r.OperationID)
	if e != nil {
		return nil, "", e
	}
	if coverageDigest(lockedRecord) != coverageDigest(record) {
		return nil, "", stale
	}
	prepared, err := q.GetCompleteTVPreparation(ctx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	var proof completeTVPreparedProof
	if json.Unmarshal(prepared.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || prepared.TaskID != task.ID || prepared.JobID == r.PriorJobID || prepared.LeaseOwner == "" || prepared.InfoHash != task.InfoHash || len(task.InfoHash) != 40 || !bytes.Equal(prepared.TorrentSha256, proof.TorrentSHA256[:]) || proof.InfoHash != task.InfoHash || proof.SavePath == "" || proof.SavePath != task.SavePath || proof.MaterialStamp != frozen.MaterialStamp || proof.TotalBytes <= 0 || proof.TotalBytes != frozen.CandidateSize || len(proof.Files) != len(units) {
		return nil, "", stale
	}
	// Lock every association and requirement, including inactive rows, so the seal
	// identifies the full set rather than a count of whichever subset passed.
	for _, query := range []string{`SELECT unit_id FROM complete_tv_submission_unit WHERE task_id=$1 ORDER BY unit_id FOR SHARE`} {
		rows, e := tx.Query(ctx, query, task.ID)
		if e != nil {
			return nil, "", e
		}
		if _, e = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); e != nil {
			return nil, "", e
		}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM download_requirement WHERE operation_id=$1 ORDER BY unit_id,root_id,id FOR UPDATE`, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	if _, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return nil, "", err
	}
	for _, query := range []string{
		`SELECT id FROM acquisition_reservation WHERE operation_id=$1 ORDER BY unit_id,id FOR UPDATE`,
		`SELECT id FROM unit_reservation WHERE operation_id=$1 ORDER BY unit_id,root_id,id FOR UPDATE`,
	} {
		locked, e := tx.Query(ctx, query, r.OperationID)
		if e != nil {
			return nil, "", e
		}
		ids, e := pgx.CollectRows(locked, pgx.RowTo[uuid.UUID])
		if e != nil {
			return nil, "", e
		}
		if len(ids) != len(units) {
			return nil, "", stale
		}
	}
	declared, err := q.ListCompleteTVPreparedUnits(ctx, task.ID)
	if err != nil {
		return nil, "", err
	}
	demands, err := q.ListRequirementsForOperation(ctx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	lanes, err := q.ListCompleteTVPreparationLanes(ctx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	reservations, err := q.ListCompleteTVPreparationReservations(ctx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	if len(declared) != len(units) || len(demands) != len(units) || len(lanes) != len(units) || len(reservations) != len(units) {
		return nil, "", stale
	}
	sort.Slice(proof.Files, func(i, j int) bool { return proof.Files[i].Unit.UnitID.String() < proof.Files[j].Unit.UnitID.String() })
	sort.Slice(demands, func(i, j int) bool { return demands[i].UnitID.String() < demands[j].UnitID.String() })
	for i, u := range units {
		p, f, d, l, v := declared[i], proof.Files[i], demands[i], lanes[i], reservations[i]
		fence, _ := json.Marshal(ports.DownloadDemandFence{PolicyID: frozen.PolicyID, PolicyRevision: frozen.PolicyRevision, SubscriptionID: &frozen.SubscriptionID, IntentVersion: frozen.IntentVersion, RootGeneration: frozen.RootGeneration, UnitMetadataVersion: u.UnitVersion, MediaMetadataVersion: frozen.MediaVersion, CandidateID: frozen.CandidateID, RootID: frozen.RootID, UnitID: u.UnitID, MediaID: frozen.MediaID, ActorID: frozen.ActorID.String(), ActorType: ports.ActorTypeUser, SourceStamp: frozen.SourceStamp})
		if f.Unit != u || p.UnitID != u.UnitID || p.UnitVersion != u.UnitVersion || p.ProviderEpisodeID != u.EpisodeID || p.UnitKey != u.UnitKey || p.VideoPath != f.Path || p.VideoSize != f.Size || d.UnitID != u.UnitID || d.RootID != frozen.RootID || d.ActorID != frozen.ActorID.String() || d.ActorType != "user" || d.State != "pending" || d.ReleasedAt.Valid || completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(fence) || l.UnitID != u.UnitID || l.AcquisitionLane != "default" || l.LegacyConflict || l.SourceRootID != frozen.RootID || l.SourceGeneration != frozen.RootGeneration || l.SourceStamp != frozen.SourceStamp || l.CandidateID != frozen.CandidateID || !l.TaskID.Valid || uuid.UUID(l.TaskID.Bytes) != task.ID || v.UnitID != u.UnitID || v.RootID != frozen.RootID || v.CandidateID != frozen.CandidateID || v.State != "dispatched" || v.Purpose != "manual_acquisition" {
			return nil, "", stale
		}
	}
	client, err := q.LockEpisodeSubmissionClient(ctx, task.ClientID)
	if err != nil {
		return nil, "", err
	}
	stamp := episodeClientStamp(client)
	if stamp != frozen.ClientStamp {
		return nil, "", stale
	}

	raw, err := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: r.Binding.ClientID, RootID: r.Binding.RootID})
	if err != nil {
		return nil, "", err
	}
	var root watchRootMaterial
	if err = statsDecode(raw, &root); err != nil {
		return nil, "", err
	}
	material := watchTaskMaterial{Eligible: true, SavePath: task.SavePath, ContentPath: task.ContentPath.String, Root: root}
	if !watchTaskBinding(material, r.Binding, r.Hint) {
		return nil, "", stale
	}
	protection, err := trackingProtection(ctx, tx, ports.CompleteTVTrackingRequest{TaskID: r.TaskID, OperationID: r.OperationID})
	if err != nil {
		return nil, "", err
	}
	return &hintAuthorityState{task: task, authority: coverageDigest(record), preparation: coverageDigest(prepared)}, protection, nil
}

func hintRequestValid(r ports.CompleteTVTrackingHintRequest) bool {
	if r.Binding.LocalRoot == r.Binding.RootPath && (r.SourceIdentity.Device != r.Binding.RootIdentity.Device || r.SourceIdentity.Inode != r.Binding.RootIdentity.Inode) {
		return false
	}
	return r.OperationID != uuid.Nil && r.TaskID != uuid.Nil && r.PriorJobID != uuid.Nil && r.HintID != uuid.Nil && r.InstanceID != uuid.Nil && r.Sequence > 0 && r.SourceIdentity.Inode > 0 && !r.ReceivedAt.IsZero() && watchHint(r.Hint) && r.Hint.Kind != ports.RootChangeRescan
}
func hintRequestDigest(r ports.CompleteTVTrackingHintRequest) string {
	// Receipt time is local freshness, never a PG absolute timestamp or replay key.
	r.ReceivedAt = time.Time{}
	return watchDigest(r)
}
func hintEvidence(row db.CompleteTvTrackingHint) (*ports.CompleteTVTrackingHintEvidence, error) {
	e := &ports.CompleteTVTrackingHintEvidence{Receipt: ports.CompleteTVTrackingHintReceipt{JobID: row.JobID, TaskID: row.TaskID, OperationID: row.OperationID, PriorJobID: row.PriorJobID, HintID: row.HintID, AcceptedAt: row.AcceptedAt.Time}, InstanceID: row.InstanceID, Sequence: row.Sequence, Hint: ports.RootChangeHint{Kind: ports.RootChangeKind(row.Kind), RelativePath: row.RelativePath}, SourceIdentity: ports.FileIdentity{Device: row.SourceDevice, Inode: row.SourceInode}, PriorDigest: row.PriorDigest, AuthorityDigest: row.AuthorityDigest, PreparationDigest: row.PreparationDigest, ProtectionDigest: row.ProtectionDigest, RequestDigest: row.RequestDigest}
	if err := statsDecode(row.Binding, &e.Binding); err != nil {
		return nil, err
	}
	e.Digest = coverageDigest(row)
	request := hintEvidenceRequest(*e)
	if !hintRequestValid(request) || hintRequestDigest(request) != e.RequestDigest {
		return nil, ports.ErrCompleteTVObservationStale
	}
	return e, nil
}
func hintEvidenceRequest(e ports.CompleteTVTrackingHintEvidence) ports.CompleteTVTrackingHintRequest {
	return ports.CompleteTVTrackingHintRequest{OperationID: e.Receipt.OperationID, TaskID: e.Receipt.TaskID, PriorJobID: e.Receipt.PriorJobID, HintID: e.Receipt.HintID, InstanceID: e.InstanceID, Sequence: e.Sequence, Hint: e.Hint, Binding: e.Binding, SourceIdentity: e.SourceIdentity, ReceivedAt: e.Receipt.AcceptedAt}
}
func hintReplay(ctx context.Context, q *db.Queries, r ports.CompleteTVTrackingHintRequest) (*ports.CompleteTVTrackingHintEvidence, error) {
	row, err := q.GetCompleteTVTrackingHintReplay(ctx, db.GetCompleteTVTrackingHintReplayParams{OperationID: r.OperationID, HintID: r.HintID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e, err := hintEvidence(row)
	if err != nil {
		return nil, err
	}
	if e.RequestDigest != hintRequestDigest(r) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	return e, nil
}

// Resolve binds an explicit predecessor once. The returned concrete capture is
// private, so a caller cannot extend the DB deadline or replace its proof.
func (s *CompleteTVTrackingRuntimeStore) ResolveCompleteTVTrackingHint(ctx context.Context, r ports.CompleteTVTrackingHintRequest, clock ports.Clock) (ports.CompleteTVTrackingHintCapture, error) {
	r.SourceIdentity.Size, r.SourceIdentity.Mtime = 0, 0
	if !hintRequestValid(r) || clock == nil {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	held, err := q.LockCompleteTVTrackingScheduler(ctx)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ports.ErrUnavailable
	}
	if err = watchGuard(ctx, q); err != nil {
		return nil, err
	}
	replay, err := hintReplay(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return completeTVHintCapture{request: r, evidence: *replay, replay: true}, nil
	}
	age := clock.Since(r.ReceivedAt)
	if age < 0 || age > completeTVHintMaxAge {
		return nil, ports.ErrCompleteTVObservationStale
	}
	var start time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&start); err != nil {
		return nil, err
	}
	state, protection, err := lockHintAuthority(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	prior, err := hintPrior(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if err = hintIdle(ctx, tx, r, uuid.Nil); err != nil {
		return nil, err
	}
	age = clock.Since(r.ReceivedAt)
	if age < 0 || age > completeTVHintMaxAge {
		return nil, ports.ErrCompleteTVObservationStale
	}
	c := completeTVHintCapture{request: r, deadline: start.Add(completeTVHintMaxAge - age), evidence: ports.CompleteTVTrackingHintEvidence{PriorDigest: prior, AuthorityDigest: state.authority, PreparationDigest: state.preparation, ProtectionDigest: protection, RequestDigest: hintRequestDigest(r)}}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func hintPrior(ctx context.Context, q *db.Queries, r ports.CompleteTVTrackingHintRequest) (string, error) {
	j, err := q.GetCompleteTVTrackingHintPrior(ctx, r.TaskID.String())
	if err != nil {
		return "", err
	}
	var receipt ports.CompleteTVTrackingReceipt
	var payload struct {
		TaskID      uuid.UUID `json:"task_id"`
		OperationID uuid.UUID `json:"operation_id"`
	}
	if j.ID != r.PriorJobID || j.State != "succeeded" || json.Unmarshal(j.Result, &receipt) != nil || receipt.State != "tracking" || receipt.ReviewRequired || receipt.TaskID != r.TaskID || len(receipt.EvidenceStamp) != 64 || len(receipt.Files) != 0 || json.Unmarshal(j.Payload, &payload) != nil || payload.TaskID != r.TaskID || payload.OperationID != r.OperationID {
		return "", ports.ErrCompleteTVObservationStale
	}
	return completeTVJSONDigest(j.Result), nil
}
func hintIdle(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingHintRequest, own uuid.UUID) error {
	var idle bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM complete_tv_physical_permit WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM job WHERE payload->>'operation_id'=$1::text AND kind IN ('complete_tv_coverage_download_submission','complete_tv_coverage_observation','complete_tv_coverage_tracking') AND state IN ('queued','running') AND id<>$2)`, r.OperationID, own).Scan(&idle)
	if err != nil {
		return err
	}
	if !idle {
		return ports.ErrCompleteTVObservationStale
	}
	return nil
}

func (s *CompleteTVTrackingRuntimeStore) ScheduleCompleteTVTrackingHint(ctx context.Context, capture ports.CompleteTVTrackingHintCapture) (*ports.CompleteTVTrackingHintReceipt, error) {
	c, ok := capture.(completeTVHintCapture)
	if !ok {
		return nil, ports.ErrRejected
	}
	r := c.request
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	held, err := q.LockCompleteTVTrackingScheduler(ctx)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ports.ErrUnavailable
	}
	if err = watchGuard(ctx, q); err != nil {
		return nil, err
	}
	replay, err := hintReplay(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if replay != nil {
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &replay.Receipt, nil
	}
	if c.replay {
		return nil, ports.ErrCompleteTVObservationStale
	}
	state, protection, err := lockHintAuthority(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	prior, err := hintPrior(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if state.authority != c.evidence.AuthorityDigest || state.preparation != c.evidence.PreparationDigest || protection != c.evidence.ProtectionDigest || prior != c.evidence.PriorDigest {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = hintIdle(ctx, tx, r, uuid.Nil); err != nil {
		return nil, err
	}
	var fresh bool
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()<=$1`, c.deadline).Scan(&fresh); err != nil {
		return nil, err
	}
	if !fresh {
		return nil, ports.ErrCompleteTVObservationStale
	}
	taskBefore := coverageDigest(state.task)
	var priorBefore []byte
	if err = tx.QueryRow(ctx, `SELECT to_jsonb(j) FROM job j WHERE id=$1`, r.PriorJobID).Scan(&priorBefore); err != nil {
		return nil, err
	}
	jobID := s.ids.NewUUID()
	if err = q.EnqueueCompleteTVTracking(ctx, db.EnqueueCompleteTVTrackingParams{ID: jobID, DedupeKey: "complete-tv-track:" + r.TaskID.String(), TaskID: r.TaskID.String(), OperationID: r.OperationID.String()}); err != nil {
		return nil, err
	}
	binding, _ := json.Marshal(r.Binding)
	row, err := q.InsertCompleteTVTrackingHint(ctx, db.InsertCompleteTVTrackingHintParams{JobID: jobID, TaskID: r.TaskID, OperationID: r.OperationID, PriorJobID: r.PriorJobID, HintID: r.HintID, InstanceID: r.InstanceID, Sequence: r.Sequence, Kind: string(r.Hint.Kind), RelativePath: r.Hint.RelativePath, Binding: binding, SourceDevice: r.SourceIdentity.Device, SourceInode: r.SourceIdentity.Inode, PriorDigest: prior, AuthorityDigest: state.authority, PreparationDigest: state.preparation, ProtectionDigest: protection, RequestDigest: c.evidence.RequestDigest})
	if err != nil {
		return nil, err
	}
	evidence, err := hintEvidence(row)
	if err != nil {
		return nil, err
	}
	if evidence.Receipt.JobID != jobID || evidence.RequestDigest != c.evidence.RequestDigest || evidence.AuthorityDigest != state.authority || evidence.PreparationDigest != state.preparation || evidence.ProtectionDigest != protection || evidence.PriorDigest != prior {
		return nil, ports.ErrCompleteTVObservationStale
	}
	safe, _ := json.Marshal(evidence.Receipt)
	auditID := s.ids.NewUUID()
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: auditID, ActorType: "system", ActorID: r.InstanceID.String(), Action: "complete_tv.tracking_hint", TargetType: "download_task", TargetID: r.TaskID.String(), Payload: safe}); err != nil {
		return nil, err
	}
	// Force every deferred trigger before the final rereads, including custom
	// triggers that swap otherwise immutable proof inside this transaction.
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	tail, tailProtection, err := lockHintAuthority(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if coverageDigest(tail.task) != taskBefore || tail.authority != state.authority || tail.preparation != state.preparation || tailProtection != protection {
		return nil, ports.ErrCompleteTVObservationStale
	}
	var priorAfter []byte
	if err = tx.QueryRow(ctx, `SELECT to_jsonb(j) FROM job j WHERE id=$1`, r.PriorJobID).Scan(&priorAfter); err != nil {
		return nil, err
	}
	if completeTVJSONDigest(priorBefore) != completeTVJSONDigest(priorAfter) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	tailRow, err := q.GetCompleteTVTrackingHint(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if coverageDigest(tailRow) != coverageDigest(row) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = hintIdle(ctx, tx, r, jobID); err != nil {
		return nil, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND kind='complete_tv_coverage_tracking' AND state='queued' AND dedupe_key=$2 AND payload=jsonb_build_object('task_id',$3::text,'operation_id',$4::text) AND max_attempts=3 AND attempts=0 AND result IS NULL AND error IS NULL AND lease_owner IS NULL AND lease_until IS NULL AND id=(SELECT id FROM job WHERE kind='complete_tv_coverage_tracking' AND payload->>'task_id'=$3::text ORDER BY created_at DESC,id DESC LIMIT 1)) AND EXISTS(SELECT 1 FROM audit_log WHERE id=$5 AND action='complete_tv.tracking_hint' AND actor_type='system' AND actor_id=$6 AND target_type='download_task' AND target_id=$3::text AND payload=$7::jsonb) AND clock_timestamp()<=$8`, jobID, "complete-tv-track:"+r.TaskID.String(), r.TaskID, r.OperationID, auditID, r.InstanceID.String(), safe, c.deadline).Scan(&valid)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = watchGuard(ctx, q); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &evidence.Receipt, nil
}

// ReadCompleteTVTrackingHint returns nil for periodic jobs. Native filesystem
// reprobes happen after this read transaction, never while holding DB locks.
func (s *CompleteTVTrackingRuntimeStore) ReadCompleteTVTrackingHint(ctx context.Context, jobID uuid.UUID) (*ports.CompleteTVTrackingHintEvidence, error) {
	row, err := db.New(s.pool).GetCompleteTVTrackingHint(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return hintEvidence(row)
}

// LockCompleteTVTrackingHintRoot must run after the original account lock and
// BEFORE the frozen unit locks in lockTracking. Periodic jobs remain unchanged.
func LockCompleteTVTrackingHintRoot(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest) error {
	q := db.New(tx)
	row, err := q.GetCompleteTVTrackingHint(ctx, r.Lease.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := hintEvidence(row)
	if err != nil {
		return err
	}
	if e.Receipt.TaskID != r.TaskID || e.Receipt.OperationID != r.OperationID {
		return ports.ErrCompleteTVObservationStale
	}
	if err = watchGuard(ctx, q); err != nil {
		return err
	}
	_, err = q.LockDownloadWatchRoot(ctx, e.Binding.RootID)
	return err
}

// ValidateCompleteTVTrackingHint runs after existing lockTracking proof/lease
// validation. Include its returned digest in Seal. It does not acquire a
// physical permit or apply a cooldown.
func ValidateCompleteTVTrackingHint(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest) (string, error) {
	return validateCompleteTVTrackingHint(ctx, tx, r, true)
}

// ValidateCompleteTVTrackingHintEvidence repeats the immutable evidence checks
// after FinishJob and SET CONSTRAINTS ALL IMMEDIATE. Compare with the original
// digest; FinishJob's existing checks remain responsible for the completed lease.
func ValidateCompleteTVTrackingHintEvidence(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest) (string, error) {
	return validateCompleteTVTrackingHint(ctx, tx, r, false)
}

func validateCompleteTVTrackingHint(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest, running bool) (string, error) {
	q := db.New(tx)
	row, err := q.GetCompleteTVTrackingHint(ctx, r.Lease.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	e, err := hintEvidence(row)
	if err != nil {
		return "", err
	}
	if e.Receipt.TaskID != r.TaskID || e.Receipt.OperationID != r.OperationID {
		return "", ports.ErrCompleteTVObservationStale
	}
	if err = watchGuard(ctx, q); err != nil {
		return "", err
	}
	if err = watchCurrentBinding(ctx, q, e.Binding); err != nil {
		return "", err
	}
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT result FROM job WHERE id=$1 AND kind='complete_tv_coverage_tracking' AND state='succeeded' AND result->>'state'='tracking' AND payload->>'operation_id'=$2::text AND payload->>'task_id'=$3::text FOR SHARE`, e.Receipt.PriorJobID, r.OperationID, r.TaskID).Scan(&prior)
	if err != nil {
		return "", err
	}
	if completeTVJSONDigest(prior) != e.PriorDigest {
		return "", ports.ErrCompleteTVObservationStale
	}
	authority, err := q.GetCompleteTVExecutionAuthority(ctx, r.OperationID)
	if err != nil {
		return "", err
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(authority.Snapshot, &frozen) != nil || authority.RootID != e.Binding.RootID || authority.ClientID != e.Binding.ClientID || frozen.RootGeneration != e.Binding.Generation {
		return "", ports.ErrCompleteTVObservationStale
	}
	preparation, err := q.GetCompleteTVPreparation(ctx, r.OperationID)
	if err != nil {
		return "", err
	}
	protection, err := trackingProtection(ctx, tx, r)
	if err != nil {
		return "", err
	}
	if coverageDigest(authority) != e.AuthorityDigest || coverageDigest(preparation) != e.PreparationDigest || protection != e.ProtectionDigest {
		return "", ports.ErrCompleteTVObservationStale
	}
	if !running {
		return coverageDigest(row), nil
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND kind='complete_tv_coverage_tracking' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND payload=jsonb_build_object('task_id',$4::text,'operation_id',$5::text))`, r.Lease.ID, r.Lease.Owner, r.Lease.Token, r.TaskID, r.OperationID).Scan(&valid)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", ports.ErrCompleteTVObservationStale
	}
	return coverageDigest(row), nil
}

var _ ports.CompleteTVTrackingHintReader = (*CompleteTVTrackingStore)(nil)

// ReadCompleteTVTrackingHintForLease validates the running lease and original
// tracking authority under the manager's existing physical permit. The returned
// value is only a safe projection for filesystem reprobes after this UoW ends.
func (s *CompleteTVTrackingStore) ReadCompleteTVTrackingHintForLease(ctx context.Context, lease ports.JobLease) (out *ports.CompleteTVTrackingHintEvidence, err error) {
	defer func() {
		if err != nil {
			out = nil
			err = trackingError(ctx, err)
		}
	}()
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token <= 0 {
		return nil, ports.ErrCompleteTVObservationStale
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	row, err := q.GetCompleteTVTrackingHint(ctx, lease.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	evidence, err := hintEvidence(row)
	if err != nil {
		return nil, err
	}
	r := ports.CompleteTVTrackingRequest{Lease: lease, TaskID: evidence.Receipt.TaskID, OperationID: evidence.Receipt.OperationID}
	if err = watchGuard(ctx, q); err != nil {
		return nil, err
	}
	authority, err := q.GetCompleteTVExecutionAuthority(ctx, r.OperationID)
	if err != nil {
		return nil, err
	}
	var actor uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM app_user WHERE id=$1 FOR SHARE`, authority.ActorID).Scan(&actor); err != nil {
		return nil, err
	}
	if err = LockCompleteTVTrackingHintRoot(ctx, tx, r); err != nil {
		return nil, err
	}
	if _, _, err = s.lockTracking(ctx, tx, r); err != nil {
		return nil, err
	}
	var locked uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT job_id FROM complete_tv_tracking_hint WHERE job_id=$1 FOR SHARE`, lease.ID).Scan(&locked); err != nil {
		return nil, err
	}
	digest, err := ValidateCompleteTVTrackingHint(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if digest != evidence.Digest {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return evidence, nil
}
