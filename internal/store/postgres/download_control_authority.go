package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DownloadControlAuthority seals the original dedicated authority and its current
// protections. It grants no submission, organization, or reservation mutation.
type DownloadControlAuthority struct {
	Scope       string
	ClientStamp string
	OwnerID     uuid.UUID
	Digest      string
}

func LockDownloadControlAuthority(ctx context.Context, tx pgx.Tx, actor *ports.Actor, task db.DownloadTask, scope string, decoders ...ports.CredentialDecoder) (out DownloadControlAuthority, err error) {
	out.Scope = scope
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && ctx.Err() == nil {
			err = ports.ErrDownloadControlConflict
		}
	}()
	if task.State != "tracking" && (scope != "movie-upgrade" || task.State != "submitting") {
		return out, ports.ErrDownloadControlConflict
	}
	q := db.New(tx)
	var material any
	switch scope {
	case "automatic-tv":
		a, e := automaticTVSubmissionAuthority(ctx, q, task.OperationID)
		if e != nil {
			return out, e
		}
		out.OwnerID = a.Target.OwnerID
		out.ClientStamp = a.Target.ClientStamp
		if _, e = automaticTVLockOwner(ctx, q, out.OwnerID); e != nil {
			return out, e
		}
		if e = controlCurrentTarget(ctx, tx, a.Target.SubscriptionID, out.OwnerID, a.Target.MediaID, a.Target.RootID, a.Target.ClientID, a.Target.IntentVersion, a.Target.RootGeneration, a.Target.MediaVersion, a.Target.PolicyID, a.Target.PolicyRevision); e != nil {
			return out, e
		}
		t := a.Target
		unit, e := q.LockAcquisitionUnit(ctx, t.UnitID)
		if e != nil {
			return out, e
		}
		wanted, e := q.LockSearchWanted(ctx, t.WantedID)
		if e != nil {
			return out, e
		}
		if unit.Retired || unit.MetadataVersion != t.UnitVersion || unit.UnitKey != t.UnitKey || unit.MediaID != t.MediaID || !unit.SeasonNumber.Valid || unit.SeasonNumber.Int32 != t.Season || !unit.EpisodeNumber.Valid || unit.EpisodeNumber.Int32 != t.Episode || unit.AbsoluteNumber.Valid || !unit.AirDate.Valid || !unit.AirDate.Time.Equal(t.AirDate) || wanted.SubscriptionID != t.SubscriptionID || wanted.UnitID != t.UnitID || wanted.IntentVersion != t.IntentVersion || wanted.IsSkipped || wanted.UserSkipped || wanted.ScopeExcluded {
			return out, ports.ErrDownloadControlConflict
		}
		sub, e := q.GetSubscriptionByID(ctx, t.SubscriptionID)
		if e != nil {
			return out, e
		}
		if sub.UpgradeMode != "disabled" || sub.EpisodeMappingRevision.Int64 != t.MappingRevision || sub.EpisodeMappingRevision.Valid != (t.MappingRevision > 0) {
			return out, ports.ErrDownloadControlConflict
		}
		root, e := q.LockAcquisitionRoot(ctx, t.RootID)
		if e != nil {
			return out, e
		}
		if root.HealthRevision != t.RootHealthRevision || automaticTVDigest([]byte(root.Path)) != a.RootPathDigest {
			return out, ports.ErrDownloadControlConflict
		}
		current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: a.IndexerID, ConfigVersion: a.SourceConfigVersion})
		if e != nil {
			return out, e
		}
		if !current {
			return out, ports.ErrDownloadControlConflict
		}
		if e = automaticTVSubmissionOriginal(ctx, tx, a); e != nil {
			return out, e
		}
		if e = automaticTVSubmissionProtections(ctx, tx, q, a, task.ID); e != nil {
			return out, e
		}
		var prepared []byte
		e = tx.QueryRow(ctx, `SELECT to_jsonb(p) FROM automatic_tv_submission_preparation p JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id WHERE p.operation_id=$1 AND p.task_id=$2 AND p.info_hash=$3 AND p.authority_hash=$4 AND x.finished_at IS NOT NULL`, task.OperationID, task.ID, task.InfoHash, a.DecisionStamp).Scan(&prepared)
		if e != nil {
			return out, e
		}
		var row struct {
			Proof automaticTVPreparedProof `json:"proof"`
		}
		if json.Unmarshal(prepared, &row) != nil || row.Proof.SavePath != task.SavePath || row.Proof.InfoHash != task.InfoHash || row.Proof.Authority.DecisionStamp != a.DecisionStamp || task.ClientID != t.ClientID || task.CandidateID != a.CandidateID {
			return out, ports.ErrDownloadControlConflict
		}
		material = []any{a, json.RawMessage(prepared), unit, wanted, sub}
	case "complete-tv":
		record, e := q.GetCompleteTVExecutionAuthority(ctx, task.OperationID)
		if e != nil {
			return out, e
		}
		var a ports.CompleteTVCoverageExecutionSnapshot
		if json.Unmarshal(record.Snapshot, &a) != nil || a.Kind != "reviewed_complete_tv_coverage_v1" || a.ActorID != record.ActorID || len(a.Units) < 2 || len(a.Units) > 1000 || a.ClientID != task.ClientID || a.CandidateID != task.CandidateID {
			return out, ports.ErrDownloadControlConflict
		}
		out.OwnerID = a.ActorID
		out.ClientStamp = a.ClientStamp
		owner, e := automaticTVLockOwner(ctx, q, out.OwnerID)
		if e != nil {
			return out, e
		}
		if e = controlCurrentTarget(ctx, tx, a.SubscriptionID, out.OwnerID, a.MediaID, a.RootID, a.ClientID, a.IntentVersion, a.RootGeneration, a.MediaVersion, a.PolicyID, a.PolicyRevision); e != nil {
			return out, e
		}
		current, e := readTVCoverageCandidate(ctx, q, owner, a.SubscriptionID, a.CandidateID)
		if e != nil {
			return out, e
		}
		sort.Slice(current.Rows, func(i, j int) bool { return current.Rows[i].UnitID.String() < current.Rows[j].UnitID.String() })
		units := append(a.Units[:0:0], a.Units...)
		sort.Slice(units, func(i, j int) bool { return units[i].UnitID.String() < units[j].UnitID.String() })
		if !current.CanReview || current.MaterialStamp != a.MaterialStamp || completeTVJSONDigest(current.Material) != completeTVJSONDigest(a.Material) || coverageDigest(current.Rows) != coverageDigest(units) {
			return out, ports.ErrDownloadControlConflict
		}
		p, e := q.GetCompleteTVPreparation(ctx, task.OperationID)
		if e != nil {
			return out, e
		}
		var proof completeTVPreparedProof
		if json.Unmarshal(p.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || p.TaskID != task.ID || p.InfoHash != task.InfoHash || proof.InfoHash != task.InfoHash || proof.SavePath != task.SavePath || proof.MaterialStamp != a.MaterialStamp || len(proof.Files) != len(units) {
			return out, ports.ErrDownloadControlConflict
		}
		declared, e := q.ListCompleteTVPreparedUnits(ctx, task.ID)
		if e != nil {
			return out, e
		}
		demands, e := q.ListRequirementsForOperation(ctx, task.OperationID)
		if e != nil {
			return out, e
		}
		lanes, e := q.ListCompleteTVPreparationLanes(ctx, task.OperationID)
		if e != nil {
			return out, e
		}
		reserved, e := q.ListCompleteTVPreparationReservations(ctx, task.OperationID)
		if e != nil {
			return out, e
		}
		if len(declared) != len(units) || len(demands) != len(units) || len(lanes) != len(units) || len(reserved) != len(units) {
			return out, ports.ErrDownloadControlConflict
		}
		sort.Slice(demands, func(i, j int) bool { return demands[i].UnitID.String() < demands[j].UnitID.String() })
		sort.Slice(proof.Files, func(i, j int) bool { return proof.Files[i].Unit.UnitID.String() < proof.Files[j].Unit.UnitID.String() })
		for i, u := range units {
			d, l, r, f, p := demands[i], lanes[i], reserved[i], proof.Files[i], declared[i]
			fence, _ := json.Marshal(ports.DownloadDemandFence{PolicyID: a.PolicyID, PolicyRevision: a.PolicyRevision, SubscriptionID: &a.SubscriptionID, IntentVersion: a.IntentVersion, RootGeneration: a.RootGeneration, UnitMetadataVersion: u.UnitVersion, MediaMetadataVersion: a.MediaVersion, CandidateID: a.CandidateID, RootID: a.RootID, UnitID: u.UnitID, MediaID: a.MediaID, ActorID: a.ActorID.String(), ActorType: ports.ActorTypeUser, SourceStamp: a.SourceStamp})
			if f.Unit != u || p.UnitID != u.UnitID || p.UnitVersion != u.UnitVersion || p.ProviderEpisodeID != u.EpisodeID || p.VideoPath != f.Path || p.VideoSize != f.Size || d.UnitID != u.UnitID || d.RootID != a.RootID || d.ActorID != a.ActorID.String() || d.ActorType != "user" || d.State != "pending" || completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(fence) || l.UnitID != u.UnitID || l.SourceRootID != a.RootID || l.SourceGeneration != a.RootGeneration || l.SourceStamp != a.SourceStamp || l.CandidateID != a.CandidateID || !l.TaskID.Valid || uuid.UUID(l.TaskID.Bytes) != task.ID || l.ReleasedAt.Valid || r.UnitID != u.UnitID || r.RootID != a.RootID || r.CandidateID != a.CandidateID || r.State != "dispatched" || r.Purpose != "manual_acquisition" {
				return out, ports.ErrDownloadControlConflict
			}
		}
		material = []any{record, p, declared, lanes, reserved, current.Material}
	case "tv-episode-upgrade":
		var decoder ports.CredentialDecoder
		if len(decoders) == 1 {
			decoder = decoders[0]
		}
		var e error
		out, material, e = lockTVEpisodeUpgradeControlAuthority(ctx, tx, task, decoder)
		if e != nil {
			return out, e
		}
	case "movie-upgrade":
		var owner uuid.UUID
		if e := tx.QueryRow(ctx, `SELECT actor_id FROM movie_upgrade_authority WHERE operation_id=$1`, task.OperationID).Scan(&owner); e != nil {
			return out, e
		}
		out.OwnerID = owner
		original, e := automaticTVLockOwner(ctx, q, owner)
		if e != nil {
			return out, e
		}
		store := &UpgradePreparationStore{}
		session := &movieUpgradeTrackingSession{base: &movieUpgradeSendSession{store: store, operation: task.OperationID}}
		_, _, verify, e := session.load(ctx, tx, original)
		if e != nil {
			return out, e
		}
		if e = verify(); e != nil {
			return out, e
		}
		if session.descriptor.Identity.TaskID != task.ID || session.descriptor.Identity.InfoHash != task.InfoHash || session.descriptor.Identity.SavePath != task.SavePath || session.descriptor.Identity.ClientID != task.ClientID {
			return out, ports.ErrDownloadControlConflict
		}
		if e = controlMovieCurrent(ctx, tx, task, session.descriptor); e != nil {
			return out, e
		}
		digest, e := sendReturnCurrentDigest(ctx, tx, task.OperationID)
		if e != nil {
			return out, e
		}
		out.ClientStamp = session.descriptor.Identity.ClientDigest
		material = []any{session.base.armExpected, session.descriptor, digest}
	default:
		return out, ports.ErrDownloadControlConflict
	}
	if actor.Role != ports.RoleAdmin && actor.ID != out.OwnerID.String() {
		return out, ports.ErrForbidden
	}
	protection, e := trackingProtection(ctx, tx, ports.CompleteTVTrackingRequest{OperationID: task.OperationID, TaskID: task.ID})
	if e != nil {
		return out, e
	}
	bound, e := q.GetBoundSubmissionClient(ctx, task.ClientID)
	if e != nil {
		return out, e
	}
	if episodeClientStamp(bound.DownloadClient) != out.ClientStamp {
		return out, ports.ErrDownloadControlConflict
	}
	out.ClientStamp = bound.ConfigStamp
	contextDigest, e := controlContextDigest(ctx, tx, task, scope)
	if e != nil {
		return out, e
	}
	out.Digest = coverageDigest([]any{scope, material, protection, contextDigest})
	return out, nil
}

func controlCurrentTarget(ctx context.Context, tx pgx.Tx, subID, owner, mediaID, rootID, clientID uuid.UUID, intent, generation, mediaVersion int64, policyID string, revision int64) error {
	q := db.New(tx)
	policy, e := q.LockSearchPolicy(ctx, policyID)
	if e != nil {
		return e
	}
	sub, e := q.LockSearchSubscription(ctx, subID)
	if e != nil {
		return e
	}
	root, e := q.LockAcquisitionRoot(ctx, rootID)
	if e != nil {
		return e
	}
	if _, e = q.LockScopeMediaUnits(ctx, mediaID); e != nil {
		return e
	}
	m, e := q.LockSearchMedia(ctx, mediaID)
	if e != nil {
		return e
	}
	if sub.UserID != owner || sub.State != "active" || sub.IntentVersion != intent || sub.MediaID != mediaID || sub.RootID != rootID || sub.QualityPolicyID != policyID || sub.QualityPolicyRevision != revision || policy.CurrentRevision != revision || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != clientID || root.Generation != generation || root.Health != "online" || !root.BaselineCompleted || root.MediaType != m.MediaType || m.MetadataVersion != mediaVersion {
		return ports.ErrDownloadControlConflict
	}
	return nil
}
