package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ ports.LibraryReusePreviewRepository = (*Repository)(nil)

// All targets share one database snapshot. An offline or unmapped source never
// becomes proof of ownership, and an incomplete list cannot authorize reuse.
func (r *Repository) PreviewLibraryReuseBatch(ctx context.Context, a *ports.Actor, targets []ports.LibraryReuseTarget) (out []ports.LibraryReusePreview, err error) {
	if len(targets) == 0 || len(targets) > 20 {
		return nil, ports.ErrInvalidLibraryReuse
	}
	defer func() {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			err = ports.ErrNotFound
			return
		}
		for _, allowed := range []error{ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrInvalidLibraryReuse, ports.ErrLibraryReuseChanged} {
			if errors.Is(err, allowed) {
				return
			}
		}
		err = ports.ErrUnavailable
	}()
	tx, err := r.execution.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.execution.queries.WithTx(tx)
	actor, err := recoveryUser(ctx, q, a)
	if err != nil {
		return nil, err
	}
	now, err := q.GetSearchDatabaseTime(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[[2]uuid.UUID]bool, len(targets))
	out = make([]ports.LibraryReusePreview, 0, len(targets))
	for _, target := range targets {
		key := [2]uuid.UUID{target.UnitID, target.RootID}
		if seen[key] || target.UnitID == uuid.Nil || target.RootID == uuid.Nil || target.PolicyID == "" || target.PolicyRevision < 1 {
			return nil, ports.ErrInvalidLibraryReuse
		}
		seen[key] = true
		preview, e := loadLibraryReusePreview(ctx, q, actor, target, now)
		if e != nil {
			return nil, e
		}
		out = append(out, *preview)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

type reuseInput struct {
	Actor              uuid.UUID
	Target             ports.LibraryReuseTarget
	Root               reuseRootInput
	Unit               db.MediaUnit
	MediaVersion       int64
	Rules              quality.PolicyRules
	SubscriptionIntent int64
	Protected          bool
	Wanted             *reuseWantedInput
	Files              []db.ListLibraryReuseFilesRow
}

type reuseRootInput struct {
	ID                              uuid.UUID
	Path, MediaType, Health, Marker string
	Generation, HealthRevision      int64
	Baseline                        bool
}

func reuseRootSnapshot(r db.LibraryRoot) reuseRootInput {
	return reuseRootInput{ID: r.ID, Path: r.Path, MediaType: r.MediaType, Health: r.Health, Marker: r.MarkerFilename, Generation: r.Generation, HealthRevision: r.HealthRevision, Baseline: r.BaselineCompleted}
}

type reuseWantedInput struct {
	ID                                    uuid.UUID
	IntentVersion                         int64
	UserSkipped, ScopeExcluded, IsSkipped bool
}

func loadLibraryReusePreview(ctx context.Context, q *db.Queries, actor uuid.UUID, target ports.LibraryReuseTarget, now pgtype.Timestamptz, ignore ...uuid.UUID) (*ports.LibraryReusePreview, error) {
	root, e := q.GetLibraryRootByID(ctx, target.RootID)
	if e != nil {
		return nil, e
	}
	unit, e := q.GetLibraryReuseUnit(ctx, target.UnitID)
	if e != nil {
		return nil, e
	}
	parent, e := q.GetMediaByID(ctx, unit.MediaID)
	if e != nil {
		return nil, e
	}
	if unit.Retired || root.MediaType != parent.MediaType {
		return nil, ports.ErrInvalidLibraryReuse
	}
	policy, e := q.GetLibraryReusePolicy(ctx, target.PolicyID)
	if e != nil {
		return nil, e
	}
	if policy.CurrentRevision != target.PolicyRevision {
		return nil, ports.ErrLibraryReuseChanged
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: target.PolicyID, Revision: target.PolicyRevision})
	if e != nil {
		return nil, e
	}
	input := reuseInput{Actor: actor, Target: target, Root: reuseRootSnapshot(root), Unit: unit, MediaVersion: parent.MetadataVersion}
	if json.Unmarshal(revision.Rules, &input.Rules) != nil {
		return nil, ports.ErrLibraryReuseChanged
	}
	if target.SubscriptionID != uuid.Nil {
		sub, e := q.GetSubscriptionByID(ctx, target.SubscriptionID)
		if e != nil {
			return nil, e
		}
		if sub.UserID != actor {
			return nil, ports.ErrForbidden
		}
		if sub.RootID != root.ID || sub.MediaID != parent.ID || sub.QualityPolicyID != target.PolicyID || sub.QualityPolicyRevision != target.PolicyRevision || sub.State != "active" {
			return nil, ports.ErrLibraryReuseChanged
		}
		wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
		if e != nil {
			return nil, e
		}
		for _, w := range wanted {
			if w.UnitID == unit.ID {
				input.Wanted = &reuseWantedInput{ID: w.ID, IntentVersion: w.IntentVersion, UserSkipped: w.UserSkipped, ScopeExcluded: w.ScopeExcluded, IsSkipped: w.IsSkipped}
				break
			}
		}
		if input.Wanted == nil || input.Wanted.UserSkipped || input.Wanted.ScopeExcluded || input.Wanted.IsSkipped {
			return nil, ports.ErrLibraryReuseChanged
		}
		input.SubscriptionIntent = sub.IntentVersion
	}
	input.Files, e = q.ListLibraryReuseFiles(ctx, db.ListLibraryReuseFilesParams{UnitID: unit.ID, TargetRootID: root.ID, ObservedAt: now})
	if e != nil {
		return nil, e
	}
	var ignorePlan uuid.UUID
	if len(ignore) == 1 {
		ignorePlan = ignore[0]
	}
	input.Protected, e = q.HasLibraryReuseProtection(ctx, db.HasLibraryReuseProtectionParams{RootID: root.ID, UnitID: unit.ID, IgnorePlan: ignorePlan})
	if e != nil {
		return nil, e
	}
	out := &ports.LibraryReusePreview{Target: target, MediaID: parent.ID, MediaType: parent.MediaType, UnitKey: unit.UnitKey, Complete: len(input.Files) <= 128, Candidates: []ports.LibraryReuseCandidate{}}
	files := input.Files
	if len(files) > 128 {
		files = files[:128]
	}
	for _, f := range files {
		candidate := reuseCandidate(f, unit, parent.MediaType, input.Rules)
		if f.RootID == root.ID {
			if candidate.Qualified && candidate.SourceAvailable {
				out.AlreadyOwned = true
			}
			if candidate.WaitReason == "ownership_unknown" {
				out.OwnershipUnknown = true
			}
			continue
		}
		out.Candidates = append(out.Candidates, candidate)
	}
	switch {
	case root.Health != "online" || !root.BaselineCompleted:
		out.WaitReason = "target_root_unavailable"
	case !out.Complete:
		out.WaitReason = "candidate_list_incomplete"
	case input.Protected:
		out.WaitReason = "reserved"
	case out.OwnershipUnknown:
		out.WaitReason = "ownership_unknown"
	case out.AlreadyOwned:
		out.WaitReason = "already_owned"
	default:
		for _, c := range out.Candidates {
			if c.Qualified && c.SourceAvailable && len(c.UnitIDs) <= 20 {
				out.CanConfirm = true
				break
			}
		}
		if !out.CanConfirm {
			out.WaitReason = "no_available_qualified_source"
		}
	}
	// The stamp binds identities, versions, mappings, imported evidence and rules;
	// it deliberately excludes wall time and derived projection write timestamps.
	out.InputStamp = recoveryHash(input)
	return out, nil
}

func reuseCandidate(f db.ListLibraryReuseFilesRow, unit db.MediaUnit, mediaType string, rules quality.PolicyRules) ports.LibraryReuseCandidate {
	out := ports.LibraryReuseCandidate{FileID: f.FileID, RootID: f.RootID, RootName: f.RootName, RelPath: f.RelPath, SourceAvailable: f.Health == "online" && f.BaselineCompleted, UnitIDs: []uuid.UUID{}, UnitKeys: []string{}, WaitReason: "ownership_unknown"}
	var coverage []struct {
		UnitID          uuid.UUID `json:"unit_id"`
		UnitKey         string    `json:"unit_key"`
		MediaID         uuid.UUID `json:"media_id"`
		MetadataVersion int64     `json:"metadata_version"`
		Retired         bool      `json:"retired"`
	}
	if json.Unmarshal(f.UnitCoverage, &coverage) != nil || len(coverage) == 0 {
		return out
	}
	contains := false
	for _, c := range coverage {
		if c.UnitID == uuid.Nil || c.MediaID != unit.MediaID || c.Retired || c.MetadataVersion < 1 {
			return out
		}
		if c.UnitID == unit.ID {
			contains = true
		}
		out.UnitIDs = append(out.UnitIDs, c.UnitID)
		out.UnitKeys = append(out.UnitKeys, c.UnitKey)
	}
	if !contains || f.RootMediaType != mediaType || f.SizeBytes <= 0 || !f.FileFingerprint.Valid || f.MappingRevision < 1 {
		return out
	}
	var facts []struct {
		ID      uuid.UUID       `json:"id"`
		UnitID  uuid.UUID       `json:"unit_id"`
		Profile quality.Profile `json:"profile"`
	}
	if json.Unmarshal(f.ImportEvidence, &facts) != nil || len(facts) == 0 {
		return out
	}
	covered := make(map[uuid.UUID]bool)
	for i, fact := range facts {
		if fact.ID == uuid.Nil || !slices.Contains(out.UnitIDs, fact.UnitID) || !quality.KnownProfile(fact.Profile) || (i > 0 && fact.Profile != facts[0].Profile) {
			return out
		}
		covered[fact.UnitID] = true
	}
	if len(covered) != len(out.UnitIDs) || len(out.UnitIDs) > 20 {
		return out
	}
	out.Quality = facts[0].Profile
	decision := quality.EvaluateCandidate(rules, f.RelPath, out.Quality)
	out.Score = int(decision.Score)
	if !decision.Passed {
		out.WaitReason = "quality_rejected"
		return out
	}
	out.Qualified = true
	out.WaitReason = ""
	if !out.SourceAvailable {
		out.WaitReason = "source_unavailable"
	} else if len(out.UnitIDs) > 1 {
		out.WaitReason = "complete_file_coverage_required"
	}
	return out
}
