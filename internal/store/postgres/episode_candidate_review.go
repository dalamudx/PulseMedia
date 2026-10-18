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

func episodeReviewActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.ErrForbidden
	}
	if _, e := uuid.Parse(a.ID); e != nil {
		return ports.ErrForbidden
	}
	return nil
}
func cachedEpisodeCandidateReview(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.EpisodeCandidateReviewCommand, key, hash string) (*ports.EpisodeCandidateReviewReceipt, error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if _, e := readTVRefresh(ctx, q, a, c.SubscriptionID); e != nil {
		return nil, e
	}
	r, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var out ports.EpisodeCandidateReviewReceipt
	if r.RequestHash != hash || json.Unmarshal(r.ResponseBody, &out) != nil || out.SubscriptionID != c.SubscriptionID || out.CandidateID != c.CandidateID || out.ID == uuid.Nil || out.Status != "reviewed_not_executable" {
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	record, e := q.GetEpisodeCandidateReview(ctx, out.ID)
	if e != nil {
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	actorID, _ := uuid.Parse(a.ID)
	if record.ActorID != actorID || record.SubscriptionID != out.SubscriptionID || record.CandidateID != out.CandidateID || record.UnitID != out.UnitID || record.InputStamp != out.InputStamp || out.InputStamp != c.ExpectedInputStamp {
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	return &out, nil
}
func (s *EpisodeMappingStore) ReadEpisodeCandidateReviewReceipt(ctx context.Context, a *ports.Actor, c ports.EpisodeCandidateReviewCommand, key, hash string) (*ports.EpisodeCandidateReviewReceipt, error) {
	tx, e := s.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	out, e := cachedEpisodeCandidateReview(ctx, db.New(tx), a, c, key, hash)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func (s *EpisodeMappingStore) CommitEpisodeCandidateReview(ctx context.Context, a *ports.Actor, c ports.EpisodeCandidateReviewCommand, key, hash string) (out *ports.EpisodeCandidateReviewReceipt, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !c.Confirmed || len(c.ExpectedInputStamp) != 64 || c.CandidateID == uuid.Nil || c.SubscriptionID == uuid.Nil || key == "" || len(key) > 160 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || media.UnsafeCandidateTitle(c.Reason) || strings.ContainsAny(c.Reason, "\r\n") || strings.Contains(strings.ToLower(c.Reason), "token=") {
		return nil, ports.ErrInvalidEpisodeCandidateReview
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrEpisodeCandidateReviewConflict) && !errors.Is(err, ports.ErrInvalidEpisodeCandidateReview) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.refresh.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	actorKey, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "episode.candidate.review.command", AggregateID: string(actorKey)}); e != nil {
		return nil, e
	}
	if old, e := cachedEpisodeCandidateReview(ctx, q, a, c, key, hash); e != nil || old != nil {
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
	if sub.MediaID != hint.MediaID || sub.RootID != hint.RootID || sub.QualityPolicyID != hint.QualityPolicyID || sub.State == "archived" {
		return nil, ports.ErrEpisodeCandidateReviewConflict
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
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	v, e := readEpisodeCandidate(ctx, q, a, sub.ID, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if !v.Current || !v.Mapping.Current || v.InputStamp != c.ExpectedInputStamp || media.UnsafeCandidateTitle(v.Name) || strings.ContainsAny(v.Name, "/\\") || len(v.Name) > 512 || media.HasConflictingTVIdentity(v.Name, v.Mapping.TMDBID) {
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	decision, _ := media.SuggestTVIdentity(v.Name, v.ParentTitle, v.ParentOriginalTitle, v.ParentYear, v.Mapping.TMDBID)
	if decision == "reject" {
		return nil, ports.ErrEpisodeCandidateReviewConflict
	}
	parsed := media.RecognizeRelease(v.Name)
	var rows []media.NumberingRow
	if v.Mapping.Mode == "standard" {
		units := []media.NumberingUnit{}
		for _, u := range v.CanonicalUnits {
			units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
		}
		rows, _ = media.ResolveStandardSingleEpisode(parsed, units)
	} else {
		mapping := []media.NumberingRow{}
		for _, r := range v.Mapping.Rows {
			mapping = append(mapping, media.NumberingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
		}
		canonical := []media.NumberingUnit{}
		for _, u := range v.CanonicalUnits {
			canonical = append(canonical, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
		}
		rows, _ = media.ResolveApprovedAbsoluteSingle(parsed, v.Mapping.Mode, mapping, canonical)
	}
	if len(rows) != 1 {
		return nil, ports.ErrInvalidEpisodeCandidateReview
	}
	row := rows[0]
	actorID, _ := uuid.Parse(a.ID)
	record, e := q.CreateEpisodeCandidateReview(ctx, db.CreateEpisodeCandidateReviewParams{ID: s.ids.NewUUID(), ActorID: actorID, SubscriptionID: sub.ID, MediaID: sub.MediaID, UnitID: row.UnitID, CandidateID: c.CandidateID, InputStamp: v.InputStamp, UnitKey: row.UnitKey, UnitVersion: row.UnitVersion, MediaVersion: v.Target.MediaMetadataVersion, IntentVersion: sub.IntentVersion, MappingRevision: v.Mapping.MappingRevision, PolicyRevision: v.Target.PolicyRevision, RootGeneration: v.Target.RootGeneration, SourceVersion: v.SourceVersion, ParserVersion: media.EpisodeParserVersion, Reason: strings.TrimSpace(c.Reason)})
	if e != nil {
		return nil, e
	}
	out = &ports.EpisodeCandidateReviewReceipt{ID: record.ID, SubscriptionID: sub.ID, CandidateID: c.CandidateID, UnitID: row.UnitID, InputStamp: v.InputStamp, Status: "reviewed_not_executable"}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "episode_candidate.reviewed", TargetType: "subscription", TargetID: sub.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: sub.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: sub.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "subscription", AggregateID: sub.ID.String(), EventType: "episode_candidate.reviewed", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: sub.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
