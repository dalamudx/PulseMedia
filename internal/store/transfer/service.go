package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

var (
	ErrSymlinkForbidden  = ports.ErrFileSymlink
	ErrDeviceMismatch    = errors.New("hardlink failed: source and target are on different devices")
	ErrInodeMismatch     = ports.ErrFileIdentity
	ErrReviewRequired    = ports.ErrTransferReviewRequired
	ErrExecutionRejected = ports.ErrTransferExecutionRejected
)

type Service struct {
	pool              *pgxpool.Pool
	idgen             ports.IDGen
	outbox            *outbox.Outbox
	queries           *db.Queries
	files             ports.TransferFilesystem
	minimumMediaBytes int64
	coordinator       ports.TransferExecutionCoordinator
}

func NewService(pool *pgxpool.Pool, idgen ports.IDGen, ob *outbox.Outbox, files ports.TransferFilesystem, coordinator ports.TransferExecutionCoordinator) *Service {
	return &Service{pool: pool, idgen: idgen, outbox: ob, queries: postgres.Queries(pool), files: files, minimumMediaBytes: 1, coordinator: coordinator}
}
func text(v string) pgtype.Text   { return pgtype.Text{String: v, Valid: true} }
func integer(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }
func relative(root, path string) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return "", ports.ErrFileEscape
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", ports.ErrFileEscape
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", ports.ErrFileEscape
	}
	return naming.CleanRelPath(rel)
}
func (s *Service) source(ctx context.Context, op uuid.UUID, path string) (string, string, ports.FileIdentity, error) {
	if eligible, _ := naming.IsPrimaryMediaPath(filepath.Base(path)); !eligible {
		return "", "", ports.FileIdentity{}, ports.ErrFileEscape
	}
	roots, err := s.queries.ListTransferSourceRoots(ctx, op)
	if err != nil {
		return "", "", ports.FileIdentity{}, err
	}
	// Prefer the narrowest configured capability; never allow an arbitrary parent.
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, pathRoot := range roots {
		rel, err := relative(pathRoot, path)
		if err != nil {
			continue
		}
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(part, ".") {
				return "", "", ports.FileIdentity{}, ports.ErrFileEscape
			}
		}
		root, err := s.files.OpenRoot(ctx, pathRoot)
		if err != nil {
			return "", "", ports.FileIdentity{}, err
		}
		identity, err := root.Inspect(ctx, rel)
		root.Close()
		if err != nil {
			return "", "", ports.FileIdentity{}, err
		}
		if eligible, _ := naming.IsEligibleMediaFile(rel, identity.Size, s.minimumMediaBytes); !eligible {
			return "", "", ports.FileIdentity{}, ErrReviewRequired
		}
		return pathRoot, rel, identity, nil
	}
	return "", "", ports.FileIdentity{}, ports.ErrFileEscape
}

// Plan and step are frozen together; no approved half-plan can be observed.
func (s *Service) CreateMoviePlan(ctx context.Context, opID, rootID, unitID uuid.UUID, sourcePath, movieTitle string, year int, q quality.Profile) (*db.TransferPlan, *db.TransferStep, error) {
	return s.createMoviePlan(ctx, opID, rootID, unitID, sourcePath, movieTitle, year, q, pgtype.UUID{})
}
func (s *Service) createMoviePlan(ctx context.Context, opID, rootID, unitID uuid.UUID, sourcePath, movieTitle string, year int, profile quality.Profile, old pgtype.UUID, automatic ...*automaticPlan) (*db.TransferPlan, *db.TransferStep, error) {
	return s.createHardlinkPlan(ctx, opID, rootID, unitID, sourcePath, movieTitle, year, profile, old, false, automatic...)
}
func (s *Service) createReviewedEpisodePlan(ctx context.Context, auto *automaticPlan, sourcePath string, profile quality.Profile) (*db.TransferPlan, *db.TransferStep, error) {
	record, e := s.queries.GetEpisodeExecutionAuthority(ctx, auto.task.OperationID)
	if e != nil {
		return nil, nil, ErrReviewRequired
	}
	return s.createHardlinkPlan(ctx, auto.task.OperationID, record.RootID, record.UnitID, sourcePath, "", 0, profile, pgtype.UUID{}, true, auto)
}
func (s *Service) createHardlinkPlan(ctx context.Context, opID, rootID, unitID uuid.UUID, sourcePath, movieTitle string, year int, profile quality.Profile, old pgtype.UUID, episode bool, automatic ...*automaticPlan) (*db.TransferPlan, *db.TransferStep, error) {
	sourceRoot, sourceRel, identity, err := s.source(ctx, opID, sourcePath)
	if err != nil {
		return nil, nil, err
	}
	targetRel, err := naming.CleanRelPath(filepath.Join(naming.FormatMovieDir(movieTitle, year), naming.FormatMovieFile(movieTitle, year, profile, filepath.Ext(sourcePath))))
	if episode {
		u, e := s.queries.GetSearchUnit(ctx, unitID)
		if e != nil {
			return nil, nil, e
		}
		parent, e := s.queries.GetMediaByID(ctx, u.MediaID)
		if e != nil {
			return nil, nil, e
		}
		targetRel, err = naming.FormatEpisodeRel(parent.Title, int(parent.Year.Int32), int(u.SeasonNumber.Int32), int(u.EpisodeNumber.Int32), profile, filepath.Ext(sourcePath))
	}
	if err != nil {
		return nil, nil, err
	}
	// Probe directory identity before the short database transaction; it is checked
	// again under the physical slot when the plan executes.
	observedRoot, err := s.queries.GetLibraryRootByID(ctx, rootID)
	if err != nil {
		return nil, nil, err
	}
	targetRoot, err := s.files.OpenRoot(ctx, observedRoot.Path)
	if err != nil {
		return nil, nil, err
	}
	rootIdentity, err := targetRoot.Identity(ctx)
	if err == nil {
		marker := observedRoot.MarkerFilename
		if marker == "" {
			marker = ".pulsemedia_root"
		}
		_, err = targetRoot.Inspect(ctx, marker)
	}
	targetRoot.Close()
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	frozen, e := prelockEpisodeTransfer(ctx, q, opID)
	if e != nil {
		return nil, nil, e
	}
	if episode {
		if frozen == nil || frozen.RootID != rootID || frozen.UnitID != unitID || old.Valid || len(automatic) != 1 {
			return nil, nil, ErrReviewRequired
		}
	} else if frozen != nil {
		return nil, nil, ErrReviewRequired
	}
	if len(automatic) == 1 {
		if err := guardOrganization(ctx, q, []ports.JobLease{automatic[0].lease}); err != nil {
			return nil, nil, err
		}
	}
	root, err := q.LockTransferRoot(ctx, rootID)
	if err != nil {
		return nil, nil, err
	}
	unit, err := q.LockTransferUnit(ctx, unitID)
	if err != nil {
		return nil, nil, err
	}
	if unit.Retired || (!episode && (unit.UnitKey != "movie" || root.MediaType != "movie")) || (episode && (root.MediaType != "tv" || unit.UnitKey == "movie")) {
		return nil, nil, ErrReviewRequired
	}
	if err = q.LockDownloadAcquisition(ctx, opID); err != nil {
		return nil, nil, err
	}
	var movieInventoryTaskID uuid.UUID
	var movieInventorySize int64
	var movieInventorySource string
	if len(automatic) == 1 {
		auto := automatic[0]
		current, err := q.LockDownloadTask(ctx, auto.task.ID)
		if err != nil {
			return nil, nil, err
		}
		if current.TransferPlanID.Valid {
			existing, err := q.GetTransferPlanByID(ctx, uuid.UUID(current.TransferPlanID.Bytes))
			if err != nil {
				return nil, nil, err
			}
			steps, err := q.ListStepsForPlan(ctx, existing.ID)
			if err != nil || len(steps) != 1 {
				return nil, nil, ErrReviewRequired
			}
			return &existing, &steps[0], nil
		}
		inputs, err := q.GetDownloadOrganizationInputs(ctx, current.ID)
		if err != nil {
			return nil, nil, err
		}
		expectedSize := current.TotalBytes
		if !episode {
			var verifyErr error
			expectedSize, movieInventoryTaskID, movieInventorySource, verifyErr = validateMovieInventory(ctx, q, current)
			if verifyErr != nil || (movieInventoryTaskID != uuid.Nil && sourcePath != movieInventorySource) {
				return nil, nil, ErrReviewRequired
			}
			movieInventorySize = expectedSize
		}
		if current.State != "completed" || current.OrganizationState != "queued" || current.ContentPath != auto.task.ContentPath || current.SavePath != auto.task.SavePath || current.InfoHash != auto.task.InfoHash || current.CandidateID != auto.task.CandidateID || current.TotalBytes != auto.task.TotalBytes || identity.Size != expectedSize || (!episode && (!singleMovieCoverage(inputs) || !containsOrganizationInput(inputs, auto.input) || !validOrganizationInput(auto.input))) {
			return nil, nil, ErrReviewRequired
		}
	}
	if root.Health != "online" || (len(automatic) == 1 && !root.BaselineCompleted) {
		return nil, nil, ErrReviewRequired
	}
	if root.Path != observedRoot.Path || root.Generation != observedRoot.Generation {
		return nil, nil, ErrReviewRequired
	}
	if old.Valid {
		if err := validateOldFile(ctx, q, rootID, unitID, uuid.UUID(old.Bytes)); err != nil {
			return nil, nil, err
		}
	}
	targetMedia, err := q.LockSubmissionMedia(ctx, unit.MediaID)
	if err != nil {
		return nil, nil, err
	}
	if (!episode && targetMedia.MediaType != "movie") || (episode && targetMedia.MediaType != "tv") {
		return nil, nil, ErrReviewRequired
	}
	if episode {
		task, e := q.GetDownloadTaskByOpID(ctx, opID)
		if e != nil {
			return nil, nil, e
		}
		if _, e = validateEpisodeTransfer(ctx, q, opID, root, unit, task); e != nil {
			return nil, nil, e
		}
	}
	namingRules, namingVersion, err := lockTransferNaming(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	template := namingRules.MovieTemplate
	if episode {
		template = namingRules.EpisodeTemplate
	}
	if template != "" {
		input, e := transferNamingInput(ctx, q, targetMedia, unit, profile, filepath.Ext(sourcePath), template)
		if e != nil {
			return nil, nil, e
		}
		if episode {
			targetRel, err = namingRules.EpisodeRelative(input)
		} else {
			targetRel, err = namingRules.MovieRelative(input)
		}
		if err != nil {
			return nil, nil, ErrReviewRequired
		}
	}
	plan, err := q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: s.idgen.NewUUID(), OperationID: opID, RootID: rootID, State: "approved", Strategy: "hardlink"})
	if err != nil {
		return nil, nil, err
	}
	if err = q.FreezeTransferPlan(ctx, db.FreezeTransferPlanParams{ID: plan.ID, RootPath: text(root.Path), RootGeneration: integer(root.Generation), OldFileID: old, RootDevice: integer(rootIdentity.Device), RootInode: integer(rootIdentity.Inode)}); err != nil {
		return nil, nil, err
	}
	step, err := q.CreateTransferStep(ctx, db.CreateTransferStepParams{ID: s.idgen.NewUUID(), PlanID: plan.ID, UnitID: unitID, SourcePath: sourcePath, TargetPath: filepath.Join(root.Path, targetRel), Action: "hardlink", StepOrder: 1, SourceSize: identity.Size, SourceMtime: identity.Mtime})
	if err != nil {
		return nil, nil, err
	}
	profileBytes, err := json.Marshal(profile)
	if err != nil {
		return nil, nil, err
	}
	frozenTarget := transferTargetEvidence{ParserVersion: media.EpisodeParserVersion, UnitID: unit.ID, UnitKey: unit.UnitKey, UnitVersion: unit.MetadataVersion, MediaID: targetMedia.ID, MediaVersion: targetMedia.MetadataVersion}
	frozenTarget.NamingVersion = namingVersion
	if movieInventoryTaskID != uuid.Nil {
		frozenTarget.InventoryTaskID = movieInventoryTaskID
		frozenTarget.InventorySourcePath = movieInventorySource
		frozenTarget.InventorySizeBytes = movieInventorySize
	}
	if episode {
		frozenTarget.EpisodeOperationID = opID
		frozenTarget.InventoryTaskID = automatic[0].task.ID
	}
	targetEvidence, err := json.Marshal(frozenTarget)
	if err != nil {
		return nil, nil, err
	}
	if err = q.FreezeTransferStep(ctx, db.FreezeTransferStepParams{ID: step.ID, SourceRoot: text(sourceRoot), SourceRel: text(sourceRel), TargetRel: text(targetRel), SourceDevice: integer(identity.Device), SourceInode: integer(identity.Inode), QualityProfile: profileBytes, TargetEvidence: targetEvidence}); err != nil {
		return nil, nil, err
	}
	if len(automatic) == 1 {
		if _, err = q.AttachDownloadTransferPlan(ctx, db.AttachDownloadTransferPlanParams{ID: automatic[0].task.ID, TransferPlanID: pgtype.UUID{Bytes: plan.ID, Valid: true}}); err != nil {
			return nil, nil, err
		}
	}
	plan, err = q.GetTransferPlanByID(ctx, plan.ID)
	if err != nil {
		return nil, nil, err
	}
	steps, err := q.ListStepsForPlan(ctx, plan.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(automatic) == 1 {
		if err := guardOrganization(ctx, q, []ports.JobLease{automatic[0].lease}); err != nil {
			return nil, nil, err
		}
	}
	if err = postgres.GuardEpisodeNumberingTime(ctx, q, opID); err != nil {
		return nil, nil, ErrReviewRequired
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &plan, &steps[0], nil
}

func validateOldFile(ctx context.Context, q *db.Queries, root, unit, fileID uuid.UUID) error {
	file, err := q.LockTransferOldFile(ctx, fileID)
	if err != nil {
		return err
	}
	if file.RootID != root || file.RetiredAt.Valid {
		return ErrReviewRequired
	}
	units, err := q.ListUnitsForFile(ctx, fileID)
	if err != nil {
		return err
	}
	// Single-unit replacement cannot revoke facts belonging to another unit.
	if len(units) != 1 || units[0].ID != unit {
		return ErrReviewRequired
	}
	return nil
}
func guardParams(plan db.TransferPlan) db.GuardTransferPlanParams {
	return db.GuardTransferPlanParams{ID: plan.ID, Owner: plan.ExecutionOwner.String, Token: plan.ExecutionToken}
}
func (s *Service) review(ctx context.Context, plan db.TransferPlan, authority ...ports.JobLease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if _, err = prelockEpisodeTransfer(ctx, q, plan.OperationID); err != nil {
		return err
	}
	if err := guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	_, err = q.ReviewTransferPlan(ctx, db.ReviewTransferPlanParams{ID: plan.ID, Owner: plan.ExecutionOwner.String, Token: plan.ExecutionToken})
	if err != nil {
		return ErrExecutionRejected
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return ErrReviewRequired
}
func frozen(step db.TransferStep) ports.FileIdentity {
	return ports.FileIdentity{Device: step.SourceDevice.Int64, Inode: step.SourceInode.Int64, Size: step.SourceSize, Mtime: step.SourceMtime}
}

// Physical slots are acquired BEFORE reclaiming logical execution. Lease expiry
// alone can never authorize a second writer while a prior syscall is alive.
func (s *Service) ExecutePlan(ctx context.Context, planID uuid.UUID) error {
	return s.executePlan(ctx, planID)
}
func (s *Service) executePlan(ctx context.Context, planID uuid.UUID, authority ...ports.JobLease) error {
	if s.coordinator == nil {
		return ports.ErrUnavailable
	}
	return s.coordinator(ctx, s, planID, authority...)
}

func (s *Service) PrepareTransferExecution(ctx context.Context, planID uuid.UUID, authority ...ports.JobLease) (*ports.PreparedTransfer, error) {
	authority = append([]ports.JobLease(nil), authority...)
	plan, err := s.queries.GetTransferPlanByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	completed := func(plan db.TransferPlan, release func()) *ports.PreparedTransfer {
		return &ports.PreparedTransfer{Completed: true, Release: release, Complete: func(c context.Context) error {
			if len(authority) == 1 {
				return s.finishOrganization(c, authority[0], plan, true)
			}
			return nil
		}}
	}
	if plan.State == "completed" {
		return completed(plan, nil), nil
	}
	if !plan.RootPath.Valid || !plan.RootGeneration.Valid || !plan.RootDevice.Valid || !plan.RootInode.Valid || (plan.Strategy != "hardlink" && !verifiedAction(plan.Strategy)) {
		return nil, ErrReviewRequired
	}
	steps, err := s.queries.ListStepsForPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, ErrReviewRequired
	}
	paths := []string{plan.RootPath.String}
	for _, step := range steps {
		if !step.SourceRoot.Valid || !step.SourceRel.Valid || !step.TargetRel.Valid || !step.SourceDevice.Valid || !step.SourceInode.Valid || len(step.QualityProfile) == 0 || step.Action != plan.Strategy {
			return nil, ErrReviewRequired
		}
		paths = append(paths, step.SourceRoot.String)
	}
	roots, release, err := s.files.LockRoots(ctx, paths...)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	identity, err := roots[plan.RootPath.String].Identity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.Device != plan.RootDevice.Int64 || identity.Inode != plan.RootInode.Int64 {
		return nil, ErrReviewRequired
	}
	plan, err = s.queries.GetTransferPlanByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan.State == "completed" {
		handedOff = true
		return completed(plan, release), nil
	}
	plan, err = s.queries.ClaimTransferPlan(ctx, db.ClaimTransferPlanParams{ID: planID, Owner: text(s.idgen.NewUUID().String())})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrExecutionRejected
	}
	if err != nil {
		return nil, err
	}
	currentRoot, err := s.queries.GetLibraryRootByID(ctx, plan.RootID)
	if err != nil {
		return nil, err
	}
	marker := currentRoot.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	attempt := &ports.PreparedTransfer{Release: release, Review: func(c context.Context) error { return s.review(c, plan, authority...) }, Complete: func(c context.Context) error {
		if len(authority) == 1 {
			return s.finishOrganization(c, authority[0], plan, false)
		}
		_, e := s.queries.FinishTransferPlan(c, db.FinishTransferPlanParams{ID: plan.ID, Owner: plan.ExecutionOwner.String, Token: plan.ExecutionToken})
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrExecutionRejected
		}
		return e
	}}
	if _, err := roots[plan.RootPath.String].Inspect(ctx, marker); err != nil {
		attempt.ReviewRequired = true
		handedOff = true
		return attempt, nil
	}
	steps, err = s.queries.ListStepsForPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		attempt.Steps = append(attempt.Steps, ports.TransferExecutionStep{State: step.State, Eligible: step.State != "pending" || s.stepMediaEligible(step), Execute: func(c context.Context) error {
			if step.Action == "hardlink" {
				return s.executeHardlinkStep(c, roots, plan, step, marker, authority)
			}
			return withTransferLease(c, func(c context.Context) error { return s.renewTransferAuthority(c, plan, authority) }, func(c context.Context) error { return s.executeVerifiedStep(c, roots, plan, step, marker, authority) }, 20*time.Second)
		}})
	}
	handedOff = true
	return attempt, nil
}
func (s *Service) startStep(ctx context.Context, plan db.TransferPlan, step db.TransferStep, authority ...ports.JobLease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if _, err = requireFileActionActor(ctx, q, plan, step, false); err != nil {
		return err
	}
	if _, err = prelockEpisodeTransfer(ctx, q, plan.OperationID); err != nil {
		return err
	}
	if err := guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return ErrExecutionRejected
	}
	root, err := q.LockTransferRoot(ctx, plan.RootID)
	if err != nil {
		return err
	}
	if root.MediaType != transferEvidenceMediaType(step.TargetEvidence) || root.Health != "online" || root.Path != plan.RootPath.String || root.Generation != plan.RootGeneration.Int64 {
		return ErrReviewRequired
	}
	if err := lockTransferAcquisition(ctx, q, plan.OperationID, step.UnitID, step.TargetEvidence); err != nil {
		return err
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return err
	}
	if plan.OldFileID.Valid {
		if err := validateOldFile(ctx, q, plan.RootID, step.UnitID, uuid.UUID(plan.OldFileID.Bytes)); err != nil {
			return err
		}
	}
	if _, err = q.UpdateStepRunning(ctx, step.ID); err != nil {
		return err
	}
	if err := guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return ErrExecutionRejected
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return err
	}
	if err = postgres.GuardEpisodeNumberingTime(ctx, q, plan.OperationID); err != nil {
		return ErrReviewRequired
	}
	return tx.Commit(ctx)
}

// targetFile uses the observed destination identity. A copy/reflink has a new
// inode and mtime; indexing its source identity would corrupt scan recovery.
func (s *Service) targetFile(ctx context.Context, q *db.Queries, root db.LibraryRoot, step db.TransferStep, observed ...ports.FileIdentity) (db.LibraryFile, error) {
	target := frozen(step)
	if len(observed) > 1 || step.Action != "hardlink" && len(observed) != 1 {
		return db.LibraryFile{}, ErrReviewRequired
	}
	if len(observed) == 1 {
		target = observed[0]
	}
	fingerprint := pgtype.Text{String: fmt.Sprintf("stat.v1:%d:%d:%d", target.Inode, target.Size, target.Mtime), Valid: true}
	existing, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: step.TargetRel.String})
	if errors.Is(err, pgx.ErrNoRows) {
		return q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: s.idgen.NewUUID(), RootID: root.ID, RelPath: step.TargetRel.String, SizeBytes: target.Size, MtimeNs: target.Mtime, FileFingerprint: fingerprint, LastMutationSeq: root.MutationSeq + 1})
	}
	if err != nil {
		return db.LibraryFile{}, err
	}
	existing, err = q.LockTransferOldFile(ctx, existing.ID)
	if err != nil {
		return db.LibraryFile{}, err
	}
	if existing.RetiredAt.Valid || existing.RootID != root.ID || existing.RelPath != step.TargetRel.String || existing.SizeBytes != target.Size || existing.MtimeNs != target.Mtime || !existing.FileFingerprint.Valid || existing.FileFingerprint.String != fingerprint.String {
		return db.LibraryFile{}, ErrReviewRequired
	}
	units, err := q.ListUnitsForFile(ctx, existing.ID)
	if err != nil {
		return db.LibraryFile{}, err
	}
	if len(units) != 0 {
		return db.LibraryFile{}, ErrReviewRequired
	}
	if err = q.SetMappingFileMutationSeq(ctx, db.SetMappingFileMutationSeqParams{ID: existing.ID, LastMutationSeq: root.MutationSeq + 1}); err != nil {
		return db.LibraryFile{}, err
	}
	return existing, nil
}

func (s *Service) commitStep(ctx context.Context, plan db.TransferPlan, step db.TransferStep, observedAt pgtype.Timestamptz, authority ...ports.JobLease) error {
	if step.Action != "hardlink" {
		return ErrReviewRequired
	}
	return s.commitObservedStep(ctx, plan, step, frozen(step), observedAt, authority...)
}
func (s *Service) commitObservedStep(ctx context.Context, plan db.TransferPlan, step db.TransferStep, target ports.FileIdentity, observedAt pgtype.Timestamptz, authority ...ports.JobLease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	a, actionErr := requireFileActionActor(ctx, q, plan, step, false)
	if actionErr != nil {
		return actionErr
	}
	if a != nil && (a.Content == nil || target.Size != step.SourceSize || target.Device == step.SourceDevice.Int64 && target.Inode == step.SourceInode.Int64) {
		return ErrReviewRequired
	}
	if _, err = prelockEpisodeTransfer(ctx, q, plan.OperationID); err != nil {
		return err
	}
	if err := guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return ErrExecutionRejected
	}
	root, err := q.LockTransferRoot(ctx, plan.RootID)
	if err != nil {
		return err
	}
	if root.MediaType != transferEvidenceMediaType(step.TargetEvidence) || root.Health != "online" || root.Path != plan.RootPath.String || root.Generation != plan.RootGeneration.Int64 {
		return ErrReviewRequired
	}
	if err := lockTransferAcquisition(ctx, q, plan.OperationID, step.UnitID, step.TargetEvidence); err != nil {
		return err
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return err
	}
	if plan.OldFileID.Valid {
		oldID := uuid.UUID(plan.OldFileID.Bytes)
		if err := validateOldFile(ctx, q, plan.RootID, step.UnitID, oldID); err != nil {
			return err
		}
		if err := q.RetireFileByID(ctx, oldID); err != nil {
			return err
		}
		if err := q.RevokeUnitFactsForFile(ctx, plan.OldFileID); err != nil {
			return err
		}
	}
	file, err := s.targetFile(ctx, q, root, step, target)
	if err != nil {
		return err
	}
	if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: step.UnitID}); err != nil {
		return err
	}
	if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: s.idgen.NewUUID(), UnitID: step.UnitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: plan.RootID, Valid: true}, QualityProfile: step.QualityProfile}); err != nil {
		return err
	}
	if _, err = q.CompleteStep(ctx, db.CompleteStepParams{ID: step.ID, TargetFileID: pgtype.UUID{Bytes: file.ID, Valid: true}}); err != nil {
		return err
	}
	if _, err = q.IncrementRootMutationSeq(ctx, plan.RootID); err != nil {
		return err
	}
	if err = q.ImportDownloadRequirements(ctx, db.ImportDownloadRequirementsParams{OperationID: plan.OperationID, UnitID: step.UnitID, RootID: plan.RootID}); err != nil {
		return err
	}
	if err = q.ConsumeTransferReservation(ctx, db.ConsumeTransferReservationParams{OperationID: plan.OperationID, UnitID: step.UnitID, RootID: plan.RootID}); err != nil {
		return err
	}
	var fullEvidence transferTargetEvidence
	_ = json.Unmarshal(step.TargetEvidence, &fullEvidence)
	if fullEvidence.CompleteTVPlanID == uuid.Nil {
		if err = q.ReleaseFinishedAcquisitionLane(ctx, plan.OperationID); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(map[string]any{"operation_id": plan.OperationID.String(), "file_id": file.ID.String(), "unit_id": step.UnitID.String(), "target_path": step.TargetPath})
	if err != nil {
		return err
	}
	if _, err = s.outbox.PublishImported(ctx, q, step.UnitID.String(), json.RawMessage(payload)); err != nil {
		return err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: plan.ExecutionOwner.String, Action: "transfer.imported", TargetType: "transfer_step", TargetID: step.ID.String(), Payload: payload}); err != nil {
		return err
	}
	if fullEvidence.CompleteTVPlanID != uuid.Nil {
		if err = validateCompleteTVTransfer(ctx, q, plan.OperationID, plan.ID); err != nil {
			return err
		}
	}
	// Re-check both authorities after SQL; transactions cannot extend an expired right.
	if err := guardOrganization(ctx, q, authority); err != nil {
		return err
	}
	if _, err = q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
		return ErrExecutionRejected
	}
	if err = guardFileActionSnapshot(ctx, q, plan, step); err != nil {
		return err
	}
	if err = postgres.GuardEpisodeNumberingTime(ctx, q, plan.OperationID); err != nil {
		return ErrReviewRequired
	}
	fresh, err := q.IsTransferObservationFresh(ctx, observedAt)
	if err != nil {
		return err
	}
	if !fresh.Valid || !fresh.Bool {
		return ErrReviewRequired
	}
	if err = s.finishAutomaticTVImportedStep(ctx, q, plan, step, file, authority); err != nil {
		return err
	}
	if fullEvidence.AutomaticTVOperationID != nil {
		if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		valid, e := q.AutomaticTVOrganizationTail(ctx, plan.ID)
		if e != nil {
			return e
		}
		if !valid.Valid || !valid.Bool {
			return ErrExecutionRejected
		}
	}
	return tx.Commit(ctx)
}

// Upgrades use the identical persisted, recoverable execution path, with retirement
// and revocation committed together with the new file, fact and Outbox event.
func (s *Service) UpgradeMediaFile(ctx context.Context, opID, rootID, unitID, oldFileID uuid.UUID, newDownloadedPath, movieTitle string, year int, newQuality quality.Profile) error {
	plan, _, err := s.createMoviePlan(ctx, opID, rootID, unitID, newDownloadedPath, movieTitle, year, newQuality, pgtype.UUID{Bytes: oldFileID, Valid: true})
	if err != nil {
		return err
	}
	if err := s.ExecutePlan(ctx, plan.ID); err != nil {
		return fmt.Errorf("upgrade needs attention: %w", err)
	}
	return nil
}
