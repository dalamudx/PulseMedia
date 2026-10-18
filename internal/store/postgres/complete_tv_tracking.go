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
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

// CompleteTVTrackingStore records original physical facts only. It has no
// acquisition, native client, sender or organization dependency.
type CompleteTVTrackingStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCompleteTVTrackingStore(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVTrackingStore {
	return &CompleteTVTrackingStore{pool: pool, ids: ids}
}

var _ ports.CompleteTVTrackingRepository = (*CompleteTVTrackingStore)(nil)

func trackingError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ports.ErrCompleteTVObservationStale) {
		return ports.ErrCompleteTVObservationStale
	}
	return ports.ErrUnavailable
}

func (s *CompleteTVTrackingStore) LoadCompleteTVTracking(ctx context.Context, r ports.CompleteTVTrackingRequest) (out *ports.CompleteTVTrackingSnapshot, err error) {
	defer func() {
		if err != nil {
			out = nil
			err = trackingError(ctx, err)
		}
	}()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	out, _, err = s.lockTracking(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *CompleteTVTrackingStore) CommitCompleteTVTracking(ctx context.Context, snapshot ports.CompleteTVTrackingSnapshot, remote ports.CompleteTVTrackingObservation) (out *ports.CompleteTVTrackingReceipt, err error) {
	defer func() {
		if err != nil {
			out = nil
			err = trackingError(ctx, err)
		}
	}()
	if !remote.Complete || remote.Before == nil || remote.After == nil {
		return nil, ports.ErrUnavailable
	}
	if remote.ClientID != snapshot.ClientID || remote.ClientStamp != snapshot.ClientStamp {
		return nil, ports.ErrCompleteTVObservationStale
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	current, protection, err := s.lockTracking(ctx, tx, snapshot.Request)
	if err != nil {
		return nil, err
	}
	if *current != snapshot {
		return nil, ports.ErrCompleteTVObservationStale
	}
	mutableBefore, err := trackingMutableFields(ctx, tx, snapshot.Request.TaskID)
	if err != nil {
		return nil, err
	}
	state, reason := "unknown", "complete TV tracking requires manual review"
	beforeOK := trackingIdentity(remote.Before, *current)
	afterOK := trackingIdentity(remote.After, *current)
	if beforeOK && afterOK {
		state, reason = "tracking", ""
		if trackingCompleted(remote.Before[0]) && trackingCompleted(remote.After[0]) {
			if remote.Files == nil || !remote.Metainfo.ValidateBinding() {
				return nil, ports.ErrUnavailable
			}
			prepared, e := db.New(tx).GetCompleteTVPreparation(ctx, snapshot.Request.OperationID)
			if e != nil {
				return nil, e
			}
			var proof completeTVPreparedProof
			if json.Unmarshal(prepared.Proof, &proof) != nil {
				return nil, ports.ErrCompleteTVObservationStale
			}
			if trackingFiles(proof, remote.Metainfo, remote.Files) {
				state = "completed"
			} else {
				state, reason = "unknown", "complete TV file set requires manual review"
			}
		}
	}
	task, err := db.New(tx).ApplyCompleteTVTrackingObservation(ctx, db.ApplyCompleteTVTrackingObservationParams{
		TaskID: snapshot.Request.TaskID, ObservedState: state, Reason: reason, TotalBytes: snapshot.TotalBytes,
	})
	if err != nil {
		return nil, err
	}
	out = &ports.CompleteTVTrackingReceipt{TaskID: task, State: state, ReviewRequired: state != "tracking", EvidenceStamp: trackingEvidenceStamp(remote)}
	if state == "completed" {
		out.Files = append([]ports.TorrentFile(nil), remote.Files...)
	}
	safe, _ := json.Marshal(out)
	q := db.New(tx)
	auditID := s.ids.NewUUID()
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: auditID, ActorType: "system", ActorID: snapshot.Request.Lease.Owner, Action: "complete_tv.tracking_observed", TargetType: "download_task", TargetID: task.String(), Payload: safe}); err != nil {
		return nil, err
	}
	event, err := (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", snapshot.Request.OperationID.String(), "complete_tv.tracking_observed", map[string]any{"operation_id": snapshot.Request.OperationID, "task_id": task, "state": state, "review_required": state != "tracking", "evidence_stamp": out.EvidenceStamp})
	if err != nil {
		return nil, err
	}
	// Re-read the entire protection/evidence set after late writes. Locks exclude
	// other writers; this also catches same-transaction trigger side effects.
	tail, err := trackingProtection(ctx, tx, snapshot.Request)
	if err != nil {
		return nil, err
	}
	if tail != protection {
		return nil, ports.ErrCompleteTVObservationStale
	}
	lease := snapshot.Request.Lease
	var deadline time.Time
	if err = tx.QueryRow(ctx, `SELECT lease_until FROM job WHERE id=$1`, lease.ID).Scan(&deadline); err != nil {
		return nil, err
	}
	finished, err := q.FinishJob(ctx, db.FinishJobParams{Column1: lease.ID, Column2: lease.Owner, Column3: lease.Token, Column4: safe})
	if err != nil {
		return nil, err
	}
	if finished.ID != lease.ID || finished.Kind != ports.CompleteTVTrackingJobKind || finished.State != "succeeded" || finished.LeaseToken != lease.Token || finished.LeaseOwner.Valid || finished.LeaseUntil.Valid || completeTVJSONDigest(finished.Result) != completeTVJSONDigest(safe) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	var completedBinding struct {
		TaskID      uuid.UUID `json:"task_id"`
		OperationID uuid.UUID `json:"operation_id"`
	}
	if json.Unmarshal(finished.Payload, &completedBinding) != nil || completedBinding.TaskID != snapshot.Request.TaskID || completedBinding.OperationID != snapshot.Request.OperationID {
		return nil, ports.ErrCompleteTVObservationStale
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !deadline.After(now) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	tail, err = trackingProtection(ctx, tx, snapshot.Request)
	if err != nil {
		return nil, err
	}
	if tail != protection {
		return nil, ports.ErrCompleteTVObservationStale
	}
	permit, err := completeTVPhysicalPermit(ctx, tx, snapshot.Request.OperationID)
	if err != nil {
		return nil, err
	}
	if permit != snapshot.PhysicalPermit {
		return nil, ports.ErrCompleteTVObservationStale
	}
	// The protection seal deliberately excludes our intended state/error write.
	// Check those fields independently after every audit/event/job trigger ran.
	var consistent bool
	if err = tx.QueryRow(ctx, `SELECT state=$2 AND coalesce(error,'')=$3 AND ($2<>'completed' OR (progress=1 AND total_bytes=$4 AND downloaded_bytes=$4 AND finished_at IS NOT NULL AND organization_state='review_required' AND organization_reason='dedicated full-set organization not implemented' AND transfer_plan_id IS NULL)) FROM download_task WHERE id=$1`, task, state, reason, snapshot.TotalBytes).Scan(&consistent); err != nil {
		return nil, err
	}
	if !consistent {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if state != "completed" {
		mutableAfter, e := trackingMutableFields(ctx, tx, task)
		if e != nil {
			return nil, e
		}
		if mutableAfter != mutableBefore {
			return nil, ports.ErrCompleteTVObservationStale
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_log WHERE id=$1 AND payload=$2::jsonb AND action='complete_tv.tracking_observed' AND target_id=$3) AND EXISTS(SELECT 1 FROM outbox_event WHERE id=$4 AND payload=$5::jsonb AND event_type='complete_tv.tracking_observed')`, auditID, safe, task.String(), event.ID, event.Payload).Scan(&consistent); err != nil {
		return nil, err
	}
	if !consistent {
		return nil, ports.ErrCompleteTVObservationStale
	}
	actualJob, e := q.GetJobByID(ctx, lease.ID)
	if e != nil {
		return nil, e
	}
	if coverageDigest(actualJob) != coverageDigest(finished) {
		return nil, ports.ErrCompleteTVObservationStale
	}
	// All waits, including final receipt reads, precede this final lease/permit check.
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, deadline).Scan(&consistent); err != nil {
		return nil, err
	}
	if !consistent {
		return nil, ports.ErrCompleteTVObservationStale
	}
	permit, err = completeTVPhysicalPermit(ctx, tx, snapshot.Request.OperationID)
	if err != nil {
		return nil, err
	}
	if permit != snapshot.PhysicalPermit {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, deadline).Scan(&consistent); err != nil {
		return nil, err
	}
	if !consistent {
		return nil, ports.ErrCompleteTVObservationStale
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// lockTracking follows account -> frozen UUID units -> operation -> task ->
// job -> protections -> client. Account activity and current selection/source
// never authorize or negate recovery. No original send lease is consulted.
func (s *CompleteTVTrackingStore) lockTracking(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest) (*ports.CompleteTVTrackingSnapshot, string, error) {
	stale := ports.ErrCompleteTVObservationStale
	if r.TaskID == uuid.Nil || r.OperationID == uuid.Nil || r.Lease.ID == uuid.Nil || r.Lease.Owner == "" {
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
	if _, err = q.LockJobForWrite(ctx, r.Lease.ID); err != nil {
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
	if json.Unmarshal(prepared.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || prepared.TaskID != task.ID || prepared.JobID == r.Lease.ID || prepared.LeaseOwner == "" || prepared.InfoHash != task.InfoHash || len(task.InfoHash) != 40 || !bytes.Equal(prepared.TorrentSha256, proof.TorrentSHA256[:]) || proof.InfoHash != task.InfoHash || proof.SavePath == "" || proof.SavePath != task.SavePath || proof.MaterialStamp != frozen.MaterialStamp || proof.TotalBytes <= 0 || proof.TotalBytes != frozen.CandidateSize || len(proof.Files) != len(units) {
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
		if f.Unit != u || p.UnitID != u.UnitID || p.UnitVersion != u.UnitVersion || p.ProviderEpisodeID != u.EpisodeID || p.UnitKey != u.UnitKey || p.VideoPath != f.Path || p.VideoSize != f.Size || d.UnitID != u.UnitID || d.RootID != frozen.RootID || d.ActorID != frozen.ActorID.String() || d.ActorType != "user" || d.State != "pending" || completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(fence) || l.UnitID != u.UnitID || l.AcquisitionLane != "default" || l.LegacyConflict || l.SourceRootID != frozen.RootID || l.SourceGeneration != frozen.RootGeneration || l.SourceStamp != frozen.SourceStamp || l.CandidateID != frozen.CandidateID || !l.TaskID.Valid || uuid.UUID(l.TaskID.Bytes) != task.ID || v.UnitID != u.UnitID || v.RootID != frozen.RootID || v.CandidateID != frozen.CandidateID || v.State != "dispatched" || v.Purpose != "manual_acquisition" {
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
	// Only now, after EVERY wait, validate the fresh observer lease and payload.
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM job WHERE id=$1 AND kind=$2 AND state='running' AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp()`, r.Lease.ID, ports.CompleteTVTrackingJobKind, r.Lease.Owner, r.Lease.Token).Scan(&payload)
	if err != nil {
		return nil, "", err
	}
	var binding struct {
		TaskID      uuid.UUID `json:"task_id"`
		OperationID uuid.UUID `json:"operation_id"`
	}
	if json.Unmarshal(payload, &binding) != nil || binding.TaskID != task.ID || binding.OperationID != r.OperationID {
		return nil, "", stale
	}
	protection, err := trackingProtection(ctx, tx, r)
	if err != nil {
		return nil, "", err
	}
	permit, err := completeTVPhysicalPermit(ctx, tx, r.OperationID)
	if err != nil {
		return nil, "", err
	}
	if permit == uuid.Nil {
		return nil, "", stale
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND kind=$2 AND state='running' AND lease_owner=$3 AND lease_token=$4 AND lease_until>clock_timestamp())`, r.Lease.ID, ports.CompleteTVTrackingJobKind, r.Lease.Owner, r.Lease.Token).Scan(&valid); err != nil {
		return nil, "", err
	}
	if !valid {
		return nil, "", stale
	}
	seal := coverageDigest([]any{"complete-tv.tracking.v1", r, record, prepared, task, stamp, protection})
	return &ports.CompleteTVTrackingSnapshot{Request: r, ClientID: task.ClientID, ClientStamp: stamp, InfoHash: task.InfoHash, OperationTag: "pulsemedia:op:" + r.OperationID.String(), SavePath: proof.SavePath, TotalBytes: proof.TotalBytes, Seal: seal, PhysicalPermit: permit}, protection, nil
}

// Canonical JSONB captures ALL row fields and exact membership. No secrets are
// returned: only the digest leaves the transaction. Include evidence and client
// identity so late triggers cannot change them while producing a valid result.
func trackingProtection(ctx context.Context, tx pgx.Tx, r ports.CompleteTVTrackingRequest) (string, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x WHERE operation_id=$1),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x WHERE operation_id=$1),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x WHERE operation_id=$1),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id) FROM complete_tv_submission_unit x WHERE task_id=$2),
 (SELECT to_jsonb(x) FROM complete_tv_submission_preparation x WHERE operation_id=$1),
 (SELECT to_jsonb(x) FROM complete_tv_execution_authority x WHERE operation_id=$1),
 (SELECT to_jsonb(x)-'state'-'error'-'updated_at'-'progress'-'total_bytes'-'downloaded_bytes'-'finished_at'-'organization_state'-'organization_reason' FROM download_task x WHERE id=$2),
 (SELECT to_jsonb(x) FROM download_client x WHERE id=(SELECT client_id FROM download_task WHERE id=$2))
 )`, r.OperationID, r.TaskID).Scan(&raw)
	if err != nil {
		return "", err
	}
	return completeTVJSONDigest(raw), nil
}

// Full-set completion is deliberately stricter than identity recovery.
func trackingMutableFields(ctx context.Context, tx pgx.Tx, task uuid.UUID) (string, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_array(progress,total_bytes,downloaded_bytes,finished_at,organization_state,organization_reason) FROM download_task WHERE id=$1`, task).Scan(&raw)
	if err != nil {
		return "", err
	}
	return completeTVJSONDigest(raw), nil
}

func trackingIdentity(rows []*ports.TorrentInfo, s ports.CompleteTVTrackingSnapshot) bool {
	if len(rows) != 1 || rows[0] == nil {
		return false
	}
	r := rows[0]
	tag := false
	for _, t := range strings.Split(r.Tags, ",") {
		if strings.TrimSpace(t) == s.OperationTag {
			tag = true
		}
	}
	return r.InfoHash == s.InfoHash && r.SavePath == s.SavePath && r.TotalBytes == s.TotalBytes && tag
}
func trackingCompleted(r *ports.TorrentInfo) bool {
	if r == nil || r.TotalBytes <= 0 || r.Progress != 1 || r.DownloadedBytes < r.TotalBytes {
		return false
	}
	switch r.State {
	case "uploading", "stalledUP", "pausedUP", "stoppedUP":
		return true
	}
	return false
}

// Exported original info bytes (not outer tracker metadata) bind every file,
// including zero-byte attachments, to the immutable preparation's v1 infohash.
func trackingFiles(p completeTVPreparedProof, metainfo *ports.CompleteTVMetainfoProof, files []ports.TorrentFile) bool {
	if !metainfo.ValidateBinding() || metainfo.InfoHash() != p.InfoHash || metainfo.TotalBytes() != p.TotalBytes {
		return false
	}
	full := metainfo.Files()
	if len(files) != len(full) || len(files) < 2 {
		return false
	}
	expected := map[string]int64{}
	for _, f := range full {
		if _, exists := expected[f.Path]; exists {
			return false
		}
		expected[f.Path] = f.Size
	}
	for _, f := range p.Files {
		size, exists := expected[f.Path]
		if !exists || size != f.Size || f.Unit.UnitID == uuid.Nil || f.Unit.UnitVersion < 1 {
			return false
		}
	}
	seen := map[int32]bool{}
	for _, f := range files {
		size, exists := expected[f.Name]
		if !exists || f.Index < 0 || int(f.Index) >= len(files) || seen[f.Index] || f.Progress != 1 || f.SizeBytes != size {
			return false
		}
		seen[f.Index] = true
		delete(expected, f.Name)
	}
	return len(expected) == 0
}
func trackingEvidenceStamp(r ports.CompleteTVTrackingObservation) string {
	if r.Metainfo == nil {
		return coverageDigest(r)
	}
	return coverageDigest([]any{r, r.Metainfo.SHA256(), r.Metainfo.InfoHash(), r.Metainfo.TotalBytes(), r.Metainfo.Files()})
}
