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

// PrelockCompleteTVOrganization follows the command/worker account -> policy ->
// subscription order before any physical root, unit or acquisition row lock.
func PrelockCompleteTVOrganization(ctx context.Context, q *db.Queries, op uuid.UUID) (bool, error) {
	r, e := q.GetCompleteTVExecutionAuthority(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return true, e
	}
	var f ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(r.Snapshot, &f) != nil || f.ActorID != r.ActorID {
		return true, ports.ErrTransferReviewRequired
	}
	u, e := q.LockAcquisitionCommandUser(ctx, r.ActorID)
	if e != nil {
		return true, completeTVOrganizationReadError(e)
	}
	if !u.IsActive || u.Role != ports.RoleAdmin {
		return true, ports.ErrTransferReviewRequired
	}
	p, e := q.LockSearchPolicy(ctx, f.PolicyID)
	if e != nil {
		return true, completeTVOrganizationReadError(e)
	}
	if p.CurrentRevision != f.PolicyRevision {
		return true, ports.ErrTransferReviewRequired
	}
	sub, e := q.LockSearchSubscription(ctx, f.SubscriptionID)
	if e != nil {
		return true, completeTVOrganizationReadError(e)
	}
	if sub.UserID != r.ActorID || sub.IntentVersion != f.IntentVersion || sub.State != "active" {
		return true, ports.ErrTransferReviewRequired
	}
	return true, nil
}

func completeTVOrganizationReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrTransferReviewRequired
	}
	return err
}

type CompleteTVOrganizationInputs struct {
	Frozen      ports.CompleteTVCoverageExecutionSnapshot
	Task        db.DownloadTask
	Preparation db.CompleteTvSubmissionPreparation
	SavePath    string
	Files       []CompleteTVOrganizationVideo
	Tracking    db.Job
	Receipt     ports.CompleteTVTrackingReceipt
}
type CompleteTVOrganizationVideo struct {
	Path        string
	Size        int64
	UnitID      uuid.UUID
	UnitVersion int64
	UnitKey     string
}

// Caller holds current account/policy/subscription and root locks. This routine
// revalidates all units in UUID order and all current source/configuration inputs.
func ReadCompleteTVOrganizationInputs(ctx context.Context, q *db.Queries, op uuid.UUID, trackingID uuid.UUID, initial bool) (*CompleteTVOrganizationInputs, error) {
	fail := func() (*CompleteTVOrganizationInputs, error) { return nil, ports.ErrTransferReviewRequired }
	r, e := q.GetCompleteTVExecutionAuthority(ctx, op)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	out := &CompleteTVOrganizationInputs{}
	f := &out.Frozen
	if json.Unmarshal(r.Snapshot, f) != nil || f.Kind != "reviewed_complete_tv_coverage_v1" || f.ActorID != r.ActorID || f.RootID != r.RootID || f.SubscriptionID != r.SubscriptionID || f.ClientID != r.ClientID || f.CandidateID != r.CandidateID || f.Mapping.Mode != "standard" || len(f.CompleteSeasons) != 1 || f.CompleteSeasons[0].Season < 1 {
		return fail()
	}
	if _, e = q.LockScopeMediaUnits(ctx, f.MediaID); e != nil {
		return nil, e
	}
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return nil, e
	}
	out.Task, e = q.LockDownloadTaskByOperation(ctx, op)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	task := out.Task
	if task.State != "completed" || task.CandidateID != r.CandidateID || task.ClientID != r.ClientID || task.Progress != 1 || task.TotalBytes != task.DownloadedBytes {
		return fail()
	}
	if initial && (task.OrganizationState != "review_required" || task.TransferPlanID.Valid) {
		return fail()
	}
	if _, e = q.LockSubmissionMedia(ctx, f.MediaID); e != nil {
		return nil, e
	}
	candidate, e := q.GetCandidateByID(ctx, f.CandidateID)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	if _, e = q.LockSubmissionIndexer(ctx, candidate.IndexerID); e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionCandidate(ctx, f.CandidateID); e != nil {
		return nil, e
	}
	if _, e = q.LockEpisodeSubmissionClient(ctx, f.ClientID); e != nil {
		return nil, e
	}
	a := &ports.Actor{ID: f.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	current, e := readTVCoverageCandidate(ctx, q, a, f.SubscriptionID, f.CandidateID)
	if e != nil {
		return nil, e
	}
	sort.Slice(current.Rows, func(i, j int) bool { return current.Rows[i].UnitID.String() < current.Rows[j].UnitID.String() })
	if !current.CanReview || current.MaterialStamp != f.MaterialStamp || coverageDigest(current.Rows) != coverageDigest(f.Units) {
		return fail()
	}
	out.Preparation, e = q.GetCompleteTVPreparation(ctx, op)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	prep := out.Preparation
	var proof completeTVPreparedProof
	if json.Unmarshal(prep.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || prep.TaskID != task.ID || proof.MaterialStamp != f.MaterialStamp || proof.InfoHash != task.InfoHash || prep.InfoHash != task.InfoHash || proof.SavePath != task.SavePath || proof.TotalBytes != task.TotalBytes || len(proof.Files) != len(f.Units) {
		return fail()
	}
	out.SavePath = proof.SavePath
	rows, e := q.ListCompleteTVPreparedUnits(ctx, task.ID)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	if len(rows) != len(proof.Files) {
		return fail()
	}
	for _, v := range proof.Files {
		found := false
		for _, row := range rows {
			if row.UnitID == v.Unit.UnitID && row.UnitVersion == v.Unit.UnitVersion && row.ProviderEpisodeID == v.Unit.EpisodeID && row.UnitKey == v.Unit.UnitKey && row.VideoPath == v.Path && row.VideoSize == v.Size {
				found = true
			}
		}
		if !found {
			return fail()
		}
		out.Files = append(out.Files, CompleteTVOrganizationVideo{v.Path, v.Size, v.Unit.UnitID, v.Unit.UnitVersion, v.Unit.UnitKey})
	}
	if trackingID == uuid.Nil {
		evidence, e := q.GetCompleteTVTransferEvidence(ctx, op)
		if e != nil {
			return nil, completeTVOrganizationReadError(e)
		}
		trackingID = evidence.TrackingJobID
	}
	out.Tracking, e = q.LockCompleteTVOrganizationTracking(ctx, trackingID)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	j := out.Tracking
	var payload struct {
		TaskID      uuid.UUID `json:"task_id"`
		OperationID uuid.UUID `json:"operation_id"`
	}
	if j.Kind != ports.CompleteTVTrackingJobKind || j.State != "succeeded" || json.Unmarshal(j.Payload, &payload) != nil || payload.TaskID != task.ID || payload.OperationID != op || json.Unmarshal(j.Result, &out.Receipt) != nil || out.Receipt.TaskID != task.ID || out.Receipt.State != "completed" || !out.Receipt.ReviewRequired || !completeTVStamp(out.Receipt.EvidenceStamp) || len(out.Receipt.Files) < 2 || len(out.Receipt.Files) > 1000 {
		return fail()
	}
	for _, v := range out.Files {
		found := false
		for _, file := range out.Receipt.Files {
			if file.Name == v.Path && file.SizeBytes == v.Size && file.Progress == 1 {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	if initial {
		v, e := readEpisodeCandidate(ctx, q, a, f.SubscriptionID, f.CandidateID)
		if e != nil {
			return nil, e
		}
		own := map[uuid.UUID]bool{}
		for _, r := range f.Units {
			own[r.UnitID] = true
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return nil, e
		}
		if !completeTVEligible(v, f.Units, own, now.Time) {
			return fail()
		}
		demands, e := q.ListRequirementsForOperation(ctx, op)
		if e != nil {
			return nil, completeTVOrganizationReadError(e)
		}
		if len(demands) != len(f.Units) {
			return fail()
		}
		lanes, e := q.ListCompleteTVPreparationLanes(ctx, op)
		if e != nil {
			return nil, completeTVOrganizationReadError(e)
		}
		if len(lanes) != len(f.Units) {
			return fail()
		}
		reservations, e := q.ListCompleteTVPreparationReservations(ctx, op)
		if e != nil {
			return nil, completeTVOrganizationReadError(e)
		}
		if len(reservations) != len(f.Units) {
			return fail()
		}
		for _, unit := range f.Units {
			found := false
			for _, d := range demands {
				if d.UnitID == unit.UnitID && d.RootID == f.RootID && d.State == "pending" && d.ActorID == f.ActorID.String() && d.ActorType == "user" {
					var fence ports.DownloadDemandFence
					if json.Unmarshal(d.SubmissionFence, &fence) != nil || fence.CandidateID != f.CandidateID || fence.UnitMetadataVersion != unit.UnitVersion || fence.MediaMetadataVersion != f.MediaVersion || fence.RootGeneration != f.RootGeneration || fence.IntentVersion != f.IntentVersion || fence.PolicyRevision != f.PolicyRevision || fence.PolicyID != f.PolicyID || fence.SourceStamp != f.SourceStamp {
						return fail()
					}
					found = true
				}
			}
			if !found {
				return fail()
			}
			found = false
			for _, l := range lanes {
				if l.UnitID == unit.UnitID && l.OperationID == op && l.TaskID.Valid && uuid.UUID(l.TaskID.Bytes) == task.ID && !l.LegacyConflict && l.AcquisitionLane == "default" && l.SourceRootID == f.RootID && l.SourceGeneration == f.RootGeneration && l.SourceStamp == f.SourceStamp && l.CandidateID == f.CandidateID {
					found = true
				}
			}
			if !found {
				return fail()
			}
			found = false
			for _, r := range reservations {
				if r.UnitID == unit.UnitID && r.RootID == f.RootID && r.CandidateID == f.CandidateID && r.State == "dispatched" && r.Purpose == "manual_acquisition" {
					found = true
				}
			}
			if !found {
				return fail()
			}
		}
	}
	return out, nil
}
