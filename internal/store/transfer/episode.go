package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"path/filepath"
)

func (f transferTargetEvidence) MarshalJSON() ([]byte, error) {
	type plain transferTargetEvidence
	raw, e := json.Marshal(plain(f))
	if e != nil {
		return nil, e
	}
	if f.EpisodeOperationID != uuid.Nil {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if e = json.Unmarshal(raw, &m); e != nil {
		return nil, e
	}
	delete(m, "episode_operation_id")
	if f.CompleteTVPlanID == uuid.Nil {
		delete(m, "complete_tv_plan_id")
	}
	if f.InventoryTaskID == uuid.Nil {
		delete(m, "inventory_task_id")
	}
	return json.Marshal(m)
}
func (s *Service) organizeReviewedEpisode(ctx context.Context, lease ports.JobLease, task db.DownloadTask, client db.DownloadClient) error {
	var err error
	if task.TransferPlanID.Valid {
		plan, e := s.queries.GetTransferPlanByID(ctx, uuid.UUID(task.TransferPlanID.Bytes))
		if e != nil {
			return e
		}
		if plan.OperationID != task.OperationID {
			return s.reviewOrganization(ctx, lease, task.ID)
		}
		err = s.executePlan(ctx, plan.ID, lease)
	} else {
		if task.OrganizationState != "queued" || !task.ContentPath.Valid {
			return s.reviewOrganization(ctx, lease, task.ID)
		}
		candidate, e := s.queries.GetCandidateByID(ctx, task.CandidateID)
		if e != nil {
			return e
		}
		var profile quality.Profile
		var mapping map[string]string
		if json.Unmarshal(candidate.QualityProfile, &profile) != nil || json.Unmarshal(client.RemotePathMap, &mapping) != nil {
			return s.reviewOrganization(ctx, lease, task.ID)
		}
		local := naming.TranslateRemotePath(mapping, task.ContentPath.String)
		plan, _, e := s.createReviewedEpisodePlan(ctx, &automaticPlan{lease: lease, task: task}, local, profile)
		if e != nil {
			err = e
		} else {
			err = s.executePlan(ctx, plan.ID, lease)
		}
	}
	if reviewable(err) {
		return s.reviewOrganization(ctx, lease, task.ID)
	}
	if err != nil && !errors.Is(err, ErrExecutionRejected) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	return err
}

func transferEvidenceMediaType(raw []byte) string {
	var f transferTargetEvidence
	if json.Unmarshal(raw, &f) != nil {
		return ""
	}
	if (f.EpisodeOperationID != uuid.Nil || f.CompleteTVPlanID != uuid.Nil) && f.InventoryTaskID != uuid.Nil {
		return "tv"
	}
	return "movie"
}

// Before job/plan/root locks: account -> policy -> subscription -> root.
func prelockEpisodeTransfer(ctx context.Context, q *db.Queries, op uuid.UUID) (*ports.EpisodeExecutionSnapshot, error) {
	if handled, e := postgres.PrelockCompleteTVOrganization(ctx, q, op); handled || e != nil {
		return nil, e
	}
	r, e := q.GetEpisodeExecutionAuthority(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var f ports.EpisodeExecutionSnapshot
	if json.Unmarshal(r.Snapshot, &f) != nil || !f.ValidNumberingShape() || f.MediaType != "tv" || r.UnitID != f.UnitID || r.RootID != f.RootID || r.ClientID != f.ClientID || r.CandidateID != f.CandidateID || r.SubscriptionID != f.SubscriptionID || r.ActorID != f.ActorID || r.ReviewID != f.ReviewID {
		return nil, ErrReviewRequired
	}
	if e = postgres.LockEpisodeExecutionUser(ctx, q, op); e != nil {
		if errors.Is(e, ports.ErrDownloadDemandChanged) {
			return nil, ErrReviewRequired
		}
		return nil, e
	}
	p, e := q.LockSearchPolicy(ctx, f.PolicyID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e != nil || p.CurrentRevision != f.PolicyRevision {
		return nil, ErrReviewRequired
	}
	s, e := q.LockSearchSubscription(ctx, f.SubscriptionID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e != nil || s.State != "active" || s.IntentVersion != f.IntentVersion || s.UserID != f.ActorID || s.RootID != f.RootID || s.MediaID != f.MediaID || s.QualityPolicyID != f.PolicyID || s.QualityPolicyRevision != f.PolicyRevision {
		return nil, ErrReviewRequired
	}
	return &f, nil
}

// Root, unit, acquisition/task and media already locked; never re-sign reviewed identity.
func validateEpisodeTransfer(ctx context.Context, q *db.Queries, op uuid.UUID, root db.LibraryRoot, unit db.MediaUnit, task db.DownloadTask) (*ports.EpisodeExecutionSnapshot, error) {
	r, e := q.GetEpisodeExecutionAuthority(ctx, op)
	if e != nil {
		return nil, ErrReviewRequired
	}
	var f ports.EpisodeExecutionSnapshot
	if json.Unmarshal(r.Snapshot, &f) != nil || !f.ValidNumberingShape() || f.UnitID != unit.ID || f.RootID != root.ID || task.OperationID != op || task.CandidateID != f.CandidateID || task.ClientID != f.ClientID || task.State != "completed" || task.Progress != 1 || task.TotalBytes <= 0 || task.DownloadedBytes < task.TotalBytes {
		return nil, ErrReviewRequired
	}
	inv, e := q.GetDownloadFileInventory(ctx, task.ID)
	if e != nil || inv.OperationID != op || inv.ClientID != task.ClientID || inv.InfoHash != task.InfoHash || inv.TotalBytes != task.TotalBytes || inv.ContentPath != task.ContentPath.String || inv.ClientStamp != f.ClientStamp {
		return nil, ErrReviewRequired
	}
	var files []ports.TorrentFile
	if json.Unmarshal(inv.Files, &files) != nil || len(files) != 1 || files[0].Progress != 1 || files[0].SizeBytes != task.TotalBytes || !task.ContentPath.Valid || filepath.Clean(task.ContentPath.String) != filepath.Clean(filepath.Join(task.SavePath, files[0].Name)) {
		return nil, ErrReviewRequired
	}
	if ok, _ := naming.IsEligibleMediaFile(files[0].Name, files[0].SizeBytes, 1); !ok {
		return nil, ErrReviewRequired
	}
	identity, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil || identity.ExternalID != f.EpisodeID {
		return nil, ErrReviewRequired
	}
	rows, e := postgres.ResolveEpisodeFileNumbering(ctx, q, f, filepath.Base(files[0].Name))
	if e != nil {
		return nil, ErrReviewRequired
	}
	if len(rows) != 1 || rows[0].UnitID != unit.ID {
		return nil, ErrReviewRequired
	}
	sub, e := q.GetSubscriptionByID(ctx, f.SubscriptionID)
	if e != nil {
		return nil, ErrReviewRequired
	}
	if _, e = q.LockSubmissionIndexer(ctx, f.IndexerID); e != nil {
		return nil, e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, f.CandidateID)
	if e != nil {
		return nil, e
	}
	requirements, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil || len(requirements) != 1 {
		return nil, ErrReviewRequired
	}
	requirement := requirements[0]
	var fence ports.DownloadDemandFence
	if requirement.State != "pending" || requirement.UnitID != unit.ID || requirement.RootID != root.ID || json.Unmarshal(requirement.SubmissionFence, &fence) != nil {
		return nil, ErrReviewRequired
	}
	if e = postgres.ValidateEpisodeExecutionInputs(ctx, q, ports.DownloadSubmissionCommand{OperationID: op, ClientID: task.ClientID, CandidateID: task.CandidateID}, sub, root, unit, candidate, fence); e != nil {
		return nil, ErrReviewRequired
	}
	rev, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: f.PolicyID, Revision: f.PolicyRevision})
	if e != nil {
		return nil, e
	}
	var rules quality.PolicyRules
	var profile quality.Profile
	if json.Unmarshal(rev.Rules, &rules) != nil || json.Unmarshal(candidate.QualityProfile, &profile) != nil || !quality.EvaluateCandidate(rules, candidate.Title, profile).Passed {
		return nil, ErrReviewRequired
	}
	existing, e := q.ListAcquisitionOwnershipEvidence(ctx, db.ListAcquisitionOwnershipEvidenceParams{UnitID: unit.ID, RootID: root.ID})
	if e != nil {
		return nil, e
	}
	evidence := []quality.FileEvidence{}
	for _, file := range existing {
		var p quality.Profile
		if err := json.Unmarshal(file.QualityProfile, &p); err != nil {
			return nil, ErrReviewRequired
		}
		evidence = append(evidence, quality.FileEvidence{Title: file.RelPath, Profile: p, Verified: file.Stage.Valid})
	}
	owned := quality.AssessOwnership(rules, evidence)
	if owned.Owned || owned.Unknown {
		return nil, ErrReviewRequired
	}
	return &f, nil
}
