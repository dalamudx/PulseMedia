package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// UpgradePreviewStore has no writer, filesystem or network dependency.
// Shared row locks require a writable transaction; the body issues SELECT only.
type UpgradePreviewStore struct {
	pool           *pgxpool.Pool
	indexerDecoder ports.CredentialDecoder
}

func NewUpgradePreviewStore(pool *pgxpool.Pool, decoders ...ports.CredentialDecoder) *UpgradePreviewStore {
	s := &UpgradePreviewStore{pool: pool}
	if len(decoders) > 0 {
		s.indexerDecoder = decoders[0]
	}
	return s
}

var _ ports.UpgradePreviewRepository = (*UpgradePreviewStore)(nil)

func (s *UpgradePreviewStore) ReadUpgradePreview(ctx context.Context, a *ports.Actor, r ports.UpgradePreviewRequest) (out *ports.UpgradePreviewSnapshot, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if r.SubscriptionID == uuid.Nil || r.CurrentFileID == uuid.Nil || r.CandidateID == uuid.Nil {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrInvalidEpisodeMapping) {
			if errors.Is(ctx.Err(), context.Canceled) {
				err = context.Canceled
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			out = nil
			err = rollbackErr
		}
	}()
	out, e = readUpgradePreview(ctx, db.New(tx), a, r, s.indexerDecoder)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

// readUpgradePreview reads trusted evidence inside the caller's transaction.
// It neither commits nor turns the observation into execution authority.
func readUpgradePreview(ctx context.Context, q *db.Queries, a *ports.Actor, r ports.UpgradePreviewRequest, decoders ...ports.CredentialDecoder) (out *ports.UpgradePreviewSnapshot, err error) {
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	sub, e := q.GetSubscriptionByID(ctx, r.SubscriptionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	policy, e := q.LockUpgradePolicy(ctx, sub.QualityPolicyID)
	if e != nil {
		return nil, e
	}
	sub, e = q.LockUpgradeSubscription(ctx, r.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: policy.ID, Revision: policy.CurrentRevision})
	if e != nil {
		return nil, e
	}
	var rules quality.PolicyRules
	if json.Unmarshal(revision.Rules, &rules) != nil || quality.ValidateRules(rules) != nil {
		return nil, ports.ErrUnavailable
	}
	root, e := q.LockUpgradeRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	target, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	units, e := q.ListUpgradeUnits(ctx, target.ID)
	if e != nil {
		return nil, e
	}
	out = &ports.UpgradePreviewSnapshot{Mode: ports.UpgradeMode(sub.UpgradeMode), CurrentFileID: r.CurrentFileID, CandidateID: r.CandidateID}
	// The opaque downloader observation is read before the final source/deadline checks.
	if sub.DownloaderID.Valid {
		client, clientErr := q.GetDownloadClientByID(ctx, uuid.UUID(sub.DownloaderID.Bytes))
		if clientErr != nil && !errors.Is(clientErr, pgx.ErrNoRows) {
			return nil, clientErr
		}
		if clientErr == nil {
			out.ExpectedClientDigest = episodeClientStamp(client)
		}
	}
	hold := func(reason string) {
		if out.HoldReason == "" {
			out.HoldReason = reason
		}
	}
	if sub.State != "active" {
		hold("subscription_inactive")
	}
	if sub.QualityPolicyRevision != policy.CurrentRevision {
		hold("policy_input_changed")
	}
	if root.Health != "online" || !root.BaselineCompleted {
		hold("root_unavailable")
	}
	if target.MediaType != "movie" || len(units) != 1 || (len(units) == 1 && (units[0].UnitKey != "movie" || units[0].Retired)) {
		hold("standard_single_movie_required")
	}
	// Unsupported scopes never choose an arbitrary first unit.
	if len(units) != 1 {
		candidate, e := q.LockUpgradeCandidate(ctx, r.CandidateID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return nil, e
		}
		out.ObservedAt = now.Time
		out.SourceID = candidate.IndexerID
		out.Version = ports.UpgradePreviewVersion{ActorID: uuid.MustParse(a.ID), SubscriptionID: sub.ID, MediaID: target.ID, RootID: root.ID, IntentVersion: sub.IntentVersion, PolicyID: policy.ID, PolicyRevision: policy.CurrentRevision, InputStamp: coverageDigest([]any{"upgrade.preview.unsupported.v1", sub, policy, revision, root, target, units, candidate, r.CurrentFileID})}
		return out, nil
	}
	unit := units[0]
	files, e := q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{RootID: root.ID, UnitID: unit.ID})
	if e != nil {
		return nil, e
	}
	if len(files) != 1 {
		hold("current_file_ambiguous")
	}
	selected := false
	for _, f := range files {
		if f.ID == r.CurrentFileID {
			selected = true
			if f.UnitCount != 1 {
				hold("current_file_multi_unit")
			}
		}
	}
	if !selected {
		return nil, ports.ErrNotFound
	}
	facts, e := q.ListUpgradeFacts(ctx, db.ListUpgradeFactsParams{SourceFileID: pgtype.UUID{Bytes: r.CurrentFileID, Valid: true}, UnitID: unit.ID, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}})
	if e != nil {
		return nil, e
	}
	if len(facts) != 1 {
		hold("current_verified_fact_required")
	} else {
		out.CurrentFactID = facts[0].ID
		if json.Unmarshal(facts[0].QualityProfile, &out.Current) != nil {
			hold("current_quality_unknown")
		}
	}
	projections, e := listTVSelectionWanted(ctx, q, sub.ID)
	if e != nil {
		return nil, e
	}
	if len(projections) != 1 || projections[0].UnitID != unit.ID || !projections[0].Current || projections[0].IsSkipped {
		hold("projection_input_changed")
	}
	identities, e := q.ListSearchMediaIdentities(ctx, target.ID)
	if e != nil {
		return nil, e
	}
	matching := map[string]string{}
	for _, identity := range identities {
		matching[identity.Namespace] = identity.ExternalID
	}
	window, e := q.ReadUpgradeSourceWindow(ctx, r.CandidateID)
	if e != nil {
		return nil, e
	}
	// Read source configuration and block state before the final candidate lock.
	candidate, e := q.GetCandidateByID(ctx, r.CandidateID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	indexer, e := q.GetIndexerByID(ctx, candidate.IndexerID)
	if e != nil {
		return nil, e
	}
	var indexerDecoder ports.CredentialDecoder
	if len(decoders) > 0 {
		indexerDecoder = decoders[0]
	}
	indexer, modes, e := ResolveIndexerConfiguration(ctx, q, indexerDecoder, indexer)
	if e != nil {
		return nil, e
	}
	if !modes.LegacyV1 && modes.Protocol != ports.IndexerProtocolTorznab {
		hold("source_protocol_unsupported")
	}
	blocked, e := q.ListSubscriptionBlockedCandidates(ctx, sub.ID)
	if e != nil {
		return nil, e
	}
	for _, b := range blocked {
		if b.ID == candidate.ID {
			hold("candidate_blocked")
		}
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil {
		return nil, ports.ErrUnavailable
	}
	hasSource := false
	for _, id := range bound {
		hasSource = hasSource || id == indexer.ID
	}
	if !hasSource || indexer.Health != "online" || candidate.SourceConfigVersion < 1 || candidate.SourceConfigVersion != indexer.ConfigVersion {
		hold("source_input_changed")
	}
	candidate, e = q.LockUpgradeCandidate(ctx, r.CandidateID)
	if e != nil {
		return nil, e
	}
	// One common fresh time after every potentially blocking input read.
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	out.ObservedAt = now.Time
	if !window.Valid || !window.Time.After(now.Time) {
		hold("candidate_source_expired")
	}
	for _, f := range facts {
		if f.ExpiresAt.Valid && !f.ExpiresAt.Time.After(now.Time) {
			hold("current_fact_expired")
		}
	}
	for _, w := range projections {
		if w.ProjectionValidUntil.Valid && !w.ProjectionValidUntil.Time.After(now.Time) {
			hold("projection_expired")
		}
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) || profile.ResolutionParserVersion != quality.ResolutionTokenParserVersion {
		hold("candidate_quality_unknown")
	}
	// Persisted independent attribute versions must agree with the current full-title parser.
	if profile != media.Recognize(candidate.Title).Quality {
		hold("candidate_quality_evidence_changed")
	}
	if candidate.MatchedMediaID.Valid && uuid.UUID(candidate.MatchedMediaID.Bytes) != target.ID || candidate.MatchedUnitID.Valid && uuid.UUID(candidate.MatchedUnitID.Bytes) != unit.ID {
		hold("candidate_identity_mismatch")
	}
	out.SourceID = indexer.ID
	out.Candidate = media.CandidateRuleInput{CandidateID: candidate.ID.String(), PolicyID: policy.ID, PolicyRevision: policy.CurrentRevision, MediaID: target.ID.String(), UnitID: unit.ID.String(), MediaVersion: target.MetadataVersion, UnitVersion: unit.MetadataVersion, RawProfile: string(candidate.QualityProfile), Scope: sub.ScopeType + ":" + string(sub.Seasons), Title: candidate.Title, TargetTitle: target.Title, Year: int(target.Year.Int32), Identities: matching, Profile: profile, Rules: rules, SizeBytes: candidate.SizeBytes, HasReference: len(candidate.LinkEncrypted) > 0, SourceVersion: candidate.SourceConfigVersion}
	out.Version = ports.UpgradePreviewVersion{ActorID: uuid.MustParse(a.ID), SubscriptionID: sub.ID, MediaID: target.ID, UnitID: unit.ID, RootID: root.ID, IntentVersion: sub.IntentVersion, PolicyID: policy.ID, PolicyRevision: policy.CurrentRevision, InputStamp: coverageDigest([]any{"upgrade.preview.v1", media.EpisodeParserVersion, media.CandidateDecisionVersion, sub, policy, revision, root, target, units, files, facts, projections, identities, candidate, indexer, window, blocked})}
	return out, nil
}
