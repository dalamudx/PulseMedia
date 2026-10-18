package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	sd "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

type EpisodeAcquisitionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewEpisodeAcquisitionStore(p *pgxpool.Pool, ids ports.IDGen) *EpisodeAcquisitionStore {
	return &EpisodeAcquisitionStore{p, ids}
}

var _ ports.EpisodeAcquisitionRepository = (*EpisodeAcquisitionStore)(nil)

func episodeClientStamp(c db.DownloadClient) string {
	raw, _ := json.Marshal([]any{c.ID, c.ClientType, c.Url, c.Username, c.PasswordEncrypted, c.RemotePathMap})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (s *EpisodeAcquisitionStore) AcquireReviewedEpisode(ctx context.Context, a *ports.Actor, c ports.AcquireReviewedEpisodeCommand, key string) (out *ports.AcquireResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !c.Confirmed || c.SubscriptionID == uuid.Nil || c.ReviewID == uuid.Nil || len(c.ExpectedInputStamp) != 64 || strings.TrimSpace(key) == "" || len(key) > 140 {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrEpisodeAcquisitionConflict) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrAcquisitionUnitAlreadyReserved) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	key = "episode.acquire:" + key
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	actorID, _ := uuid.Parse(a.ID)
	if _, e = readTVRefresh(ctx, q, a, c.SubscriptionID); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "episode.acquisition.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	if old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key}); e == nil {
		var r ports.AcquireResult
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &r) != nil || r.OperationID == uuid.Nil {
			return nil, ports.ErrEpisodeAcquisitionConflict
		}
		var valid bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM episode_execution_authority WHERE operation_id=$1 AND review_id=$2 AND subscription_id=$3 AND actor_id=$4)", r.OperationID, c.ReviewID, c.SubscriptionID, actorID).Scan(&valid); e != nil {
			return nil, e
		}
		if !valid {
			return nil, ports.ErrEpisodeAcquisitionConflict
		}
		return &r, nil
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
	if sub.MediaID != hint.MediaID || sub.RootID != hint.RootID || sub.QualityPolicyID != hint.QualityPolicyID || sub.State != "active" || sub.UserID != actorID || !sub.DownloaderID.Valid {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	root, e := q.LockAcquisitionRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	if root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	if _, e = q.LockScopeMediaUnits(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	review, e := q.GetEpisodeCandidateReview(ctx, c.ReviewID)
	if e != nil {
		return nil, e
	}
	if review.ActorID != actorID || review.SubscriptionID != sub.ID || review.MediaID != sub.MediaID || review.InputStamp != c.ExpectedInputStamp || review.ParserVersion != media.EpisodeParserVersion {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	op := s.ids.NewUUID()
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return nil, e
	}
	if _, e = q.LockActiveAcquisitionLane(ctx, review.UnitID); e == nil {
		return nil, ports.ErrAcquisitionUnitAlreadyReserved
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
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
	v, e := readEpisodeCandidate(ctx, q, a, sub.ID, cand.ID)
	if e != nil {
		return nil, e
	}
	if !v.Current || !v.Mapping.Current || (v.Mapping.Mode != "standard" && v.Mapping.Mode != "absolute" && v.Mapping.Mode != "manual") || v.InputStamp != review.InputStamp || v.Mapping.IntentVersion != review.IntentVersion || v.Mapping.MappingRevision != review.MappingRevision || v.Target.MediaMetadataVersion != review.MediaVersion || v.Target.PolicyRevision != review.PolicyRevision || root.Generation != review.RootGeneration || idx.ConfigVersion != review.SourceVersion || cand.IndexerID != idx.ID || media.HasConflictingTVIdentity(v.Name, v.Mapping.TMDBID) {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	if decision, _ := media.SuggestTVIdentity(v.Name, v.ParentTitle, v.ParentOriginalTitle, v.ParentYear, v.Mapping.TMDBID); decision == "reject" {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	units := []media.NumberingUnit{}
	for _, u := range v.CanonicalUnits {
		units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
	}
	rows, _ := media.ResolveStandardSingleEpisode(media.RecognizeRelease(v.Name), units)
	if v.Mapping.Mode != "standard" {
		mapping := []media.NumberingRow{}
		for _, r := range v.Mapping.Rows {
			mapping = append(mapping, media.NumberingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
		}
		rows, _ = media.ResolveApprovedAbsoluteSingle(media.RecognizeRelease(v.Name), v.Mapping.Mode, mapping, units)
	}
	if len(rows) != 1 || rows[0].UnitID != review.UnitID || rows[0].UnitVersion != review.UnitVersion || rows[0].UnitKey != review.UnitKey || !quality.EvaluateCandidate(v.Target.Rules, v.Name, v.Quality).Passed {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	eligible := false
	for _, u := range sd.DeriveProjection(v.Target).Items {
		if u.UnitID == review.UnitID && !u.IsSatisfied && !u.IsSkipped && (u.WaitReason == "missing" || u.WaitReason == "file_rejected" || u.WaitReason == "no_qualified_candidate") {
			eligible = true
		}
	}
	if !eligible {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	other, e := q.ListPotentialReusableFiles(ctx, db.ListPotentialReusableFilesParams{UnitID: review.UnitID, RootID: root.ID})
	if e != nil {
		return nil, e
	}
	if len(other) > 0 {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	var now pgtype.Timestamptz
	e = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	if e != nil {
		return nil, e
	}
	snapshot := ports.EpisodeExecutionSnapshot{Kind: "reviewed_standard_episode", ReviewID: review.ID, ReviewInputStamp: review.InputStamp, SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion, MediaID: sub.MediaID, MediaType: "tv", MediaVersion: review.MediaVersion, UnitID: review.UnitID, UnitKey: review.UnitKey, UnitVersion: review.UnitVersion, EpisodeID: rows[0].EpisodeID, MappingMode: v.Mapping.Mode, MappingRevision: review.MappingRevision, ParserVersion: media.EpisodeParserVersion, PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, RootID: root.ID, RootGeneration: root.Generation, CandidateID: cand.ID, IndexerID: idx.ID, SourceVersion: idx.ConfigVersion, SourceStamp: quality.AcquisitionSourceStamp(cand.Title, cand.InfoHash.String, cand.LinkEncrypted, cand.QualityProfile), ClientID: clientID, ClientStamp: episodeClientStamp(client), ActorID: actorID, AcquiredAt: now.Time}
	snapshot.MappingSourceHash = v.Mapping.SourceHash
	snapshot.MappingInputStamp = v.Mapping.InputStamp
	snapshot.MappingValidUntil = v.Mapping.ValidUntil
	if v.Mapping.Mode != "standard" {
		snapshot.Kind = "reviewed_mapped_episode"
		snapshot.SourceNumber = rows[0].SourceNumber
		snapshot.MappingRows = append([]ports.EpisodeGroupMappingRow(nil), v.Mapping.Rows...)
	}
	if !snapshot.ValidNumberingShape() || (snapshot.MappingValidUntil != nil && !snapshot.MappingValidUntil.After(now.Time)) {
		return nil, ports.ErrEpisodeAcquisitionConflict
	}
	evidence, _ := json.Marshal(snapshot)
	if _, e = tx.Exec(ctx, "INSERT INTO episode_execution_authority(operation_id,review_id,subscription_id,candidate_id,unit_id,root_id,client_id,actor_id,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", op, review.ID, sub.ID, cand.ID, review.UnitID, root.ID, clientID, actorID, evidence); e != nil {
		return nil, e
	}
	if _, e = q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: s.ids.NewUUID(), UnitID: review.UnitID, OperationID: op, CandidateID: cand.ID, SourceRootID: root.ID, SourceGeneration: root.Generation, SourceStamp: snapshot.SourceStamp}); e != nil {
		return nil, e
	}
	resID := s.ids.NewUUID()
	if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: resID, UnitID: review.UnitID, RootID: root.ID, Purpose: "manual_acquisition", CandidateID: cand.ID, OperationID: op, ExpiresAt: pgtype.Timestamptz{Time: now.Time.Add(24 * time.Hour), Valid: true}}); e != nil {
		return nil, e
	}
	fence, _ := json.Marshal(ports.DownloadDemandFence{PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, SubscriptionID: &sub.ID, IntentVersion: sub.IntentVersion, RootGeneration: root.Generation, UnitMetadataVersion: review.UnitVersion, MediaMetadataVersion: review.MediaVersion, CandidateID: cand.ID, RootID: root.ID, UnitID: review.UnitID, MediaID: sub.MediaID, ActorID: a.ID, ActorType: a.Type, SourceStamp: snapshot.SourceStamp})
	if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: s.ids.NewUUID(), OperationID: op, UnitID: review.UnitID, RootID: root.ID, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: fence}); e != nil {
		return nil, e
	}
	jobID := s.ids.NewUUID()
	payload, _ := json.Marshal(map[string]any{"operation_id": op, "candidate_id": cand.ID, "root_id": root.ID, "root_generation": root.Generation, "client_id": clientID, "unit_ids": []uuid.UUID{review.UnitID}, "episode_authority": op})
	if _, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: jobID, Kind: "episode_download_submission", DedupeKey: pgtype.Text{String: op.String(), Valid: true}, Priority: 10, Payload: payload, MaxAttempts: 3, NotBefore: now}); e != nil {
		return nil, e
	}
	out = &ports.AcquireResult{OperationID: op, Reservations: []uuid.UUID{resID}, JobID: jobID, Status: "dispatched", SourceCandidateID: cand.ID}
	receipt, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "episode.acquisition.create", TargetType: "subscription", TargetID: sub.ID.String(), Payload: receipt}); e != nil {
		return nil, e
	}
	if _, e = (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", op.String(), "acquisition.initiated", map[string]any{"operation_id": op, "candidate_id": cand.ID, "unit_ids": []uuid.UUID{review.UnitID}}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 202, ResponseBody: receipt, ResourceID: pgtype.Text{String: op.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
