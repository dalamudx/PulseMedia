package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TVSelectionStore only observes; no execution/projection repository is attached.
type TVSelectionStore struct{ mapping *EpisodeMappingStore }

func NewTVSelectionStore(mapping *EpisodeMappingStore) *TVSelectionStore {
	return &TVSelectionStore{mapping: mapping}
}

var _ ports.TVSelectionRepository = (*TVSelectionStore)(nil)

func (s *TVSelectionStore) ReadTVSelection(ctx context.Context, a *ports.Actor, r ports.TVSelectionRequest) (out *ports.TVSelectionPreviewInput, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if err = ports.ValidateTVSelectionRequest(r); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrTVSelectionChanged) {
			// Explicit caller cancellation differs from backend lock/deadline failures.
			if errors.Is(ctx.Err(), context.Canceled) {
				err = context.Canceled
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.mapping.refresh.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = coverageAccess(ctx, q, a, r.SubscriptionID); e != nil {
		return nil, e
	}
	sub, e := q.GetSubscriptionByID(ctx, r.SubscriptionID)
	if e != nil {
		return nil, e
	}
	source, e := readEpisodeSourceContext(ctx, q, a, sub.ID)
	if e != nil {
		return nil, e
	}
	mapping, e := readEpisodeMappingState(ctx, q, a, sub.ID)
	if e != nil {
		return nil, e
	}
	root, e := q.GetLibraryRootByID(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	policy, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrTVSelectionChanged
		}
		return nil, e
	}
	out = &ports.TVSelectionPreviewInput{PackPolicy: r.PackPolicy, Policy: ports.PolicyDocument{ID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision}}
	if json.Unmarshal(policy.Rules, &out.Policy.Rules) != nil || quality.ValidateRules(out.Policy.Rules) != nil {
		return nil, ports.ErrUnavailable
	}
	for _, proof := range source.CompleteSeasons {
		if proof.Season == r.Season {
			out.Season = proof
		}
	}
	if len(out.Season.Units) == 0 {
		return nil, ports.ErrTVSelectionChanged
	}
	for _, u := range source.Units {
		if !u.Retired && u.Season == r.Season {
			out.Current.Canonical = append(out.Current.Canonical, u)
		}
	}
	sort.Slice(out.Current.Canonical, func(i, j int) bool {
		return out.Current.Canonical[i].ID.String() < out.Current.Canonical[j].ID.String()
	})
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	snap, e := readSubscriptionStatus(ctx, q, a, sub.ID, &now.Time)
	if e != nil {
		return nil, e
	}
	projections, e := listTVSelectionWanted(ctx, q, sub.ID)
	if e != nil {
		return nil, e
	}
	byUnit := map[uuid.UUID]db.ListTVSelectionWantedRow{}
	for _, w := range projections {
		byUnit[w.UnitID] = w
	}
	inputs := map[uuid.UUID]int{}
	for i, u := range snap.Units {
		inputs[u.UnitID] = i
	}
	for _, u := range out.Current.Canonical {
		w, ok := byUnit[u.ID]
		pos, exists := inputs[u.ID]
		if !ok || !exists || !w.Current {
			return nil, ports.ErrTVSelectionChanged
		}
		input := snap.Units[pos]
		ownership := quality.AssessOwnership(snap.Rules, input.Files)
		// An unknown file never establishes a missing target, even with a stale green cache.
		if ownership.Unknown || ownership.Owned != w.IsSatisfied {
			return nil, ports.ErrTVSelectionChanged
		}
		out.Wanted = append(out.Wanted, ports.TVSelectionWanted{UnitID: u.ID, UnitVersion: u.Version, Selected: !input.Skipped && !input.Retired, Missing: !w.IsSatisfied, Aired: input.AirDate != nil && !input.AirDate.After(now.Time), Unprotected: !w.Protected && !input.Reserved && !input.ReservationUnknown && !input.SharedAcquisition})
	}
	var client any
	if sub.DownloaderID.Valid {
		c, e := q.GetDownloadClientByID(ctx, uuid.UUID(sub.DownloaderID.Bytes))
		if e != nil {
			return nil, e
		}
		client = []any{c.ID, c.ClientType, c.UpdatedAt, coverageDigest([]any{c.Url, c.Username, c.PasswordEncrypted, json.RawMessage(c.RemotePathMap)})}
	}
	var indexerIDs []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &indexerIDs) != nil {
		return nil, ports.ErrUnavailable
	}
	sort.Slice(indexerIDs, func(i, j int) bool { return indexerIDs[i].String() < indexerIDs[j].String() })
	configs := []any{}
	for _, id := range indexerIDs {
		idx, e := q.GetIndexerByID(ctx, id)
		if e != nil {
			return nil, e
		}
		configs = append(configs, []any{idx.ID, idx.ConfigVersion, idx.Health, idx.NextQueryAt, idx.RateLimitUntil})
	}
	version := ports.TVSelectionVersion{ActorID: uuid.MustParse(a.ID), SubscriptionID: sub.ID, MediaID: source.MediaID, IntentVersion: sub.IntentVersion, MediaVersion: source.MediaVersion, PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, SelectionPolicyStamp: coverageDigest([]any{"tv.selection.policy.v1", r.PackPolicy.Mode, r.PackPolicy.MinMissingRatio})}
	version.ScopeStamp = coverageDigest([]any{"tv.selection.scope.v1", out.Season, out.Current.Canonical, out.Wanted, projections, snap.Units})
	mapping.ObservedAt = time.Time{}
	version.ConfigurationStamp = coverageDigest([]any{"tv.selection.config.v1", root, source.InputStamp, mapping, client, configs, policy})
	out.Current.Version = version
	out.Current.SourceTMDBID = source.TMDBID
	out.Current.AccountAuthorized = true
	out.Current.ObjectAuthorized = true
	out.Current.Active = sub.State == "active"
	out.Current.RootReady = root.Health == "online" && root.BaselineCompleted
	out.Current.EndpointsReady = snap.EndpointsReady && sub.DownloaderID.Valid && snap.DownloaderAvailable && mapping.Current
	sourceDeadlines := make(map[uuid.UUID]time.Time, len(r.Packs)+len(r.Episodes))
	for _, ref := range r.Packs {
		review, e := q.GetTVCoverageCandidateReview(ctx, ref.ReviewID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		if review.ActorID != version.ActorID || review.SubscriptionID != sub.ID || review.CandidateID != ref.CandidateID {
			return nil, ports.ErrForbidden
		}
		window, e := q.GetTVSelectionCandidateObservationWindow(ctx, ref.CandidateID)
		if e != nil {
			return nil, e
		}
		// Pack material binds every current source proof; any proof expiry changes it.
		if window.FirstValidUntil.Valid {
			sourceDeadlines[ref.CandidateID] = window.FirstValidUntil.Time
		}
		observed, e := readTVCoverageCandidate(ctx, q, a, sub.ID, ref.CandidateID)
		if e != nil {
			return nil, e
		}
		candidate, e := readEpisodeCandidate(ctx, q, a, sub.ID, ref.CandidateID)
		if e != nil {
			return nil, e
		}
		facts := selectionCandidateFacts(candidate, version)
		facts.CurrentInputStamp = observed.InputStamp
		facts.EvidenceCurrent = facts.EvidenceCurrent && observed.CanReview
		facts.Rows = observed.Rows
		out.Packs = append(out.Packs, ports.TVSelectionPack{Review: ports.TVCoverageCandidateReviewReceipt{ID: review.ID, SubscriptionID: review.SubscriptionID, CandidateID: review.CandidateID, InputStamp: review.InputStamp, MaterialStamp: review.MaterialStamp, Status: review.Status}, CurrentMaterialStamp: observed.MaterialStamp, Facts: facts})
	}
	for _, ref := range r.Episodes {
		review, e := q.GetEpisodeCandidateReview(ctx, ref.ReviewID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		if review.ActorID != version.ActorID || review.SubscriptionID != sub.ID || review.CandidateID != ref.CandidateID {
			return nil, ports.ErrForbidden
		}
		window, e := q.GetTVSelectionCandidateObservationWindow(ctx, ref.CandidateID)
		if e != nil {
			return nil, e
		}
		// A single candidate requires at least one current observation.
		if window.LastValidUntil.Valid {
			sourceDeadlines[ref.CandidateID] = window.LastValidUntil.Time
		}
		observed, e := readEpisodeCandidate(ctx, q, a, sub.ID, ref.CandidateID)
		if e != nil {
			return nil, e
		}
		facts := selectionCandidateFacts(observed, version)
		out.Episodes = append(out.Episodes, ports.TVSelectionEpisode{Review: ports.EpisodeCandidateReviewReceipt{ID: review.ID, SubscriptionID: review.SubscriptionID, CandidateID: review.CandidateID, UnitID: review.UnitID, InputStamp: review.InputStamp, Status: "reviewed_not_executable"}, Facts: facts})
	}
	// Re-observe candidate identity without changing the MVCC snapshot.
	for i, p := range out.Packs {
		live, e := readTVCoverageCandidate(ctx, q, a, sub.ID, p.Facts.CandidateID)
		if e != nil {
			return nil, e
		}
		if live.InputStamp != p.Facts.CurrentInputStamp || !live.CanReview {
			out.Packs[i].Facts.EvidenceCurrent = false
		}
	}
	for i, p := range out.Episodes {
		live, e := readEpisodeCandidate(ctx, q, a, sub.ID, p.Facts.CandidateID)
		if e != nil {
			return nil, e
		}
		if live.InputStamp != p.Facts.CurrentInputStamp || !live.Current {
			out.Episodes[i].Facts.EvidenceCurrent = false
		}
	}
	// All potentially blocking evidence reads precede this common clock sample.
	// Captured deadlines prevent later queries from extending older proofs.
	final, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	for _, w := range projections {
		if w.ProjectionValidUntil.Valid && !w.ProjectionValidUntil.Time.After(final.Time) {
			return nil, ports.ErrTVSelectionChanged
		}
	}
	if mapping.ValidUntil != nil && !mapping.ValidUntil.After(final.Time) {
		return nil, ports.ErrTVSelectionChanged
	}
	for i := range out.Packs {
		if !sourceDeadlines[out.Packs[i].Facts.CandidateID].After(final.Time) {
			out.Packs[i].Facts.EvidenceCurrent = false
		}
	}
	for i := range out.Episodes {
		if !sourceDeadlines[out.Episodes[i].Facts.CandidateID].After(final.Time) {
			out.Episodes[i].Facts.EvidenceCurrent = false
		}
	}
	candidateInputs := []any{}
	for _, p := range out.Packs {
		candidateInputs = append(candidateInputs, []any{p.Review, p.CurrentMaterialStamp, p.Facts.CurrentInputStamp, p.Facts.EvidenceCurrent})
	}
	for _, p := range out.Episodes {
		candidateInputs = append(candidateInputs, []any{p.Review, p.Facts.CurrentInputStamp, p.Facts.EvidenceCurrent})
	}
	version.ConfigurationStamp = coverageDigest([]any{version.ConfigurationStamp, candidateInputs})
	out.Current.Version = version
	for i := range out.Packs {
		out.Packs[i].Facts.Version = version
	}
	for i := range out.Episodes {
		out.Episodes[i].Facts.Version = version
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func selectionCandidateFacts(o *ports.EpisodeCandidateObservation, v ports.TVSelectionVersion) ports.TVSelectionCandidateFacts {
	f := ports.TVSelectionCandidateFacts{CandidateID: o.CandidateID, Version: v, CurrentInputStamp: o.InputStamp, EvidenceCurrent: o.Current && o.Mapping.Current, StandardNumbering: o.Mapping.Mode == "standard", Name: o.Name, Quality: o.Quality}
	decision, _ := media.SuggestTVIdentity(o.Name, o.ParentTitle, o.ParentOriginalTitle, o.ParentYear, o.Mapping.TMDBID)
	f.EvidenceCurrent = f.EvidenceCurrent && decision != "reject" && !media.HasConflictingTVIdentity(o.Name, o.Mapping.TMDBID)
	units := make([]media.NumberingUnit, 0, len(o.CanonicalUnits))
	for _, u := range o.CanonicalUnits {
		units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Key: u.Key, Version: u.Version, Season: u.Season, Episode: u.Episode, Retired: u.Retired})
	}
	f.Rows, _ = media.ResolveStandardSingleEpisode(media.RecognizeRelease(o.Name), units)
	return f
}

func listTVSelectionWanted(ctx context.Context, q *db.Queries, subscriptionID uuid.UUID) ([]db.ListTVSelectionWantedRow, error) {
	rows, err := q.ListTVSelectionWanted(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	installed, err := q.HasSubscriptionStatusLocalReuseSchema(ctx)
	if err != nil || !installed {
		return rows, err
	}
	protected, err := q.ListTVSelectionLocalReuseProtection(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	byUnit := make(map[uuid.UUID]bool, len(protected))
	for _, unitID := range protected {
		byUnit[unitID] = true
	}
	for i := range rows {
		rows[i].Protected = rows[i].Protected || byUnit[rows[i].UnitID]
	}
	return rows, nil
}
