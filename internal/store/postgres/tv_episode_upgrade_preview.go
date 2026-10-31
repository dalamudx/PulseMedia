package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strconv"
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
)

// Material is private to the transaction boundary; only Result is public.
type tvEpisodeUpgradeMaterial struct {
	Result            *ports.TVEpisodeUpgradeResult
	Sub               db.Subscription
	Root              db.LibraryRoot
	Unit              db.MediaUnit
	Target            media.AutomaticTVTorrentTarget
	Candidate         db.ReleaseCandidate
	Client            db.DownloadClient
	Source            db.Indexer
	Current           quality.Profile
	Rules             quality.PolicyRules
	FactID            uuid.UUID
	OldRel            string
	OldSize, OldMtime int64
	Snapshot          json.RawMessage
	ValidUntil        time.Time
}

// Read under the caller's transaction, in account/policy/subscription/root/unit
// order. This observation never grants permission to send or organize a file.
func readTVEpisodeUpgradePreview(ctx context.Context, tx pgx.Tx, actor *ports.Actor, req ports.TVEpisodeUpgradePreviewRequest, decoder ports.CredentialDecoder) (*tvEpisodeUpgradeMaterial, error) {
	if e := episodeReviewActor(actor); e != nil {
		return nil, e
	}
	if req.SubscriptionID == uuid.Nil || req.UnitID == uuid.Nil || req.CurrentFileID == uuid.Nil || req.CandidateID == uuid.Nil {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	q := db.New(tx)
	if e := lockAcquisitionUser(ctx, q, actor); e != nil {
		return nil, e
	}
	hint, e := q.GetSubscriptionByID(ctx, req.SubscriptionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if !actor.CanAccessUser(hint.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	policy, e := q.LockUpgradePolicy(ctx, hint.QualityPolicyID)
	if e != nil {
		return nil, e
	}
	sub, e := q.LockUpgradeSubscription(ctx, hint.ID)
	if e != nil {
		return nil, e
	}
	if !actor.CanAccessUser(sub.UserID.String()) {
		return nil, ports.ErrForbidden
	}
	if sub.QualityPolicyID != policy.ID || sub.RootID != hint.RootID || sub.MediaID != hint.MediaID {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: policy.ID, Revision: policy.CurrentRevision})
	if e != nil {
		return nil, e
	}
	m := &tvEpisodeUpgradeMaterial{Sub: sub, Result: &ports.TVEpisodeUpgradeResult{Status: "preview_not_executable", UnitID: req.UnitID, CurrentFileID: req.CurrentFileID, CandidateID: req.CandidateID}}
	hold := func(reason string) {
		if m.Result.Reason == "" {
			m.Result.Reason = reason
		}
	}
	if json.Unmarshal(revision.Rules, &m.Rules) != nil || quality.ValidateRules(m.Rules) != nil {
		return nil, ports.ErrUnavailable
	}
	m.Root, e = q.LockUpgradeRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	units, e := q.ListUpgradeUnits(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	found := false
	for _, u := range units {
		if u.ID == req.UnitID {
			m.Unit = u
			found = true
		}
	}
	if !found {
		return nil, ports.ErrNotFound
	}
	work, e := q.LockSearchMedia(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	u := m.Unit
	if sub.State != "active" {
		hold("subscription_inactive")
	}
	if sub.QualityPolicyRevision != policy.CurrentRevision {
		hold("policy_input_changed")
	}
	if !quality.UpgradeMode(sub.UpgradeMode).Enabled() {
		hold("upgrade_mode_disabled_or_unknown")
	}
	if m.Root.MediaType != "tv" || m.Root.Health != "online" || !m.Root.BaselineCompleted {
		hold("root_unavailable")
	}
	if work.MediaType != "tv" || work.MetadataVersion < 1 || !work.Year.Valid || work.Year.Int32 < 1 || work.Year.Int32 > 9999 || u.Retired || u.MetadataVersion < 1 || !u.SeasonNumber.Valid || !u.EpisodeNumber.Valid || u.AbsoluteNumber.Valid || u.SeasonNumber.Int32 < 1 || u.SeasonNumber.Int32 > 999 || u.EpisodeNumber.Int32 < 1 || u.EpisodeNumber.Int32 > 9999 || u.UnitKey != media.FormatEpisodeUnitKey(int(u.SeasonNumber.Int32), int(u.EpisodeNumber.Int32)) {
		hold("standard_single_episode_required")
	}
	var scope sd.Scope
	scope.Type, scope.IncludeSpecials = sub.ScopeType, sub.IncludeSpecials
	if json.Unmarshal(sub.Seasons, &scope.Seasons) != nil || json.Unmarshal(sub.SelectedUnitIds, &scope.Selected) != nil {
		return nil, ports.ErrUnavailable
	}
	if !sd.ScopeContains(scope, sd.ScopeUnit{ID: u.ID, Season: int(u.SeasonNumber.Int32), Retired: u.Retired, TV: true}) {
		hold("unit_outside_subscription_scope")
	}
	identities, e := q.ListSearchMediaIdentities(ctx, work.ID)
	if e != nil {
		return nil, e
	}
	var tmdb int64
	count := 0
	for _, id := range identities {
		if id.Namespace == "tmdb.tv" {
			count++
			tmdb, e = strconv.ParseInt(id.ExternalID, 10, 64)
			if e != nil {
				tmdb = 0
			}
		}
	}
	if count != 1 || tmdb < 1 {
		hold("tmdb_series_identity_required")
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e != nil || episode.ExternalID < 1 || episode.TmdbSeriesID != tmdb {
		hold("tmdb_episode_identity_required")
	}
	m.Target = media.AutomaticTVTorrentTarget{Unit: media.NumberingUnit{ID: u.ID, EpisodeID: episode.ExternalID, Season: int(u.SeasonNumber.Int32), Episode: int(u.EpisodeNumber.Int32), Key: u.UnitKey, Version: u.MetadataVersion, Retired: u.Retired}, Title: work.Title, OriginalTitle: work.OriginalTitle.String, Year: work.Year.Int32, TMDBID: tmdb}
	if sub.EpisodeMappingRevision.Valid {
		mapping, err := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err != nil || mapping.Revision < 1 || mapping.Mode != "standard" || mapping.MediaID != work.ID || mapping.MediaVersion != work.MetadataVersion {
			hold("standard_mapping_required")
		}
	}
	files, e := q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{RootID: m.Root.ID, UnitID: u.ID})
	if e != nil {
		return nil, e
	}
	if len(files) != 1 {
		hold("current_file_ambiguous")
	}
	found = false
	for _, f := range files {
		if f.ID == req.CurrentFileID {
			found = true
			m.OldRel, m.OldSize, m.OldMtime = f.RelPath, f.SizeBytes, f.MtimeNs
			if f.UnitCount != 1 {
				hold("current_file_multi_unit")
			}
		}
	}
	if !found {
		return nil, ports.ErrNotFound
	}
	if !tvEpisodeUpgradeVideoPath(m.OldRel) || m.OldSize < 1 {
		hold("current_video_required")
	}
	facts, e := q.ListUpgradeFacts(ctx, db.ListUpgradeFactsParams{SourceFileID: pgtype.UUID{Bytes: req.CurrentFileID, Valid: true}, UnitID: u.ID, RootID: pgtype.UUID{Bytes: m.Root.ID, Valid: true}})
	if e != nil {
		return nil, e
	}
	if len(facts) != 1 {
		hold("current_verified_fact_required")
	} else {
		m.FactID = facts[0].ID
		if json.Unmarshal(facts[0].QualityProfile, &m.Current) != nil {
			hold("current_quality_unknown")
		}
	}
	// User intent is authoritative; cached Wanted projections can be invalidated by
	// this operation's own reservation and must not become a revalidation input.
	wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil {
		return nil, e
	}
	found = false
	for _, w := range wanted {
		if w.UnitID == u.ID {
			found = true
			if w.UserSkipped || w.ScopeExcluded || w.IntentVersion != sub.IntentVersion {
				hold("wanted_input_changed")
			}
		}
	}
	if !found {
		hold("wanted_input_changed")
	}
	var protected bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_reuse_unit WHERE unit_id=$1 AND active)`, u.ID).Scan(&protected); e != nil {
		return nil, e
	}
	if protected {
		hold("unit_protected")
	}
	candidate, e := q.GetCandidateByID(ctx, req.CandidateID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	m.Source, e = q.LockSearchIndexer(ctx, candidate.IndexerID)
	if e != nil {
		return nil, e
	}
	var modes ports.IndexerExecutionModes
	m.Source, modes, e = ResolveIndexerConfiguration(ctx, q, decoder, m.Source)
	if e != nil {
		return nil, e
	}
	if !modes.LegacyV1 && modes.Protocol != ports.IndexerProtocolTorznab {
		hold("source_protocol_unsupported")
	}
	var sourceIDs []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &sourceIDs) != nil {
		return nil, ports.ErrUnavailable
	}
	found = false
	for _, id := range sourceIDs {
		found = found || id == m.Source.ID
	}
	if !found || m.Source.Health != "online" || candidate.SourceConfigVersion < 1 || candidate.SourceConfigVersion != m.Source.ConfigVersion {
		hold("source_input_changed")
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
	if !sub.DownloaderID.Valid {
		hold("downloader_required")
	} else {
		clientID := uuid.UUID(sub.DownloaderID.Bytes)
		if _, e = q.GetSearchDownloaderSnapshot(ctx, clientID); e != nil {
			return nil, e
		}
		m.Client, e = q.GetDownloadClientByID(ctx, clientID)
		if e != nil {
			return nil, e
		}
		if m.Client.ClientType != "qbittorrent" && m.Client.ClientType != "transmission" {
			hold("downloader_unsupported")
		}
	}
	m.Candidate, e = q.LockUpgradeCandidate(ctx, req.CandidateID)
	if e != nil {
		return nil, e
	}
	if m.Candidate.IndexerID != m.Source.ID || m.Candidate.SourceConfigVersion != m.Source.ConfigVersion {
		hold("source_input_changed")
	}
	window, e := q.ReadUpgradeSourceWindow(ctx, req.CandidateID)
	if e != nil {
		return nil, e
	}
	var profile quality.Profile
	if json.Unmarshal(m.Candidate.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) || profile.ResolutionParserVersion != quality.ResolutionTokenParserVersion {
		hold("candidate_quality_unknown")
	}
	if profile != media.Recognize(m.Candidate.Title).Quality {
		hold("candidate_quality_evidence_changed")
	}
	if m.Candidate.SizeBytes < 1 || len(m.Candidate.LinkEncrypted) == 0 {
		hold("candidate_reference_required")
	}
	if m.Candidate.MatchedMediaID.Valid && uuid.UUID(m.Candidate.MatchedMediaID.Bytes) != work.ID || m.Candidate.MatchedUnitID.Valid && uuid.UUID(m.Candidate.MatchedUnitID.Bytes) != u.ID {
		hold("candidate_identity_mismatch")
	}
	if media.DecideAutomaticTVIdentity(m.Candidate.Title, work.Title, work.OriginalTitle.String, int(work.Year.Int32), tmdb).State != "eligible" {
		hold("candidate_identity_mismatch")
	}
	rows, _ := media.ResolveStandardSingleEpisode(media.RecognizeRelease(m.Candidate.Title), []media.NumberingUnit{m.Target.Unit})
	if len(rows) != 1 || rows[0].UnitID != u.ID || rows[0].EpisodeID != episode.ExternalID || rows[0].UnitKey != u.UnitKey || rows[0].UnitVersion != u.MetadataVersion {
		hold("candidate_standard_single_episode_required")
	}
	if !quality.EvaluateCandidate(m.Rules, m.Candidate.Title, profile).Passed {
		hold("candidate_policy_rejected")
	}
	// Hash complete protected rows in memory; neither the decrypted URL nor any
	// credential/reference bytes enter Snapshot or the public Result.
	var raw json.RawMessage
	raw, e = q.ReadTVEpisodeUpgradeEvidence(ctx, db.ReadTVEpisodeUpgradeEvidenceParams{ActorID: uuid.MustParse(actor.ID), SubscriptionID: sub.ID, PolicyID: policy.ID, PolicyRevision: policy.CurrentRevision, RootID: m.Root.ID, MediaID: work.ID, UnitID: u.ID, FileID: req.CurrentFileID, CandidateID: req.CandidateID})
	if e != nil {
		return nil, e
	}
	digest := coverageDigest([]any{"tv.episode.upgrade.preview.v1", completeTVJSONDigest(raw), m.Source, modes, episodeClientStamp(m.Client), media.EpisodeParserVersion, media.AutomaticTVIdentityRuleVersion, media.CandidateDecisionVersion})
	m.Snapshot, e = json.Marshal(struct {
		Kind            string `json:"kind"`
		ProtectedDigest string `json:"protected_digest"`
		ClientDigest    string `json:"client_digest"`
	}{"tv_episode_upgrade_preview_v1", digest, episodeClientStamp(m.Client)})
	if e != nil {
		return nil, e
	}
	m.Result.Version = coverageDigest(m.Snapshot)
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if !u.AirDate.Valid || u.AirDate.Time.After(now.Time) {
		hold("episode_not_aired")
	}
	m.ValidUntil = window.Time
	if !window.Valid || !window.Time.After(now.Time) {
		hold("candidate_source_expired")
	}
	for _, f := range facts {
		if f.ExpiresAt.Valid {
			if m.ValidUntil.IsZero() || f.ExpiresAt.Time.Before(m.ValidUntil) {
				m.ValidUntil = f.ExpiresAt.Time
			}
			if !f.ExpiresAt.Time.After(now.Time) {
				hold("current_fact_expired")
			}
		}
	}
	decision := quality.EvaluateUpgradeForMode(m.Rules, m.Current, profile, quality.UpgradeMode(sub.UpgradeMode))
	m.Result.Comparable = decision.ScoresKnown
	if decision.ScoresKnown {
		m.Result.CurrentScore = &decision.CurrentScore
		m.Result.CandidateScore = &decision.NewScore
	}
	m.Result.Improvement = m.Result.Reason == "" && decision.ShouldUpgrade
	if m.Result.Reason == "" && !decision.ShouldUpgrade {
		m.Result.Reason = decision.Reason
	}
	return m, nil
}

func tvEpisodeUpgradeVideoPath(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\:") {
		return false
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".webm", ".ts", ".m2ts":
		return true
	}
	return false
}

// Reservation rows are validated by the command's separate live protection check.
