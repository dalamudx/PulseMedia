package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"
)

func cachedTVCoverageReview(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.TVCoverageCandidateReviewCommand, key, hash string) (*ports.TVCoverageCandidateReviewReceipt, error) {
	if e := coverageAccess(ctx, q, a, c.SubscriptionID); e != nil {
		return nil, e
	}
	r, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out ports.TVCoverageCandidateReviewReceipt
	if r.RequestHash != hash || r.ResponseCode != 200 || json.Unmarshal(r.ResponseBody, &out) != nil || out.ID == uuid.Nil || out.SubscriptionID != c.SubscriptionID || out.CandidateID != c.CandidateID || out.InputStamp != c.ExpectedInputStamp || out.Status != "reviewed_not_executable" {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	record, e := q.GetTVCoverageCandidateReview(ctx, out.ID)
	if e != nil || record.ActorID.String() != a.ID || record.SubscriptionID != out.SubscriptionID || record.CandidateID != out.CandidateID || record.InputStamp != out.InputStamp || record.MaterialStamp != out.MaterialStamp || record.Status != out.Status {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	return &out, nil
}
func (s *TVCoverageCandidateReviewStore) ReadTVCoverageCandidateReviewReceipt(ctx context.Context, a *ports.Actor, c ports.TVCoverageCandidateReviewCommand, key, hash string) (*ports.TVCoverageCandidateReviewReceipt, error) {
	tx, e := s.mapping.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	out, e := cachedTVCoverageReview(ctx, db.New(tx), a, c, key, hash)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func (s *TVCoverageCandidateReviewStore) CommitTVCoverageCandidateReview(ctx context.Context, a *ports.Actor, c ports.TVCoverageCandidateReviewCommand, key, hash string) (out *ports.TVCoverageCandidateReviewReceipt, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	decoded := false
	if len(c.ExpectedInputStamp) == 64 {
		decoded = true
		for _, v := range c.ExpectedInputStamp {
			if !strings.ContainsRune("0123456789abcdef", v) {
				decoded = false
			}
		}
	}
	if !c.Confirmed || !decoded || c.SubscriptionID == uuid.Nil || c.CandidateID == uuid.Nil || key == "" || len(key) > 160 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || media.UnsafeCandidateTitle(c.Reason) || strings.ContainsAny(c.Reason, "\r\n") || strings.Contains(strings.ToLower(c.Reason), "token=") {
		return nil, ports.ErrInvalidTVCoverageReview
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrTVCoverageReviewConflict) && !errors.Is(err, ports.ErrInvalidTVCoverageReview) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrUnauthorized) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.mapping.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	// Never wait for an account while holding policy, subscription, root or unit locks.
	if e = coverageAccess(ctx, q, a, c.SubscriptionID); e != nil {
		return nil, e
	}
	actorKey, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "tv.coverage.review.command", AggregateID: string(actorKey)}); e != nil {
		return nil, e
	}
	if old, e := cachedTVCoverageReview(ctx, q, a, c, key, hash); e != nil || old != nil {
		return old, e
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
	if sub.MediaID != hint.MediaID || sub.RootID != hint.RootID || sub.UserID != hint.UserID || sub.QualityPolicyID != hint.QualityPolicyID || sub.State == "archived" {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	if _, e = q.LockAcquisitionRoot(ctx, sub.RootID); e != nil {
		return nil, e
	}
	if _, e = q.LockScopeMediaUnits(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionMedia(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	candidate, e := q.GetCandidateByID(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionIndexer(ctx, candidate.IndexerID); e != nil {
		return nil, e
	}
	locked, e := q.LockSubmissionCandidate(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if locked.IndexerID != candidate.IndexerID {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	if sub.DownloaderID.Valid {
		if _, e = tx.Exec(ctx, "SELECT id FROM download_client WHERE id=$1 FOR SHARE", uuid.UUID(sub.DownloaderID.Bytes)); e != nil {
			return nil, e
		}
	}
	v, e := readTVCoverageCandidate(ctx, q, a, sub.ID, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if !v.CanReview || v.InputStamp != c.ExpectedInputStamp {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	rows, _ := json.Marshal(v.Rows)
	record, e := q.CreateTVCoverageCandidateReview(ctx, db.CreateTVCoverageCandidateReviewParams{ID: s.mapping.ids.NewUUID(), ActorID: uuid.MustParse(a.ID), SubscriptionID: sub.ID, CandidateID: c.CandidateID, InputStamp: v.InputStamp, MaterialStamp: v.MaterialStamp, Material: v.Material, Facts: v.Facts, ResolvedUnits: rows, Reason: strings.TrimSpace(c.Reason)})
	if e != nil {
		return nil, e
	}
	out = &ports.TVCoverageCandidateReviewReceipt{ID: record.ID, SubscriptionID: sub.ID, CandidateID: c.CandidateID, InputStamp: record.InputStamp, MaterialStamp: record.MaterialStamp, Status: record.Status}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.mapping.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "tv_coverage_candidate.reviewed", TargetType: "subscription", TargetID: sub.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: sub.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: sub.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.mapping.ids.NewUUID(), AggregateType: "subscription", AggregateID: sub.ID.String(), EventType: "tv_coverage_candidate.reviewed", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.mapping.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: record.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	// Recheck freshness after all potentially blocking writes; compare the original seal,
	// never mint a replacement review from a later observation.
	final, e := readTVCoverageCandidate(ctx, q, a, sub.ID, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if !final.CanReview || final.InputStamp != v.InputStamp {
		return nil, ports.ErrTVCoverageReviewConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
