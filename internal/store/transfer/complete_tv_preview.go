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
	"sort"
	"time"
)

var _ ports.CompleteTVOrganizationPreviewRepository = (*Repository)(nil)

// All evidence belongs to one repeatable-read snapshot. Row/advisory locks reuse
// the trusted confirmation reader; this transaction ALWAYS rolls back and performs
// no DML, file/network observation, job scheduling, or idempotency writes.
func (r *Repository) ReadCompleteTVOrganizations(ctx context.Context, a *ports.Actor, op uuid.UUID) (out *ports.CompleteTVOrganizationPreviewPage, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrTransferReviewRequired) {
			err = ports.ErrUnavailable
		}
	}()
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	tx, err := r.execution.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && err == nil {
			out = nil
			err = rollbackErr
		}
	}()
	q := r.execution.queries.WithTx(tx)
	who, err := recoveryUser(ctx, q, a)
	if err != nil {
		return nil, err
	}
	scope, err := q.GetCompleteTVOrganizationScope(ctx, op)
	if errors.Is(err, pgx.ErrNoRows) {
		return &ports.CompleteTVOrganizationPreviewPage{Items: []ports.CompleteTVOrganizationPreview{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if scope.ActorID != who {
		return nil, ports.ErrForbidden
	}
	if _, err = q.LockSearchPolicy(ctx, scope.PolicyID); err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	sub, err := q.LockSearchSubscription(ctx, scope.SubscriptionID)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	if sub.UserID != who {
		return nil, ports.ErrForbidden
	}
	// Object checks precede snapshots, task, preparation, tracking and summaries.
	authority, err := q.GetCompleteTVExecutionAuthority(ctx, op)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	var f ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(authority.Snapshot, &f) != nil || f.ActorID != who || f.SubscriptionID != scope.SubscriptionID || f.PolicyID != scope.PolicyID {
		return nil, ports.ErrTransferReviewRequired
	}
	task, err := q.GetDownloadTaskByOpID(ctx, op)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	if task.State != "completed" {
		return &ports.CompleteTVOrganizationPreviewPage{Items: []ports.CompleteTVOrganizationPreview{}}, nil
	}
	tracking, err := q.FindCompletedTVOrganizationTracking(ctx, db.FindCompletedTVOrganizationTrackingParams{OperationID: op.String(), TaskID: task.ID.String()})
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	if _, err = postgres.PrelockCompleteTVOrganization(ctx, q, op); err != nil {
		return nil, err
	}
	root, err := q.LockTransferRoot(ctx, f.RootID)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	if root.Health != "online" || !root.BaselineCompleted || root.Generation != f.RootGeneration {
		return nil, ports.ErrTransferReviewRequired
	}
	ready := task.OrganizationState == "review_required" && !task.TransferPlanID.Valid
	inputs, err := postgres.ReadCompleteTVOrganizationInputs(ctx, q, op, tracking, ready)
	if err != nil {
		return nil, err
	}
	if len(inputs.Files) == 0 || len(inputs.Files) > 1000 || len(inputs.Frozen.CompleteSeasons) != 1 {
		return nil, ports.ErrTransferReviewRequired
	}
	parent, err := q.GetMediaByID(ctx, f.MediaID)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	candidate, err := q.GetCandidateByID(ctx, f.CandidateID)
	if err != nil {
		return nil, completeTVOrganizationReadError(err)
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil {
		return nil, ports.ErrTransferReviewRequired
	}
	var namingRules naming.TemplateRules
	var namingVersion int64
	frozenTargets := make(map[uuid.UUID]db.TransferStep)
	if task.TransferPlanID.Valid {
		steps, e := q.ListStepsForPlan(ctx, uuid.UUID(task.TransferPlanID.Bytes))
		if e != nil {
			return nil, e
		}
		if len(steps) != len(inputs.Files) {
			return nil, ports.ErrTransferReviewRequired
		}
		for index, step := range steps {
			var frozen transferTargetEvidence
			if json.Unmarshal(step.TargetEvidence, &frozen) != nil || frozen.NamingVersion < 0 || (index > 0 && frozen.NamingVersion != namingVersion) || !step.TargetRel.Valid {
				return nil, ports.ErrTransferReviewRequired
			}
			namingVersion = frozen.NamingVersion
			frozenTargets[step.UnitID] = step
		}
	} else {
		namingRules, namingVersion, err = lockTransferNaming(ctx, q)
		if err != nil {
			return nil, err
		}
	}
	p := ports.CompleteTVOrganizationPreview{CompleteTVOrganizationReferences: ports.CompleteTVOrganizationReferences{TaskID: task.ID, OperationID: op, TrackingJobID: tracking}, Season: inputs.Frozen.CompleteSeasons[0].Season, Videos: []ports.CompleteTVOrganizationVideoPreview{}, AttachmentCount: len(inputs.Receipt.Files) - len(inputs.Files), OrganizationState: task.OrganizationState, CanConfirm: ready}
	p.NamingVersion = namingVersion
	for _, v := range inputs.Files {
		source, e := naming.CleanRelPath(v.Path)
		if e != nil || source != v.Path {
			return nil, ports.ErrTransferReviewRequired
		}
		u, e := q.GetSearchUnit(ctx, v.UnitID)
		if e != nil {
			return nil, completeTVOrganizationReadError(e)
		}
		target, e := naming.FormatEpisodeRel(parent.Title, int(parent.Year.Int32), int(u.SeasonNumber.Int32), int(u.EpisodeNumber.Int32), profile, filepath.Ext(source))
		if task.TransferPlanID.Valid {
			frozen, ok := frozenTargets[u.ID]
			if !ok {
				return nil, ports.ErrTransferReviewRequired
			}
			target, e = naming.CleanRelPath(frozen.TargetRel.String)
			if e != nil || target != frozen.TargetRel.String {
				return nil, ports.ErrTransferReviewRequired
			}
		} else if namingRules.EpisodeTemplate != "" {
			input, err := transferNamingInput(ctx, q, parent, u, profile, filepath.Ext(source), namingRules.EpisodeTemplate)
			if err != nil {
				return nil, err
			}
			target, e = namingRules.EpisodeRelative(input)
		}
		if e != nil {
			return nil, ports.ErrTransferReviewRequired
		}
		p.Videos = append(p.Videos, ports.CompleteTVOrganizationVideoPreview{UnitID: v.UnitID, UnitKey: v.UnitKey, SourceRelative: source, TargetRelative: target, SizeBytes: v.Size})
	}
	sort.Slice(p.Videos, func(i, j int) bool { return p.Videos[i].UnitKey < p.Videos[j].UnitKey })
	return &ports.CompleteTVOrganizationPreviewPage{Items: []ports.CompleteTVOrganizationPreview{p}}, nil
}
