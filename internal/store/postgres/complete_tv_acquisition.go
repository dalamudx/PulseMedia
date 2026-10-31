package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	sd "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const completeTVJobKind = "complete_tv_coverage_download_submission"

// CompleteTVAcquisitionStore atomically protects the whole target set and queues
// dedicated submission work. HTTP commands never perform downloader I/O.
type CompleteTVAcquisitionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCompleteTVAcquisitionStore(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVAcquisitionStore {
	return &CompleteTVAcquisitionStore{pool, ids}
}

var _ ports.CompleteTVCoverageAcquisitionRepository = (*CompleteTVAcquisitionStore)(nil)

// PostgreSQL JSONB reorders object keys; compare JSON values without losing integer precision.
func completeTVJSONDigest(raw []byte) string {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "invalid"
	}
	return coverageDigest(value)
}

func completeTVStamp(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func completeTVFacts(units []sd.UnitInput, own map[uuid.UUID]bool) json.RawMessage {
	copyUnits := append([]sd.UnitInput(nil), units...)
	for i := range copyUnits {
		if own[copyUnits[i].UnitID] {
			// Migration 21: one unit-reservation insert; migration 25: one lane
			// insert. Normalize exactly these two owned version bumps, preserving
			// every other fact-version change as a conflict.
			copyUnits[i].FactsVersion -= 2
			copyUnits[i].Reserved = false
			copyUnits[i].ReservationUnknown = false
			copyUnits[i].SharedAcquisition = false
		}
	}
	raw, _ := json.Marshal(struct {
		Units any `json:"units"`
	}{copyUnits})
	return raw
}
func completeTVEligible(v *ports.EpisodeCandidateObservation, rows []media.NumberingRow, own map[uuid.UUID]bool, now time.Time) bool {
	target := v.Target
	target.ObservedAt = now
	target.Units = append([]sd.UnitInput(nil), target.Units...)
	for i := range target.Units {
		if own[target.Units[i].UnitID] {
			target.Units[i].Reserved = false
			target.Units[i].ReservationUnknown = false
			target.Units[i].SharedAcquisition = false
		}
	}
	eligible := map[uuid.UUID]bool{}
	for _, u := range sd.DeriveProjection(target).Items {
		eligible[u.UnitID] = !u.IsSatisfied && !u.IsSkipped && (u.WaitReason == "missing" || u.WaitReason == "file_rejected" || u.WaitReason == "no_qualified_candidate")
	}
	for _, row := range rows {
		if !eligible[row.UnitID] {
			return false
		}
		found := false
		for _, u := range target.Units {
			if u.UnitID == row.UnitID {
				found = u.AirDate != nil && !u.AirDate.After(now) && u.MetadataVersion == row.UnitVersion && !u.Retired && !u.Skipped
			}
		}
		if !found {
			return false
		}
	}
	return v.Current && v.Mapping.Current && v.Mapping.Mode == "standard" && target.State == "active" && target.PolicyAvailable && target.DownloaderAvailable && quality.EvaluateCandidate(target.Rules, v.Name, v.Quality).Passed
}

func (s *CompleteTVAcquisitionStore) AcquireCompleteTVCoverage(ctx context.Context, a *ports.Actor, c ports.AcquireCompleteTVCoverageCommand, key string) (out *ports.AcquireResult, err error) {
	conflict := ports.ErrCompleteTVCoverageAcquisitionConflict
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !c.Confirmed || c.SubscriptionID == uuid.Nil || c.ReviewID == uuid.Nil || !completeTVStamp(c.ExpectedInputStamp) || !completeTVStamp(c.ExpectedMaterialStamp) || key == "" || strings.TrimSpace(key) != key || len(key) > 130 {
		return nil, conflict
	}
	defer func() {
		if err != nil && !errors.Is(err, conflict) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrAcquisitionUnitAlreadyReserved) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	hash := coverageDigest([]any{"complete-tv.acquire.v1", c})
	key = "complete-tv.acquire:" + key
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	actorID := uuid.MustParse(a.ID)
	// Account lock precedes receipt and every policy/root/unit wait; rechecks real role/activity.
	if e = coverageAccess(ctx, q, a, c.SubscriptionID); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "complete-tv.acquisition.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	if old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key}); e == nil {
		var result ports.AcquireResult
		if old.RequestHash != hash || old.ResponseCode != 202 || json.Unmarshal(old.ResponseBody, &result) != nil || result.OperationID == uuid.Nil {
			return nil, conflict
		}
		authority, e := q.GetCompleteTVExecutionAuthority(ctx, result.OperationID)
		if e != nil {
			return nil, e
		}
		var snap ports.CompleteTVCoverageExecutionSnapshot
		if json.Unmarshal(authority.Snapshot, &snap) != nil || authority.ActorID != actorID || authority.SubscriptionID != c.SubscriptionID || authority.ReviewID != c.ReviewID || snap.ObservationStamp != c.ExpectedInputStamp || snap.MaterialStamp != c.ExpectedMaterialStamp {
			return nil, conflict
		}
		return &result, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	hint, e := q.GetSubscriptionByID(ctx, c.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockSearchPolicy(ctx, hint.QualityPolicyID); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.ID)
	if e != nil {
		return nil, e
	}
	if sub.UserID != actorID || sub.State != "active" || !sub.DownloaderID.Valid || sub.MediaID != hint.MediaID || sub.RootID != hint.RootID || sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, conflict
	}
	root, e := q.LockAcquisitionRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	if root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted {
		return nil, conflict
	}
	// Same sorted full-media unit locking contract as AcquireReviewedEpisode.
	if _, e = q.LockScopeMediaUnits(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	review, e := q.GetTVCoverageCandidateReview(ctx, c.ReviewID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, conflict
	}
	if e != nil {
		return nil, e
	}
	if review.ActorID != actorID || review.SubscriptionID != sub.ID || review.Status != "reviewed_not_executable" || review.MaterialStamp != c.ExpectedMaterialStamp {
		return nil, conflict
	}
	if _, e = q.LockSubmissionMedia(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	cand, e := q.GetCandidateByID(ctx, review.CandidateID)
	if e != nil {
		return nil, e
	}
	idx, e := q.LockSubmissionIndexer(ctx, cand.IndexerID)
	if e != nil {
		return nil, e
	}
	cand, e = q.LockSubmissionCandidate(ctx, cand.ID)
	if e != nil {
		return nil, e
	}
	clientID := uuid.UUID(sub.DownloaderID.Bytes)
	if _, e = tx.Exec(ctx, "SELECT id FROM download_client WHERE id=$1 FOR SHARE", clientID); e != nil {
		return nil, e
	}
	client, e := q.GetDownloadClientByID(ctx, clientID)
	if e != nil {
		return nil, e
	}
	if (client.ClientType != "qbittorrent" && client.ClientType != "transmission") || strings.TrimSpace(client.Url) == "" || len(client.PasswordEncrypted) == 0 || cand.SizeBytes <= 0 || idx.ID != cand.IndexerID {
		return nil, conflict
	}
	observed, e := readTVCoverageCandidate(ctx, q, a, sub.ID, cand.ID)
	if e != nil {
		return nil, e
	}
	var reviewedRows []media.NumberingRow
	if json.Unmarshal(review.ResolvedUnits, &reviewedRows) != nil || !observed.CanReview || observed.MaterialStamp != review.MaterialStamp || coverageDigest(observed.Rows) != coverageDigest(reviewedRows) || completeTVJSONDigest(review.Material) != completeTVJSONDigest(observed.Material) {
		return nil, fmt.Errorf("review material: %w", conflict)
	}
	rows := append([]media.NumberingRow(nil), observed.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitID.String() < rows[j].UnitID.String() })
	unitIDs := make([]uuid.UUID, 0, len(rows))
	own := map[uuid.UUID]bool{}
	var operation uuid.UUID
	lanes := 0
	for _, row := range rows {
		unitIDs = append(unitIDs, row.UnitID)
		lane, e := q.LockActiveAcquisitionLane(ctx, row.UnitID)
		if e == nil {
			lanes++
			if lane.LegacyConflict || lane.CandidateID != cand.ID || lane.SourceRootID != root.ID || lane.SourceGeneration != root.Generation || (operation != uuid.Nil && operation != lane.OperationID) {
				return nil, ports.ErrAcquisitionUnitAlreadyReserved
			}
			operation = lane.OperationID
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	if lanes != 0 && lanes != len(rows) {
		return nil, ports.ErrAcquisitionUnitAlreadyReserved
	}
	var snapshot ports.CompleteTVCoverageExecutionSnapshot
	reused := lanes == len(rows)
	if !reused {
		operation = s.ids.NewUUID()
	}
	if e = q.LockDownloadAcquisition(ctx, operation); e != nil {
		return nil, e
	}
	if reused {
		authority, e := q.GetCompleteTVExecutionAuthority(ctx, operation)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrAcquisitionUnitAlreadyReserved
		}
		if e != nil {
			return nil, e
		}
		if json.Unmarshal(authority.Snapshot, &snapshot) != nil || snapshot.Kind != "reviewed_complete_tv_coverage_v1" || authority.ActorID != actorID || authority.ReviewID != review.ID || authority.SubscriptionID != sub.ID || authority.RootID != root.ID || authority.ClientID != clientID || snapshot.MaterialStamp != observed.MaterialStamp || snapshot.ObservationStamp != c.ExpectedInputStamp || coverageDigest(snapshot.Units) != coverageDigest(rows) || snapshot.ClientStamp != episodeClientStamp(client) {
			return nil, ports.ErrAcquisitionUnitAlreadyReserved
		}
		// Reuse only an exact, complete, still-unsent authority. Any physical task,
		// including unknown/expired states, remains protected and cannot be reauthorized.
		var tasks, liveLanes, reservations, requirements int
		e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM download_task WHERE operation_id=$1),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND root_id=$2 AND state IN ('reserved','dispatched')),(SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND root_id=$2 AND actor_id=$3 AND actor_type='user' AND state='pending')`, operation, root.ID, a.ID).Scan(&tasks, &liveLanes, &reservations, &requirements)
		if e != nil {
			return nil, e
		}
		if tasks != 0 || liveLanes != len(rows) || reservations != len(rows) || requirements != len(rows) {
			return nil, fmt.Errorf("reuse counts: %w", conflict)
		}
		requirementsRows, e := q.ListRequirementsForOperation(ctx, operation)
		if e != nil {
			return nil, e
		}
		if len(requirementsRows) != len(rows) {
			return nil, conflict
		}
		for _, r := range rows {
			found := false
			for _, d := range requirementsRows {
				if d.UnitID == r.UnitID && d.RootID == root.ID && d.ActorID == a.ID && d.ActorType == string(a.Type) && d.State == "pending" && completeTVJSONDigest(d.SubmissionFence) == completeTVJSONDigest(completeTVFence(a, sub, root.Generation, cand, r, snapshot.SourceStamp, snapshot.MediaVersion)) {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("reuse fence: %w", conflict)
			}
			own[r.UnitID] = true
		}
	} else if observed.InputStamp != c.ExpectedInputStamp {
		return nil, conflict
	}
	v, e := readEpisodeCandidate(ctx, q, a, sub.ID, cand.ID)
	if e != nil {
		return nil, e
	}
	var now time.Time
	if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return nil, e
	}
	if !completeTVEligible(v, rows, own, now) {
		return nil, fmt.Errorf("initial eligibility: %w", conflict)
	}
	for _, row := range rows {
		files, e := q.ListPotentialReusableFiles(ctx, db.ListPotentialReusableFilesParams{UnitID: row.UnitID, RootID: root.ID})
		if e != nil {
			return nil, e
		}
		if len(files) > 0 {
			return nil, conflict
		}
	}
	facts := completeTVFacts(v.Target.Units, own)
	if reused && completeTVJSONDigest(facts) != completeTVJSONDigest(snapshot.Facts) {
		return nil, fmt.Errorf("reuse facts: %w", conflict)
	}
	if !reused {
		source, e := readEpisodeSourceContext(ctx, q, a, sub.ID)
		if e != nil {
			return nil, e
		}
		mapping := v.Mapping
		mapping.ObservedAt = time.Time{}
		snapshot = ports.CompleteTVCoverageExecutionSnapshot{Kind: "reviewed_complete_tv_coverage_v1", ActorID: actorID, ReviewID: review.ID, ReviewInputStamp: review.InputStamp, ObservationStamp: observed.InputStamp, MaterialStamp: observed.MaterialStamp, Material: observed.Material, Facts: facts, Units: rows, CompleteSeasons: source.CompleteSeasons, SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion, MediaID: sub.MediaID, MediaVersion: source.MediaVersion, Mapping: mapping, PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, RootID: root.ID, RootGeneration: root.Generation, CandidateID: cand.ID, CandidateSize: cand.SizeBytes, SourceStamp: quality.AcquisitionSourceStamp(cand.Title, cand.InfoHash.String, cand.LinkEncrypted, cand.QualityProfile), ClientID: clientID, ClientStamp: episodeClientStamp(client), AcquiredAt: now}
		raw, _ := json.Marshal(snapshot)
		if e = q.CreateCompleteTVExecutionAuthority(ctx, db.CreateCompleteTVExecutionAuthorityParams{OperationID: operation, ReviewID: review.ID, SubscriptionID: sub.ID, CandidateID: cand.ID, RootID: root.ID, ClientID: clientID, ActorID: actorID, Snapshot: raw}); e != nil {
			return nil, e
		}
		out = &ports.AcquireResult{OperationID: operation, Status: "queued", SourceCandidateID: cand.ID}
		for _, row := range rows {
			if _, e = q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: s.ids.NewUUID(), UnitID: row.UnitID, OperationID: operation, CandidateID: cand.ID, SourceRootID: root.ID, SourceGeneration: root.Generation, SourceStamp: snapshot.SourceStamp}); e != nil {
				return nil, e
			}
			id := s.ids.NewUUID()
			if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: id, UnitID: row.UnitID, RootID: root.ID, Purpose: "manual_acquisition", CandidateID: cand.ID, OperationID: operation, ExpiresAt: pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true}}); e != nil {
				return nil, e
			}
			out.Reservations = append(out.Reservations, id)
			if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: s.ids.NewUUID(), OperationID: operation, UnitID: row.UnitID, RootID: root.ID, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: completeTVFence(a, sub, root.Generation, cand, row, snapshot.SourceStamp, snapshot.MediaVersion)}); e != nil {
				return nil, e
			}
			own[row.UnitID] = true
		}
		out.JobID = s.ids.NewUUID()
		payload, _ := json.Marshal(map[string]any{"operation_id": operation, "candidate_id": cand.ID, "root_id": root.ID, "root_generation": root.Generation, "client_id": clientID, "unit_ids": unitIDs, "complete_tv_authority": operation, "material_stamp": snapshot.MaterialStamp, "observation_stamp": snapshot.ObservationStamp})
		if _, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: out.JobID, Kind: completeTVJobKind, DedupeKey: pgtype.Text{String: operation.String(), Valid: true}, Priority: 10, Payload: payload, MaxAttempts: 3, NotBefore: pgtype.Timestamptz{Time: now, Valid: true}}); e != nil {
			return nil, e
		}
	} else {
		out = &ports.AcquireResult{OperationID: operation, Status: "queued", SourceCandidateID: cand.ID}
		e = tx.QueryRow(ctx, "SELECT id FROM job WHERE kind=$1 AND payload->>'operation_id'=$2 AND state='queued'", completeTVJobKind, operation.String()).Scan(&out.JobID)
		if e != nil {
			return nil, conflict
		}
		resultRows, e := tx.Query(ctx, "SELECT id FROM unit_reservation WHERE operation_id=$1 AND root_id=$2 AND state IN ('reserved','dispatched') ORDER BY unit_id", operation, root.ID)
		if e != nil {
			return nil, e
		}
		for resultRows.Next() {
			var id uuid.UUID
			if e = resultRows.Scan(&id); e != nil {
				resultRows.Close()
				return nil, e
			}
			out.Reservations = append(out.Reservations, id)
		}
		e = resultRows.Err()
		resultRows.Close()
		if e != nil {
			return nil, e
		}
	}
	receipt, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "complete_tv.acquisition.reserve", TargetType: "subscription", TargetID: sub.ID.String(), Payload: receipt}); e != nil {
		return nil, e
	}
	if _, e = (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", operation.String(), "complete_tv.acquisition.reserved", map[string]any{"operation_id": operation, "candidate_id": cand.ID, "unit_ids": unitIDs}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 202, ResponseBody: receipt, ResourceID: pgtype.Text{String: operation.String(), Valid: true}}); e != nil {
		return nil, e
	}
	// Freshness after every potentially blocking late write, using current database
	// time for expiring facts as well as source observations. Ignore only our exact
	// protected rows, already verified above, never arbitrary competing authority.
	final, e := readTVCoverageCandidate(ctx, q, a, sub.ID, cand.ID)
	if e != nil {
		return nil, e
	}
	if !final.CanReview || final.MaterialStamp != observed.MaterialStamp || coverageDigest(final.Rows) != coverageDigest(observed.Rows) {
		return nil, fmt.Errorf("final material: %w", conflict)
	}
	if e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil {
		return nil, e
	}
	fresh, e := readSubscriptionStatus(ctx, q, a, sub.ID, &now)
	if e != nil {
		return nil, e
	}
	v.Target = fresh
	if !completeTVEligible(v, rows, own, now) || completeTVJSONDigest(completeTVFacts(fresh.Units, own)) != completeTVJSONDigest(facts) || (v.Mapping.ValidUntil != nil && !v.Mapping.ValidUntil.After(now)) {
		return nil, fmt.Errorf("final facts: %w", conflict)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func completeTVFence(a *ports.Actor, sub db.Subscription, generation int64, cand db.ReleaseCandidate, row media.NumberingRow, source string, mediaVersion int64) json.RawMessage {
	raw, _ := json.Marshal(ports.DownloadDemandFence{PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, SubscriptionID: &sub.ID, IntentVersion: sub.IntentVersion, RootGeneration: generation, UnitMetadataVersion: row.UnitVersion, MediaMetadataVersion: mediaVersion, CandidateID: cand.ID, RootID: sub.RootID, UnitID: row.UnitID, MediaID: sub.MediaID, ActorID: a.ID, ActorType: a.Type, SourceStamp: source})
	return raw
}
