package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// FinalizeMovieRound owns one transaction: authority, snapshot checks, selection,
// reservation/requirement/submission/event/audit, round result and executing job.
func (s *AcquisitionStore) FinalizeMovieRound(ctx context.Context, actor *ports.Actor, id uuid.UUID, authority *RoundJobAuthority) (*RoundDecision, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if s.selector == nil {
		return nil, ports.ErrUnavailable
	}
	pre, err := s.queries.GetSearchRoundByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var snap RoundSnapshot
	if err = json.Unmarshal(pre.DecisionSnapshot, &snap); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	if authority != nil {
		if _, err = q.LockJobForWrite(ctx, authority.ID); err != nil {
			return nil, err
		}
		job, e := q.GetAuthorizedSearchJob(ctx, db.GetAuthorizedSearchJobParams{ID: authority.ID, Kind: "search_round_finalize", LeaseOwner: pgtype.Text{String: authority.Owner, Valid: true}, LeaseToken: authority.Token})
		if e != nil {
			return nil, e
		}
		var p struct {
			RoundID uuid.UUID `json:"round_id"`
		}
		_ = json.Unmarshal(job.Payload, &p)
		if job.Kind != "search_round_finalize" || p.RoundID != id {
			return nil, ports.ErrUnauthorized
		}
	}
	// policy -> subscription -> round/wanted -> endpoints -> root -> sorted units.
	if snap.PolicyID != "" {
		if _, err = q.LockSearchPolicy(ctx, snap.PolicyID); err != nil {
			return nil, err
		}
	}
	sub, err := q.LockSearchSubscription(ctx, pre.SubscriptionID)
	if err != nil {
		return nil, err
	}
	if actor.Type != ports.ActorTypeSystem && actor.Role != ports.RoleAdmin && actor.ID != sub.UserID.String() {
		return nil, ports.ErrUnauthorized
	}
	if snap.Scheduled && authority == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type == ports.ActorTypeSystem && actor.ID != id.String() {
		return nil, ports.ErrUnauthorized
	}
	round, err := q.LockSearchRound(ctx, id)
	if err != nil {
		return nil, err
	}
	report := &RoundDecision{RoundID: id, State: round.State, Reason: round.AbortReason.String, BestScore: -1}
	if round.OperationID.Valid {
		report.OperationID = round.OperationID.Bytes
	}
	if round.SelectedCandidateID.Valid {
		selected := uuid.UUID(round.SelectedCandidateID.Bytes)
		report.SelectedCandidateID = &selected
	}
	finish := func(state, reason string) (*RoundDecision, error) {
		report.State = state
		report.Reason = reason
		if round.State == "running" {
			arg := db.SetRoundDecisionParams{ID: id, State: state, AbortReason: pgtype.Text{String: reason, Valid: reason != ""}}
			if report.SelectedCandidateID != nil {
				arg.SelectedCandidateID = pgtype.UUID{Bytes: *report.SelectedCandidateID, Valid: true}
			}
			if report.OperationID != uuid.Nil {
				arg.OperationID = pgtype.UUID{Bytes: report.OperationID, Valid: true}
			}
			if _, err = q.SetRoundDecision(ctx, arg); err != nil {
				return nil, err
			}
			if err = q.RescheduleWantedEvaluation(ctx, db.RescheduleWantedEvaluationParams{ID: round.WantedItemID, DelayMs: 300000}); err != nil {
				return nil, err
			}
		}
		if authority != nil {
			result, _ := json.Marshal(report)
			if _, err = q.FinishJob(ctx, db.FinishJobParams{Column1: authority.ID, Column2: authority.Owner, Column3: authority.Token, Column4: result}); err != nil {
				return nil, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		if state == "aborted" {
			return report, ErrRoundAborted
		}
		return report, nil
	}
	if round.State != "running" {
		return finish(round.State, round.AbortReason.String)
	}
	if sub.State != "active" {
		return finish("aborted", "subscription_paused")
	}
	if sub.IntentVersion != round.SnapshotIntentVersion {
		return finish("aborted", "intent_version_changed")
	}
	if sub.QualityPolicyRevision != round.SnapshotPolicyRevision || sub.QualityPolicyID != snap.PolicyID {
		return finish("aborted", "policy_revision_changed")
	}
	if snap.UnitID == uuid.Nil || snap.RootID != sub.RootID || !bytes.Equal(compactJSON(sub.IndexerIds), compactJSON(snap.Bindings)) {
		return finish("aborted", "target_or_bindings_changed")
	}
	var downloader *uuid.UUID
	if sub.DownloaderID.Valid {
		v := uuid.UUID(sub.DownloaderID.Bytes)
		downloader = &v
	}
	if (downloader == nil) != (snap.DownloaderID == nil) || (downloader != nil && *downloader != *snap.DownloaderID) {
		return finish("aborted", "downloader_changed")
	}
	if snap.Scheduled {
		ready, e := q.IsSearchRoundReady(ctx, id)
		if e != nil {
			return nil, e
		}
		if !ready.Bool {
			return nil, ErrRoundNotReady
		}
	}

	root, err := q.LockAcquisitionRoot(ctx, snap.RootID)
	if err != nil {
		return nil, err
	}
	if root.Health != "online" || !root.BaselineCompleted || root.Generation != snap.RootGeneration || root.Path != snap.RootPath {
		return finish("aborted", "root_changed")
	}
	unit, unitErr := q.LockAcquisitionUnit(ctx, snap.UnitID)
	if unitErr != nil {
		return nil, unitErr
	}
	if unit.Retired || unit.MediaID != snap.MediaID || unit.UnitKey != "movie" || unit.MetadataVersion != snap.UnitMetadataVersion {
		return finish("aborted", "unit_changed")
	}
	// Import/projection guard order is root -> unit -> wanted, never the inverse.
	wanted, err := q.LockSearchWanted(ctx, round.WantedItemID)
	if err != nil {
		return nil, err
	}
	if wanted.IsSkipped || wanted.UnitID != snap.UnitID {
		return finish("aborted", "wanted_changed")
	}
	media, e := q.LockSearchMedia(ctx, snap.MediaID)
	if e != nil {
		return nil, e
	}
	if media.MediaType != "movie" || media.Title != snap.Title || media.Year.Int32 != snap.Year || media.MetadataVersion != snap.MediaMetadataVersion {
		return finish("aborted", "media_changed")
	}
	rulesRow, err := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: snap.PolicyID, Revision: round.SnapshotPolicyRevision})
	if err != nil {
		return nil, err
	}
	var rules quality.PolicyRules
	if err = json.Unmarshal(rulesRow.Rules, &rules); err != nil {
		return nil, err
	}
	var candidates []db.ReleaseCandidate
	if snap.Scheduled && !snap.SharedSource {
		candidates, err = q.ListRoundQueryCandidates(ctx, id)
		if err != nil {
			return nil, err
		}
	} else {
		var ids []uuid.UUID
		if err = json.Unmarshal(round.CandidateIds, &ids); err != nil {
			return nil, err
		}
		for _, cid := range ids {
			candidate, e := q.GetCandidateByID(ctx, cid)
			if e != nil {
				return nil, e
			}
			candidates = append(candidates, candidate)
		}
	}
	// Keep the repository's frozen rules and identities independent from callback input.
	var selectionRules quality.PolicyRules
	if err = json.Unmarshal(rulesRow.Rules, &selectionRules); err != nil {
		return nil, err
	}
	var identities map[string]string
	if snap.Identities != nil {
		identities = make(map[string]string, len(snap.Identities))
		for namespace, value := range snap.Identities {
			identities[namespace] = value
		}
	}
	material := ports.MovieRoundSelectionInput{
		PolicyID: snap.PolicyID, PolicyRevision: round.SnapshotPolicyRevision,
		Rules: selectionRules, TargetTitle: snap.Title, Year: int(snap.Year), Identities: identities,
		Candidates: make([]ports.MovieRoundCandidate, 0, len(candidates)),
	}
	admissibleIDs := make(map[uuid.UUID]struct{}, len(candidates))
	for i := range candidates {
		cand := &candidates[i]
		blocked, e := q.IsSubscriptionReleaseBlocked(ctx, db.IsSubscriptionReleaseBlockedParams{SubscriptionID: sub.ID, CandidateID: cand.ID})
		if e != nil {
			return nil, e
		}
		version, ok := snap.Indexers[cand.IndexerID.String()]
		admissible := !blocked && ok && cand.SourceConfigVersion == version
		if admissible {
			admissibleIDs[cand.ID] = struct{}{}
		}
		material.Candidates = append(material.Candidates, ports.MovieRoundCandidate{
			ID: cand.ID, Title: cand.Title, SizeBytes: cand.SizeBytes,
			HasReference: len(cand.LinkEncrypted) > 0, SourceVersion: cand.SourceConfigVersion,
			Admissible: admissible,
		})
	}
	selection := s.selector(material)
	report.BestScore = selection.BestScore
	var best *db.ReleaseCandidate
	if selection.SelectedCandidateID != nil {
		selectedID := *selection.SelectedCandidateID
		if _, ok := admissibleIDs[selectedID]; !ok {
			return nil, ports.ErrUnavailable
		}
		for i := range candidates {
			if candidates[i].ID == selectedID {
				best = &candidates[i]
				break
			}
		}
		if best == nil {
			return nil, ports.ErrUnavailable
		}
	}
	if best != nil {
		if _, e := q.LockSubmissionCandidate(ctx, best.ID); e != nil {
			return nil, e
		}
	}
	keys := make([]string, 0, len(snap.Indexers))
	for key := range snap.Indexers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		version := snap.Indexers[key]
		idxID, e := uuid.Parse(key)
		if e != nil {
			return nil, e
		}
		idx, e := q.LockSearchIndexer(ctx, idxID)
		if e != nil {
			return finish("aborted", "endpoint_changed")
		}
		if idx.ConfigVersion != version || idx.Health != "online" {
			return finish("aborted", "endpoint_changed")
		}
	}
	if snap.Scheduled {
		if snap.SelectedDownloaderID == nil {
			return finish("aborted", "downloader_missing")
		}
		config, e := q.GetSearchDownloaderSnapshot(ctx, *snap.SelectedDownloaderID)
		if e != nil || config.ConfigDigest != snap.DownloaderConfigDigest {
			return finish("aborted", "downloader_changed")
		}
	}

	if best == nil {
		return finish("expired", "no_qualified_candidate")
	}

	demandActor := *actor
	demandActor.Type = ports.ActorTypeUser
	demandActor.ID = sub.UserID.String()
	result, err := s.reserveMovie(ctx, q, &demandActor, AcquireMovieCommand{UnitIDs: []uuid.UUID{snap.UnitID}, RootID: snap.RootID, CandidateID: best.ID, DownloaderID: snap.SelectedDownloaderID, SubscriptionID: &sub.ID, IntentVersion: sub.IntentVersion, PolicyID: snap.PolicyID, PolicyRevision: round.SnapshotPolicyRevision, UnitMetadataVersion: snap.UnitMetadataVersion, MediaMetadataVersion: snap.MediaMetadataVersion}, root, rules)
	if errors.Is(err, ErrAlreadyOwned) {
		return finish("completed", "already_owned")
	}
	if errors.Is(err, ErrLocalReuseRequired) {
		return finish("expired", "local_reuse_required")
	}
	if errors.Is(err, ErrOwnershipUncertain) {
		return finish("expired", "ownership_unknown")
	}
	if errors.Is(err, ErrUnitAlreadyReserved) {
		return finish("expired", "reservation_active")
	}
	if err != nil {
		return nil, err
	}
	report.OperationID = result.OperationID
	report.SelectedCandidateID = &result.SourceCandidateID
	if result.Status == "attached" {
		actual, e := q.GetCandidateByID(ctx, result.SourceCandidateID)
		if e != nil {
			return nil, e
		}
		var p quality.Profile
		if json.Unmarshal(actual.QualityProfile, &p) != nil {
			return nil, ErrOwnershipUncertain
		}
		report.BestScore = quality.EvaluateCandidate(rules, actual.Title, p).Score
	}
	return finish("completed", "")
}
func compactJSON(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	out, _ := json.Marshal(v)
	return out
}
