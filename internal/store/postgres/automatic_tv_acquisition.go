package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AutomaticTVAcquisitionStore struct {
	pool     *pgxpool.Pool
	ids      ports.IDGen
	selector ports.AutomaticTVSelector
}

func NewAutomaticTVAcquisitionStore(pool *pgxpool.Pool, ids ports.IDGen, selector ports.AutomaticTVSelector) *AutomaticTVAcquisitionStore {
	return &AutomaticTVAcquisitionStore{pool: pool, ids: ids, selector: selector}
}

var _ ports.AutomaticTVAcquisitionRepository = (*AutomaticTVAcquisitionStore)(nil)

// Recovery owns no external I/O. It reuses the original job and fences every
// previous claimant; physical source permits remain owned by their source jobs.
func (s *AutomaticTVAcquisitionStore) RecoverAutomaticTVFinalizers(ctx context.Context, limit int32) (n int, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if s == nil || s.pool == nil || s.ids == nil {
		return 0, ports.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, ports.ErrRejected
	}
	rows, e := s.pool.Query(ctx, `SELECT r.id FROM automatic_tv_round r JOIN job j ON j.id=r.finalize_job_id WHERE r.state='running' AND j.kind='automatic_tv_finalize' AND j.state='running' AND j.lease_until<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM automatic_tv_decision d WHERE d.round_id=r.id) ORDER BY r.deadline,r.id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, id := range ids {
		changed, e := s.recoverAutomaticTVFinalizer(ctx, id)
		if e != nil {
			return n, e
		}
		if changed {
			n++
		}
	}
	return n, nil
}

func (s *AutomaticTVAcquisitionStore) recoverAutomaticTVFinalizer(ctx context.Context, id uuid.UUID) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	hint, e := automaticTVDecodeRound(q.AutomaticTVReadRound(ctx, id))
	if e != nil {
		return false, e
	}
	owner, e := q.LockAcquisitionCommandUser(ctx, hint.OwnerID)
	if e != nil {
		return false, e
	}
	if _, e = q.LockJobForWrite(ctx, hint.FinalizeJobID); e != nil {
		return false, e
	}
	job, e := q.GetJobByID(ctx, hint.FinalizeJobID)
	if e != nil {
		return false, e
	}
	var payload map[string]json.RawMessage
	var jobRound uuid.UUID
	if job.Kind != "automatic_tv_finalize" || !job.DedupeKey.Valid || job.DedupeKey.String != id.String() || json.Unmarshal(job.Payload, &payload) != nil || len(payload) != 1 || json.Unmarshal(payload["round_id"], &jobRound) != nil || jobRound != id {
		return false, ports.ErrAcquisitionRoundAborted
	}
	policy, e := q.LockSearchPolicy(ctx, hint.Snapshot.PolicyID)
	if e != nil {
		return false, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.SubscriptionID)
	if e != nil {
		return false, e
	}
	r, e := automaticTVDecodeRound(q.AutomaticTVLockRound(ctx, id))
	if e != nil {
		return false, e
	}
	var eligible bool
	e = tx.QueryRow(ctx, `SELECT j.state='running' AND j.lease_owner IS NOT NULL AND j.lease_token>0 AND j.lease_until<=clock_timestamp() AND r.state='running' AND NOT EXISTS(SELECT 1 FROM automatic_tv_decision d WHERE d.round_id=r.id) FROM job j JOIN automatic_tv_round r ON r.finalize_job_id=j.id WHERE r.id=$1`, id).Scan(&eligible)
	if e != nil {
		return false, e
	}
	if !eligible {
		return false, nil
	}
	reason := ""
	switch {
	case !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) || sub.UserID != r.OwnerID:
		reason = "owner_revoked"
	case sub.State != "active" || sub.IntentVersion != r.Snapshot.IntentVersion || sub.QualityPolicyID != r.Snapshot.PolicyID || sub.QualityPolicyRevision != r.Snapshot.PolicyRevision || policy.CurrentRevision != r.Snapshot.PolicyRevision:
		reason = "target_changed"
	case job.Attempts >= job.MaxAttempts:
		reason = "retry_exhausted"
	}
	if reason == "" {
		tag, e := tx.Exec(ctx, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL,lease_token=lease_token+1,not_before=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state='running' AND lease_token=$2 AND lease_owner=$3 AND lease_until<=clock_timestamp()`, job.ID, job.LeaseToken, job.LeaseOwner.String)
		if e != nil {
			return false, e
		}
		if tag.RowsAffected() != 1 {
			return false, ports.ErrAcquisitionRoundAborted
		}
	} else {
		d := ports.RoundDecision{RoundID: id, State: "closed", BestScore: -1, Reason: reason}
		raw, e := json.Marshal(d)
		if e != nil {
			return false, e
		}
		if e = q.AutomaticTVInsertDecision(ctx, db.AutomaticTVInsertDecisionParams{RoundID: id, FinalizeJobID: job.ID, LeaseOwner: job.LeaseOwner.String, LeaseToken: job.LeaseToken + 1, State: "closed", BestScore: -1, Decision: raw}); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `UPDATE automatic_tv_round SET state='closed' WHERE id=$1`, id); e != nil {
			return false, e
		}
		if e = q.RescheduleWantedEvaluation(ctx, db.RescheduleWantedEvaluationParams{ID: r.WantedID, DelayMs: 300000}); e != nil {
			return false, e
		}
		if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: id.String(), Action: "automatic_tv.finalized", TargetType: "automatic_tv_round", TargetID: id.String(), Payload: raw}); e != nil {
			return false, e
		}
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "automatic_tv_round", AggregateID: id.String()}); e != nil {
			return false, e
		}
		seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "automatic_tv_round", AggregateID: id.String()})
		if e != nil {
			return false, e
		}
		if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "automatic_tv_round", AggregateID: id.String(), Seq: int64(seq), EventType: "automatic_tv.finalized", Payload: raw}); e != nil {
			return false, e
		}
		tag, e := tx.Exec(ctx, `UPDATE job SET state='dead',lease_owner=NULL,lease_until=NULL,lease_token=lease_token+1,result=$4,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state='running' AND lease_token=$2 AND lease_owner=$3 AND lease_until<=clock_timestamp()`, job.ID, job.LeaseToken, job.LeaseOwner.String, raw)
		if e != nil {
			return false, e
		}
		if tag.RowsAffected() != 1 {
			return false, ports.ErrAcquisitionRoundAborted
		}
	}
	var fenced bool
	e = tx.QueryRow(ctx, `SELECT lease_token=$2 AND state=$3 AND lease_owner IS NULL AND lease_until IS NULL FROM job WHERE id=$1`, job.ID, job.LeaseToken+1, map[bool]string{true: "queued", false: "dead"}[reason == ""]).Scan(&fenced)
	if e != nil {
		return false, e
	}
	if !fenced {
		return false, ports.ErrAcquisitionRoundAborted
	}
	return true, tx.Commit(ctx)
}

func (s *AutomaticTVAcquisitionStore) FinalizeAutomaticTVRound(ctx context.Context, id uuid.UUID, lease ports.JobLease) (out *ports.RoundDecision, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrAcquisitionRoundNotReady) {
			err = automaticTVError(ctx, err)
		}
	}()
	if s == nil || s.pool == nil || s.ids == nil || s.selector == nil {
		return nil, ports.ErrUnavailable
	}
	if id == uuid.Nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	hint, e := automaticTVDecodeRound(q.AutomaticTVReadRound(ctx, id))
	if e != nil {
		return nil, e
	}
	if _, e = automaticTVLockOwner(ctx, q, hint.OwnerID); e != nil {
		return nil, e
	}
	if lease.ID != hint.FinalizeJobID {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	if _, e = q.LockJobForWrite(ctx, lease.ID); e != nil {
		return nil, e
	}
	// Validate after acquiring the job lock, not against the pre-wait snapshot.
	job, e := q.GetJobByID(ctx, lease.ID)
	if e != nil {
		return nil, e
	}
	var payload map[string]json.RawMessage
	var jobRound uuid.UUID
	if job.Kind != "automatic_tv_finalize" || job.LeaseToken != lease.Token ||
		!job.DedupeKey.Valid || job.DedupeKey.String != id.String() ||
		json.Unmarshal(job.Payload, &payload) != nil || len(payload) != 1 ||
		json.Unmarshal(payload["round_id"], &jobRound) != nil || jobRound != id {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	policy, e := q.LockSearchPolicy(ctx, hint.Snapshot.PolicyID)
	if e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.SubscriptionID)
	if e != nil {
		return nil, e
	}
	r, e := automaticTVDecodeRound(q.AutomaticTVLockRound(ctx, id))
	if e != nil {
		return nil, e
	}
	if sub.UserID != r.OwnerID {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	if r.State == "closed" {
		// Recovery is read-only and accepts only the exact original finishing token.
		raw, e := q.AutomaticTVReadDecision(ctx, db.AutomaticTVReadDecisionParams{RoundID: id, FinalizeJobID: lease.ID, LeaseOwner: lease.Owner, LeaseToken: lease.Token})
		if e != nil {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		var old ports.RoundDecision
		if job.State != "succeeded" || json.Unmarshal(raw, &old) != nil || old.RoundID != id {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		return &old, tx.Commit(ctx)
	}
	if _, e = q.GetAuthorizedSearchJob(ctx, db.GetAuthorizedSearchJobParams{ID: lease.ID, Kind: "automatic_tv_finalize", LeaseOwner: pgtype.Text{String: lease.Owner, Valid: true}, LeaseToken: lease.Token}); e != nil {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	ready, e := q.AutomaticTVFinalizationReady(ctx, id)
	if e != nil {
		return nil, e
	}
	if !ready.Valid || !ready.Bool {
		return nil, ports.ErrAcquisitionRoundNotReady
	}
	if sub.QualityPolicyID != r.Snapshot.PolicyID || policy.CurrentRevision != r.Snapshot.PolicyRevision {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	// Shares exactly Source87's root/unit/wanted/media/indexer/client lock order.
	snap, sources, e := automaticTVSnapshot(ctx, q, sub, r.WantedID, r.CreatedAt)
	if e != nil {
		return nil, e
	}
	_, hash, e := automaticTVSnapshotBytes(snap)
	if e != nil || hash != r.SnapshotHash {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	material, e := automaticTVMaterial(ctx, tx, q, r, sources)
	if e != nil {
		return nil, e
	}
	selected, e := automaticTVSelect(s.selector, material)
	if e != nil {
		return nil, e
	}
	decision := &ports.RoundDecision{RoundID: id, State: "closed", BestScore: -1, Reason: "no_qualified_candidate"}
	var chosen *ports.AutomaticTVRoundCandidate
	if selected.CandidateID != nil {
		for i := range material.Candidates {
			row := &material.Candidates[i]
			if row.Candidate.ID == *selected.CandidateID {
				score, valid := automaticTVEligible(r.Snapshot, *row)
				if !valid || selected.Score != score || selected.Reason != "selected" {
					return nil, ports.ErrAcquisitionRoundAborted
				}
				chosen = row
				break
			}
		}
		if chosen == nil {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		candidate := chosen.Candidate.ID
		decision.OperationID, decision.SelectedCandidateID, decision.State = s.ids.NewUUID(), &candidate, "reserved"
		decision.BestScore, decision.Reason = selected.Score, "selected"
	} else if len(material.Candidates) != 0 || selected.Score != -1 || selected.Reason != "no_qualified_candidate" {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	// Recheck current target/source/configuration after the callback, before writes.
	current, _, e := automaticTVSnapshot(ctx, q, sub, r.WantedID, r.CreatedAt)
	if e != nil {
		return nil, e
	}
	_, hash, e = automaticTVSnapshotBytes(current)
	if e != nil || hash != r.SnapshotHash {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if chosen != nil {
		fresh, e := q.LockSubmissionCandidate(ctx, chosen.Candidate.ID)
		if e != nil {
			return nil, e
		}
		if !automaticTVSameCandidate(chosen.Candidate, fresh) {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		if e = s.reserveAutomaticTV(ctx, q, r, *chosen, decision.OperationID, now); e != nil {
			return nil, e
		}
	}
	receipt, e := json.Marshal(decision)
	if e != nil {
		return nil, e
	}
	if e = q.AutomaticTVInsertDecision(ctx, db.AutomaticTVInsertDecisionParams{RoundID: id, FinalizeJobID: lease.ID, LeaseOwner: lease.Owner, LeaseToken: lease.Token, OperationID: pgtype.UUID{Bytes: decision.OperationID, Valid: decision.OperationID != uuid.Nil}, CandidateID: searchPGUUIDValue(decision.SelectedCandidateID), State: decision.State, BestScore: int32(decision.BestScore), Decision: receipt}); e != nil {
		return nil, e
	}
	tag, e := tx.Exec(ctx, `UPDATE automatic_tv_round SET state='closed' WHERE id=$1 AND state='running'`, id)
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() != 1 {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	// Invalidate/retry the derived view without changing any existing protection.
	if e = q.RescheduleWantedEvaluation(ctx, db.RescheduleWantedEvaluationParams{ID: r.WantedID, DelayMs: 300000}); e != nil {
		return nil, e
	}
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: id.String(), Action: "automatic_tv.finalized", TargetType: "automatic_tv_round", TargetID: id.String(), Payload: receipt}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "automatic_tv_round", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "automatic_tv_round", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "automatic_tv_round", AggregateID: id.String(), Seq: int64(seq), EventType: "automatic_tv.finalized", Payload: receipt}); e != nil {
		return nil, e
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: lease.ID, Column2: lease.Owner, Column3: lease.Token, Column4: receipt}); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		return nil, e
	}
	// A trigger on any tail write (including FinishJob) may consume the lease.
	tail, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if !job.LeaseUntil.Valid || !tail.Time.Before(job.LeaseUntil.Time) {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return decision, nil
}

func searchPGUUIDValue(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func automaticTVSameCandidate(c ports.SearchCandidate, row db.ReleaseCandidate) bool {
	return c.ID == row.ID && c.IndexerID == row.IndexerID && c.SourceConfigVersion == row.SourceConfigVersion &&
		c.Title == row.Title && c.Guid == row.Guid && c.SizeBytes == row.SizeBytes &&
		(c.InfoHash != nil) == row.InfoHash.Valid && (c.InfoHash == nil || *c.InfoHash == row.InfoHash.String) &&
		bytes.Equal(c.LinkEncrypted, row.LinkEncrypted) && automaticTVSourceStamp(c) == automaticTVSourceStamp(automaticTVSearchCandidate(row))
}

func automaticTVSearchCandidate(c db.ReleaseCandidate) ports.SearchCandidate {
	out := ports.SearchCandidate{ID: c.ID, IndexerID: c.IndexerID, SourceConfigVersion: c.SourceConfigVersion, Title: c.Title, Guid: c.Guid, SizeBytes: c.SizeBytes, Seeders: c.Seeders, Leechers: c.Leechers, LinkEncrypted: append([]byte(nil), c.LinkEncrypted...), QualityProfile: append(json.RawMessage(nil), c.QualityProfile...)}
	if c.InfoHash.Valid {
		v := c.InfoHash.String
		out.InfoHash = &v
	}
	if c.PublishDate.Valid {
		v := c.PublishDate.Time
		out.PublishDate = &v
	}
	return out
}

func automaticTVMaterial(ctx context.Context, tx pgx.Tx, q *db.Queries, r *automaticTVRoundRow, queries map[uuid.UUID]ports.TVEpisodeQuery) (ports.AutomaticTVSelectionMaterial, error) {
	out := ports.AutomaticTVSelectionMaterial{Snapshot: r.Snapshot, Candidates: []ports.AutomaticTVRoundCandidate{}}
	ids := make([]uuid.UUID, 0, len(queries))
	for id := range queries {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for _, id := range ids {
		src, e := automaticTVDecodeSource(q.AutomaticTVReadSource(ctx, db.AutomaticTVReadSourceParams{RoundID: r.ID, IndexerID: id}))
		if e != nil {
			return out, e
		}
		_, _, base, e := canonicalAutomaticTVQuery(queries[id])
		if e != nil || src.ParametersHash != base || src.ConfigVersion != r.Snapshot.IndexerVersions[id.String()] {
			return out, ports.ErrAcquisitionRoundAborted
		}
		pages, e := automaticTVPages(ctx, q, ports.AutomaticTVSourceTask{RoundID: r.ID, IndexerID: id})
		if e != nil {
			return out, e
		}
		for pos, p := range pages {
			expected := queries[id]
			expected.Offset = p.Page * 50
			_, _, hash, e := canonicalAutomaticTVQuery(expected)
			_, _, actual, parseErr := canonicalAutomaticTVQuery(p.Parameters)
			if e != nil || parseErr != nil || p.Page != int32(pos) || p.SourceJobID != src.JobID || p.SourceToken < 1 || hash != actual || hash != p.ParametersHash {
				return out, ports.ErrAcquisitionRoundAborted
			}
			query, e := q.GetQueryExecution(ctx, p.QueryID)
			if e != nil {
				return out, e
			}
			if query.State != "succeeded" {
				continue
			}
			proof, e := q.GetIndexerQueryPage(ctx, p.QueryID)
			if e != nil {
				return out, ports.ErrAcquisitionRoundAborted
			}
			if query.QueryType != "tv_episode" || query.IndexerID != id || query.ConfigVersion != src.ConfigVersion || query.RssJobID.Valid || query.Page != p.Page || query.Keyword != "tv-episode-query-v1:"+hash || !query.FinishedAt.Valid || query.FinishedAt.Time.After(r.Deadline) || proof.PageOffset != int64(p.Page)*50 || proof.PageLimit != 50 || proof.ItemCount != query.ResultsCount {
				return out, ports.ErrAcquisitionRoundAborted
			}
			rows, e := tx.Query(ctx, `SELECT c.id FROM release_candidate c JOIN automatic_tv_query_candidate f ON f.candidate_id=c.id JOIN indexer_query_candidate qc ON qc.query_id=f.query_id AND qc.candidate_id=f.candidate_id WHERE f.query_id=$1 AND c.indexer_id=$2 ORDER BY c.id FOR SHARE OF c`, p.QueryID, id)
			if e != nil {
				return out, e
			}
			var candidates []uuid.UUID
			for rows.Next() {
				var cid uuid.UUID
				if e = rows.Scan(&cid); e != nil {
					rows.Close()
					return out, e
				}
				candidates = append(candidates, cid)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return out, e
			}
			for _, cid := range candidates {
				c, e := q.GetCandidateByID(ctx, cid)
				if e != nil {
					return out, e
				}
				candidate := automaticTVSearchCandidate(c)
				var stamp string
				var same bool
				e = tx.QueryRow(ctx, `SELECT f.source_stamp, f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes AND f.source_config_version=c.source_config_version FROM automatic_tv_query_candidate f JOIN release_candidate c ON c.id=f.candidate_id WHERE f.query_id=$1 AND f.candidate_id=$2`, p.QueryID, cid).Scan(&stamp, &same)
				if e != nil {
					return out, e
				}
				if !same || stamp != automaticTVSourceStamp(candidate) {
					continue
				}
				row := ports.AutomaticTVRoundCandidate{Candidate: candidate, QueryID: p.QueryID, QueryParametersHash: hash, QuerySourceStamp: stamp}
				if _, ok := automaticTVEligible(r.Snapshot, row); ok {
					out.Candidates = append(out.Candidates, row)
				}
			}
		}
	}
	return out, nil
}

func (s *AutomaticTVAcquisitionStore) reserveAutomaticTV(ctx context.Context, q *db.Queries, r *automaticTVRoundRow, c ports.AutomaticTVRoundCandidate, op uuid.UUID, now pgtype.Timestamptz) error {
	if e := q.LockDownloadAcquisition(ctx, op); e != nil {
		return e
	}
	// The snapshot boundary already owns this same root lock. Freeze the actual
	// path separately without changing Source87's observation snapshot.
	root, e := q.LockAcquisitionRoot(ctx, r.Snapshot.RootID)
	if e != nil {
		return e
	}
	a, raw, e := automaticTVNewAuthority(r, c, op, automaticTVDigest([]byte(root.Path)), now.Time)
	if e != nil {
		return e
	}
	t := a.Target
	if e = q.AutomaticTVInsertAuthority(ctx, db.AutomaticTVInsertAuthorityParams{OperationID: op, RoundID: r.ID, OwnerID: t.OwnerID, SubscriptionID: t.SubscriptionID, UnitID: t.UnitID, RootID: t.RootID, CandidateID: a.CandidateID, IndexerID: a.IndexerID, ClientID: t.ClientID, QueryID: a.QueryID, Snapshot: raw, DecisionStamp: a.DecisionStamp}); e != nil {
		return e
	}
	if _, e = q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: s.ids.NewUUID(), UnitID: t.UnitID, OperationID: op, CandidateID: a.CandidateID, SourceRootID: t.RootID, SourceGeneration: t.RootGeneration, SourceStamp: a.SourceStamp}); e != nil {
		return e
	}
	if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: s.ids.NewUUID(), UnitID: t.UnitID, RootID: t.RootID, Purpose: "acquire", CandidateID: a.CandidateID, OperationID: op, ExpiresAt: pgtype.Timestamptz{Time: now.Time.Add(24 * time.Hour), Valid: true}}); e != nil {
		return e
	}
	fence, e := json.Marshal(automaticTVDemandFence{DownloadDemandFence: ports.DownloadDemandFence{PolicyID: t.PolicyID, PolicyRevision: t.PolicyRevision, SubscriptionID: &t.SubscriptionID, IntentVersion: t.IntentVersion, RootGeneration: t.RootGeneration, UnitMetadataVersion: t.UnitVersion, MediaMetadataVersion: t.MediaVersion, CandidateID: a.CandidateID, RootID: t.RootID, UnitID: t.UnitID, MediaID: t.MediaID, ActorID: r.ID.String(), ActorType: ports.ActorTypeSystem, SourceStamp: a.SourceStamp}, Goal: "acquire", Kind: "automatic_tv_episode_v1", Authority: op, ProtectedDigest: a.DecisionStamp})
	if e != nil {
		return e
	}
	if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: s.ids.NewUUID(), OperationID: op, UnitID: t.UnitID, RootID: t.RootID, ActorType: string(ports.ActorTypeSystem), ActorID: r.ID.String(), SubmissionFence: fence}); e != nil {
		return e
	}
	payload, e := json.Marshal(automaticTVSubmissionPayload{OperationID: op, RoundID: r.ID, Authority: op, CandidateID: a.CandidateID, RootID: t.RootID, ClientID: t.ClientID})
	if e != nil {
		return e
	}
	_, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: s.ids.NewUUID(), Kind: "automatic_tv_download_submission", DedupeKey: pgtype.Text{String: op.String(), Valid: true}, Priority: 10, Payload: payload, MaxAttempts: 3, NotBefore: now})
	return e
}
