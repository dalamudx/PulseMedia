package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Keep the sender/observer lock order, validating the successful predecessor
// instead of fabricating a running lease or a physical observation permit.
func automaticTVWatchCurrent(ctx context.Context, tx pgx.Tx, q *db.Queries, r ports.AutomaticTVObservationHintRequest, a ports.AutomaticTVExecutionAuthority) (bool, string, error) {
	t := a.Target
	owner, e := q.LockAcquisitionCommandUser(ctx, t.OwnerID)
	if e != nil {
		return false, "", e
	}
	if _, e = q.LockJobForWrite(ctx, r.PriorJobID); e != nil {
		return false, "", e
	}
	if e = automaticTVWatchPriorJob(ctx, tx, r); e != nil {
		return false, "", e
	}
	policy, e := q.LockSearchPolicy(ctx, t.PolicyID)
	if e != nil {
		return false, "", e
	}
	sub, e := q.LockSearchSubscription(ctx, t.SubscriptionID)
	if e != nil {
		return false, "", e
	}
	root, e := q.LockAcquisitionRoot(ctx, t.RootID)
	if e != nil {
		return false, "", e
	}
	unit, e := q.LockAcquisitionUnit(ctx, t.UnitID)
	if e != nil {
		return false, "", e
	}
	wanted, e := q.LockSearchWanted(ctx, t.WantedID)
	if e != nil {
		return false, "", e
	}
	if e = q.LockDownloadAcquisition(ctx, a.OperationID); e != nil {
		return false, "", e
	}
	m, e := q.LockSearchMedia(ctx, t.MediaID)
	if e != nil {
		return false, "", e
	}
	idx, e := q.LockSearchIndexer(ctx, a.IndexerID)
	if e != nil {
		return false, "", e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, a.CandidateID)
	if e != nil {
		return false, "", e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM download_client WHERE id=$1 FOR UPDATE`, t.ClientID); e != nil {
		return false, "", e
	}
	client, e := q.GetDownloadClientByID(ctx, t.ClientID)
	if e != nil {
		return false, "", e
	}
	stamp := episodeClientStamp(client)
	eligible := owner.IsActive && (owner.Role == ports.RoleMember || owner.Role == ports.RoleAdmin) &&
		sub.UserID == t.OwnerID && sub.State == "active" && sub.IntentVersion == t.IntentVersion && sub.MediaID == t.MediaID && sub.RootID == t.RootID &&
		sub.QualityPolicyID == t.PolicyID && sub.QualityPolicyRevision == t.PolicyRevision && policy.CurrentRevision == t.PolicyRevision && sub.UpgradeMode == "disabled" &&
		sub.DownloaderID.Valid && uuid.UUID(sub.DownloaderID.Bytes) == t.ClientID && sub.EpisodeMappingRevision.Int64 == t.MappingRevision && sub.EpisodeMappingRevision.Valid == (t.MappingRevision > 0) &&
		root.MediaType == "tv" && root.Health == "online" && root.BaselineCompleted && root.Generation == t.RootGeneration && root.HealthRevision == t.RootHealthRevision && automaticTVDigest([]byte(root.Path)) == a.RootPathDigest &&
		unit.MediaID == t.MediaID && unit.MetadataVersion == t.UnitVersion && unit.UnitKey == t.UnitKey && !unit.Retired && unit.SeasonNumber.Valid && unit.SeasonNumber.Int32 == t.Season && unit.EpisodeNumber.Valid && unit.EpisodeNumber.Int32 == t.Episode && !unit.AbsoluteNumber.Valid && unit.AirDate.Valid && unit.AirDate.Time.Equal(t.AirDate) &&
		wanted.SubscriptionID == sub.ID && wanted.UnitID == unit.ID && !wanted.IsSkipped && !wanted.UserSkipped && !wanted.ScopeExcluded && wanted.IntentVersion == t.IntentVersion &&
		m.MediaType == "tv" && m.MetadataVersion == t.MediaVersion && m.Title == t.Title && m.OriginalTitle.String == t.OriginalTitle && m.Year.Valid && m.Year.Int32 == t.Year && t.MappingMode == "standard" && stamp == t.ClientStamp
	rev, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: t.PolicyID, Revision: t.PolicyRevision})
	if e != nil {
		return false, "", e
	}
	eligible = eligible && completeTVJSONDigest(rev.Rules) == completeTVJSONDigest(t.Rules)
	aired, e := q.AutomaticTVAired(ctx, unit.ID)
	if e != nil {
		return false, "", e
	}
	eligible = eligible && aired.Valid && aired.Bool
	identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return false, "", e
	}
	identity := false
	for _, i := range identities {
		identity = identity || (i.Namespace == "tmdb.tv" && i.ExternalID == strconv.FormatInt(t.TMDBID, 10))
	}
	eligible = eligible && identity
	ep, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return false, "", e
	}
	eligible = eligible && e == nil && ep.ExternalID == t.EpisodeID && ep.TmdbSeriesID == t.TMDBID
	if t.MappingRevision > 0 {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return false, "", e
		}
		eligible = eligible && e == nil && mapping.Revision == t.MappingRevision && mapping.Mode == "standard" && mapping.MediaID == m.ID && mapping.MediaVersion == m.MetadataVersion
	}
	var bound []uuid.UUID
	eligible = eligible && json.Unmarshal(sub.IndexerIds, &bound) == nil && slices.Contains(bound, a.IndexerID) && t.IndexerVersions[a.IndexerID.String()] == a.SourceConfigVersion
	current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: idx.ID, ConfigVersion: a.SourceConfigVersion})
	if e != nil {
		return false, "", e
	}
	eligible = eligible && current
	row := ports.AutomaticTVRoundCandidate{Candidate: automaticTVSearchCandidate(candidate), QueryID: a.QueryID, QueryParametersHash: a.QueryParametersHash, QuerySourceStamp: a.SourceStamp}
	_, candidateOK := automaticTVEligible(t, row)
	eligible = eligible && candidateOK && candidate.IndexerID == a.IndexerID
	var same bool
	e = tx.QueryRow(ctx, `SELECT f.title=c.title AND f.guid=c.guid AND f.info_hash IS NOT DISTINCT FROM c.info_hash AND f.link_encrypted=c.link_encrypted AND f.quality_profile=c.quality_profile AND f.size_bytes=c.size_bytes AND f.source_config_version=c.source_config_version FROM automatic_tv_query_candidate f JOIN release_candidate c ON c.id=f.candidate_id WHERE f.query_id=$1 AND f.candidate_id=$2`, a.QueryID, a.CandidateID).Scan(&same)
	if e != nil {
		return false, "", e
	}
	eligible = eligible && same
	var pathmap map[string]string
	eligible = eligible && json.Unmarshal(client.RemotePathMap, &pathmap) == nil && naming.TranslateRemotePath(pathmap, root.Path) != "" && (client.ClientType == "qbittorrent" || client.ClientType == "transmission")
	var clear bool
	e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=$1 AND released_at IS NULL AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched') AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM download_requirement WHERE unit_id=$1 AND state='pending' AND operation_id<>$2)
 AND NOT EXISTS(SELECT 1 FROM library_file_unit fu JOIN library_file f ON f.id=fu.file_id WHERE fu.unit_id=$1 AND fu.retired_at IS NULL AND f.retired_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM library_reuse_unit WHERE unit_id=$1 AND active)`, t.UnitID, a.OperationID).Scan(&clear)
	if e != nil {
		return false, "", e
	}
	return eligible && clear, stamp, nil
}
