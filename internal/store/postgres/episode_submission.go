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
)

// LockEpisodeExecutionUser runs before policy/subscription/root locks in every
// controlled episode transaction. Movie operations have no episode authority.
func LockEpisodeExecutionUser(ctx context.Context, q *db.Queries, op uuid.UUID) error {
	record, e := q.GetEpisodeExecutionAuthority(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return ports.ErrUnavailable
	}
	var frozen ports.EpisodeExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || !frozen.ValidNumberingShape() || frozen.MediaType != "tv" || record.ActorID != frozen.ActorID {
		return ports.ErrDownloadDemandChanged
	}
	actor := &ports.Actor{ID: frozen.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	if e = lockAcquisitionUser(ctx, q, actor); e != nil {
		return ports.ErrDownloadDemandChanged
	}
	return nil
}

// Called only after account/demand/root/unit/operation/media/indexer/candidate authority locks.
// Never compare a new post-reservation input stamp to the old review or re-sign it.
func ValidateEpisodeExecutionInputs(ctx context.Context, q *db.Queries, c ports.DownloadSubmissionCommand, sub db.Subscription, root db.LibraryRoot, unit db.MediaUnit, candidate db.ReleaseCandidate, f ports.DownloadDemandFence) error {
	record, e := q.GetEpisodeExecutionAuthority(ctx, c.OperationID)
	if e != nil {
		return ports.ErrDownloadDemandChanged
	}
	var frozen ports.EpisodeExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || !frozen.ValidNumberingShape() {
		return ports.ErrDownloadDemandChanged
	}
	if frozen.ReviewID != record.ReviewID || frozen.SubscriptionID != record.SubscriptionID || frozen.CandidateID != record.CandidateID || frozen.UnitID != record.UnitID || frozen.RootID != record.RootID || frozen.ClientID != record.ClientID || frozen.ActorID != record.ActorID {
		return ports.ErrDownloadDemandChanged
	}
	if frozen.SubscriptionID != sub.ID || sub.State != "active" || sub.UserID != frozen.ActorID || sub.IntentVersion != frozen.IntentVersion || sub.QualityPolicyID != frozen.PolicyID || sub.QualityPolicyRevision != frozen.PolicyRevision || sub.EpisodeMappingRevision.Int64 != frozen.MappingRevision || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != frozen.ClientID || sub.RootID != root.ID || sub.MediaID != frozen.MediaID {
		return ports.ErrDownloadDemandChanged
	}
	if frozen.MediaType != "tv" || root.MediaType != "tv" || root.ID != frozen.RootID || root.Generation != frozen.RootGeneration || unit.ID != frozen.UnitID || unit.UnitKey != frozen.UnitKey || unit.Retired || unit.MetadataVersion != frozen.UnitVersion || unit.MediaID != frozen.MediaID || !unit.AirDate.Valid {
		return ports.ErrDownloadDemandChanged
	}
	parent, e := q.GetMediaByID(ctx, unit.MediaID)
	if e != nil || parent.MediaType != "tv" || parent.MetadataVersion != frozen.MediaVersion {
		return ports.ErrDownloadDemandChanged
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	if unit.AirDate.Time.After(now.Time) {
		return ports.ErrDownloadDemandChanged
	}
	if candidate.ID != frozen.CandidateID || candidate.IndexerID != frozen.IndexerID || candidate.SourceConfigVersion != frozen.SourceVersion || quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile) != frozen.SourceStamp || f.SourceStamp != frozen.SourceStamp || f.UnitID != frozen.UnitID || f.RootID != frozen.RootID || f.ActorID != frozen.ActorID.String() || f.ActorType != ports.ActorTypeUser {
		return ports.ErrDownloadDemandChanged
	}
	review, e := q.GetEpisodeCandidateReview(ctx, frozen.ReviewID)
	if e != nil || review.InputStamp != frozen.ReviewInputStamp || review.ActorID != frozen.ActorID || review.SubscriptionID != frozen.SubscriptionID || review.UnitID != frozen.UnitID || review.CandidateID != frozen.CandidateID || review.UnitVersion != frozen.UnitVersion || review.MediaVersion != frozen.MediaVersion || review.IntentVersion != frozen.IntentVersion || review.MappingRevision != frozen.MappingRevision || review.ParserVersion != frozen.ParserVersion {
		return ports.ErrDownloadDemandChanged
	}
	client, e := q.LockEpisodeSubmissionClient(ctx, frozen.ClientID)
	if e != nil || c.ClientID != frozen.ClientID || episodeClientStamp(client) != frozen.ClientStamp {
		return ports.ErrDownloadDemandChanged
	}
	actor := &ports.Actor{ID: frozen.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	v, e := readEpisodeCandidate(ctx, q, actor, sub.ID, candidate.ID)
	if e != nil {
		return ports.ErrDownloadDemandChanged
	}
	if !v.Current || !v.Mapping.Current || v.Mapping.Mode != frozen.MappingMode || v.Mapping.MappingRevision != frozen.MappingRevision || v.SourceVersion != frozen.SourceVersion {
		return ports.ErrDownloadDemandChanged
	}
	// New operations freeze the approved mapping provenance as well as its revision.
	// Existing schema40 operations keep their original evidence; absence cannot enable mapped execution.
	if frozen.MappingInputStamp != "" {
		if v.Mapping.InputStamp != frozen.MappingInputStamp || v.Mapping.SourceHash != frozen.MappingSourceHash || (v.Mapping.ValidUntil == nil) != (frozen.MappingValidUntil == nil) {
			return ports.ErrDownloadDemandChanged
		}
		if frozen.MappingValidUntil != nil && (!v.Mapping.ValidUntil.Equal(*frozen.MappingValidUntil) || !frozen.MappingValidUntil.After(now.Time)) {
			return ports.ErrDownloadDemandChanged
		}
	}
	units := []media.NumberingUnit{}
	for _, u := range v.CanonicalUnits {
		units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Season: u.Season, Episode: u.Episode, Key: u.Key, Version: u.Version, Retired: u.Retired})
	}
	if frozen.MappingMode != "standard" {
		a, _ := json.Marshal(v.Mapping.Rows)
		b, _ := json.Marshal(frozen.MappingRows)
		if string(a) != string(b) {
			return ports.ErrDownloadDemandChanged
		}
	}
	rows, _ := frozen.ResolveNumbering(media.RecognizeRelease(candidate.Title), units)
	if len(rows) != 1 || rows[0].UnitID != frozen.UnitID || rows[0].UnitKey != frozen.UnitKey || rows[0].UnitVersion != frozen.UnitVersion || rows[0].EpisodeID != frozen.EpisodeID {
		return ports.ErrDownloadDemandChanged
	}
	return nil
}
