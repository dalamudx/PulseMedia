package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

type AcquireMovieCommand = ports.AcquireMovieCommand
type AcquireResult = ports.AcquireResult
type RoundSnapshot = ports.RoundSnapshot
type RoundDecision = ports.RoundDecision
type RoundJobAuthority = ports.RoundJobAuthority

var ErrUnsupportedCoverage = ports.ErrAcquisitionUnsupportedCoverage
var ErrOwnershipUncertain = ports.ErrAcquisitionOwnershipUncertain
var ErrAlreadyOwned = ports.ErrAcquisitionAlreadyOwned
var ErrRootNotOnline = ports.ErrAcquisitionRootNotOnline
var ErrLocalReuseRequired = ports.ErrAcquisitionLocalReuseRequired
var ErrUnitAlreadyReserved = ports.ErrAcquisitionUnitAlreadyReserved
var ErrRoundAborted = ports.ErrAcquisitionRoundAborted
var ErrRoundNotReady = ports.ErrAcquisitionRoundNotReady

type AcquisitionStore struct {
	pool     *pgxpool.Pool
	idgen    ports.IDGen
	clock    ports.Clock
	queries  *db.Queries
	selector ports.MovieRoundSelector
}

func NewAcquisitionStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock, selector ports.MovieRoundSelector) *AcquisitionStore {
	return &AcquisitionStore{pool: pool, idgen: ids, clock: clock, queries: db.New(pool), selector: selector}
}

var _ ports.AcquisitionRepository = (*AcquisitionStore)(nil)

// AcquireMovie executes an atomic Unit of Work: validates ownership, reserves all covered units (all-or-nothing),
// saves requirements, enqueues the submission job, publishes outbox event, and records audit.
func (s *AcquisitionStore) AcquireMovie(ctx context.Context, actor *ports.Actor, cmd AcquireMovieCommand) (out *AcquireResult, err error) {
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
		for _, allowed := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrBadResponse, ports.ErrAcquisitionCommandConflict, ErrUnsupportedCoverage, ErrOwnershipUncertain, ErrAlreadyOwned, ErrRootNotOnline, ErrLocalReuseRequired, ErrUnitAlreadyReserved, ErrRoundAborted} {
			if errors.Is(err, allowed) {
				return
			}
		}
		err = ports.ErrUnavailable
	}()

	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if len(cmd.UnitIDs) == 0 {
		return nil, ports.ErrBadResponse
	}
	if e := acquisitionCommandActor(actor); e != nil {
		return nil, e
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin acquisition tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	qtx := s.queries.WithTx(tx)
	if cmd.IdempotencyKey != "" {
		cached, e := prepareAcquisitionCommand(ctx, qtx, actor, cmd)
		if e != nil {
			return nil, e
		}
		if cached != nil {
			if e = tx.Commit(ctx); e != nil {
				return nil, e
			}
			return cached, nil
		}
	}
	if cmd.IdempotencyKey == "" {
		if e := lockAcquisitionUser(ctx, qtx, actor); e != nil {
			return nil, e
		}
	}
	// Subscription demand is authorized only by the internal round UoW.
	if cmd.SubscriptionID != nil {
		return nil, ports.ErrForbidden
	}
	root, err := qtx.GetLibraryRootByID(ctx, cmd.RootID)
	if err != nil {
		return nil, fmt.Errorf("failed to load target library root: %w", err)
	}
	if root.Health != "online" {
		return nil, fmt.Errorf("%w: root health is %s", ErrRootNotOnline, root.Health)
	}
	if cmd.DownloaderID != nil {
		if _, err = qtx.GetDownloadClientByID(ctx, *cmd.DownloaderID); err != nil {
			return nil, fmt.Errorf("selected download client not found: %w", err)
		}
	}
	policy, err := qtx.LockSearchPolicy(ctx, "default")
	if err != nil {
		return nil, err
	}
	revision, err := qtx.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: policy.ID, Revision: policy.CurrentRevision})
	if err != nil {
		return nil, err
	}
	var rules quality.PolicyRules
	if err = json.Unmarshal(revision.Rules, &rules); err != nil {
		return nil, err
	}
	res, err := s.reserveMovie(ctx, qtx, actor, cmd, root, rules)
	if err != nil {
		return nil, err
	}
	if cmd.IdempotencyKey != "" {
		if err = s.saveAcquisitionCommand(ctx, qtx, actor, cmd, res); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// reserveMovie is private to acquisition's narrow UoW; no transaction escapes to callers.
func (s *AcquisitionStore) reserveMovie(ctx context.Context, qtx *db.Queries, actor *ports.Actor, cmd AcquireMovieCommand, root db.LibraryRoot, rules quality.PolicyRules) (*AcquireResult, error) {
	// Preflight is not authority: a concurrent import can consume the reservation
	// after the earlier ownership read. Serialize root -> sorted units and re-read.
	if len(cmd.UnitIDs) != 1 {
		return nil, ErrUnsupportedCoverage
	}
	lockedRoot, err := qtx.LockAcquisitionRoot(ctx, cmd.RootID)
	if err != nil {
		return nil, err
	}
	if lockedRoot.MediaType != "movie" {
		return nil, ErrUnsupportedCoverage
	}
	if lockedRoot.Health != "online" || !lockedRoot.BaselineCompleted || lockedRoot.Generation != root.Generation || lockedRoot.Path != root.Path {
		return nil, ErrRootNotOnline
	}
	cmd.UnitIDs = append([]uuid.UUID(nil), cmd.UnitIDs...)
	sort.Slice(cmd.UnitIDs, func(i, j int) bool { return cmd.UnitIDs[i].String() < cmd.UnitIDs[j].String() })
	for i, unitID := range cmd.UnitIDs {
		if i > 0 && unitID == cmd.UnitIDs[i-1] {
			return nil, errors.New("duplicate acquisition unit")
		}
		unit, err := qtx.LockAcquisitionUnit(ctx, unitID)
		if err != nil {
			return nil, err
		}
		target, e := qtx.GetMediaByID(ctx, unit.MediaID)
		if e != nil {
			return nil, e
		}
		if unit.UnitKey != domainMedia.FormatMovieUnitKey() || target.MediaType != "movie" {
			return nil, ErrUnsupportedCoverage
		}
		if unit.Retired {
			return nil, errors.New("acquisition unit retired")
		}
		files, err := qtx.ListAcquisitionOwnershipEvidence(ctx, db.ListAcquisitionOwnershipEvidenceParams{UnitID: unitID, RootID: cmd.RootID})
		if err != nil {
			return nil, err
		}
		evidence := make([]quality.FileEvidence, 0, len(files))
		for _, file := range files {
			var profile quality.Profile
			if json.Unmarshal(file.QualityProfile, &profile) != nil {
				profile = quality.Profile{}
			}
			evidence = append(evidence, quality.FileEvidence{Title: file.RelPath, Profile: profile, Verified: file.Stage.Valid})
		}
		owned := quality.AssessOwnership(rules, evidence)
		if owned.Owned {
			return nil, ErrAlreadyOwned
		}
		if owned.Unknown {
			return nil, ErrOwnershipUncertain
		}
	}

	if cmd.PolicyID == "" {
		p, e := qtx.GetPolicyByID(ctx, "default")
		if e != nil {
			return nil, e
		}
		cmd.PolicyID = p.ID
		cmd.PolicyRevision = p.CurrentRevision
	}
	if cmd.SubscriptionID != nil {
		blocked, e := qtx.IsSubscriptionReleaseBlocked(ctx, db.IsSubscriptionReleaseBlockedParams{SubscriptionID: *cmd.SubscriptionID, CandidateID: cmd.CandidateID})
		if e != nil {
			return nil, e
		}
		if blocked {
			return nil, ErrUnitAlreadyReserved
		}
	}
	// The unit lock serializes global lane creation even across different roots.
	for _, unitID := range cmd.UnitIDs {
		if _, e := qtx.GetActiveReservationInRoot(ctx, db.GetActiveReservationInRootParams{UnitID: unitID, RootID: cmd.RootID}); e == nil {
			lane, le := qtx.LockActiveAcquisitionLane(ctx, unitID)
			if le != nil || lane.LegacyConflict || lane.SourceStamp == "" {
				return nil, ErrUnitAlreadyReserved
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	for _, unitID := range cmd.UnitIDs {
		lane, e := qtx.LockActiveAcquisitionLane(ctx, unitID)
		if e == nil {
			if len(cmd.UnitIDs) != 1 || lane.LegacyConflict || lane.SourceStamp == "" {
				return nil, ErrUnitAlreadyReserved
			}
			return s.attachSharedMovie(ctx, qtx, actor, cmd, root, rules, lane)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	candidate, e := qtx.LockSubmissionCandidate(ctx, cmd.CandidateID)
	if e != nil {
		return nil, e
	}
	coverage := domainMedia.RecognizeRelease(candidate.Title)
	if domainMedia.UnsafeCandidateTitle(candidate.Title) || coverage.MediaType != domainMedia.TypeMovie || coverage.RequiresReview || len(coverage.UnitKeys) != 1 || coverage.UnitKeys[0] != domainMedia.FormatMovieUnitKey() {
		return nil, ErrUnsupportedCoverage
	}
	for _, unitID := range cmd.UnitIDs {
		other, e := qtx.ListPotentialReusableFiles(ctx, db.ListPotentialReusableFilesParams{UnitID: unitID, RootID: cmd.RootID})
		if e != nil {
			return nil, e
		}
		for _, file := range other {
			var p quality.Profile
			_ = json.Unmarshal(file.QualityProfile, &p)
			if !file.Stage.Valid || !quality.KnownProfile(p) {
				return nil, ErrOwnershipUncertain
			}
			if quality.EvaluateStoredFile(rules, file.RelPath, p).Passed {
				return nil, ErrLocalReuseRequired
			}
		}
	}
	if candidate.SourceConfigVersion < 1 {
		return nil, ErrOwnershipUncertain
	}
	source, e := qtx.LockSubmissionIndexer(ctx, candidate.IndexerID)
	if e != nil || source.ConfigVersion != candidate.SourceConfigVersion || source.Health != "online" {
		return nil, ErrOwnershipUncertain
	}
	operationID := s.idgen.NewUUID()
	for _, unitID := range cmd.UnitIDs {
		if _, e = qtx.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: s.idgen.NewUUID(), UnitID: unitID, OperationID: operationID, CandidateID: cmd.CandidateID, SourceRootID: cmd.RootID, SourceGeneration: root.Generation, SourceStamp: quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile)}); e != nil {
			return nil, e
		}
	}
	reservationIDs := make([]uuid.UUID, 0, len(cmd.UnitIDs))

	// All-or-nothing reservation for each covered unit
	for _, unitID := range cmd.UnitIDs {
		resID := s.idgen.NewUUID()
		_, err := qtx.CreateUnitReservation(ctx, db.CreateUnitReservationParams{
			ID:          resID,
			UnitID:      unitID,
			RootID:      cmd.RootID,
			Purpose:     "manual_acquisition",
			CandidateID: cmd.CandidateID,
			OperationID: operationID,
			ExpiresAt:   pgtype.Timestamptz{Time: s.clock.Now().Add(24 * time.Hour), Valid: true},
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return nil, fmt.Errorf("%w: unit %s is already reserved", ErrUnitAlreadyReserved, unitID)
			}
			return nil, fmt.Errorf("failed to reserve unit %s: %w", unitID, err)
		}
		reservationIDs = append(reservationIDs, resID)

		// Insert download requirement
		unit, e := qtx.GetSearchUnit(ctx, unitID)
		if e != nil {
			return nil, e
		}
		media, e := qtx.GetMediaByID(ctx, unit.MediaID)
		if e != nil {
			return nil, e
		}
		if (cmd.UnitMetadataVersion > 0 && unit.MetadataVersion != cmd.UnitMetadataVersion) || (cmd.MediaMetadataVersion > 0 && media.MetadataVersion != cmd.MediaMetadataVersion) {
			return nil, ErrRoundAborted
		}
		fenceRaw, e := json.Marshal(ports.DownloadDemandFence{RootID: cmd.RootID, UnitID: unit.ID, MediaID: media.ID, ActorID: actor.ID, ActorType: actor.Type, CandidateID: candidate.ID, SourceStamp: quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile), PolicyID: cmd.PolicyID, PolicyRevision: cmd.PolicyRevision, SubscriptionID: cmd.SubscriptionID, IntentVersion: cmd.IntentVersion, RootGeneration: root.Generation, UnitMetadataVersion: unit.MetadataVersion, MediaMetadataVersion: media.MetadataVersion})
		if e != nil {
			return nil, e
		}
		reqID := s.idgen.NewUUID()
		_, err = qtx.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{
			ID:          reqID,
			OperationID: operationID,
			UnitID:      unitID,
			RootID:      cmd.RootID,
			ActorType:   string(actor.Type),
			ActorID:     actor.ID, SubmissionFence: fenceRaw,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to insert download requirement: %w", err)
		}
	}

	// Enqueue download submission job
	jobPayload, _ := json.Marshal(map[string]any{
		"operation_id":    operationID.String(),
		"candidate_id":    cmd.CandidateID.String(),
		"root_id":         cmd.RootID.String(),
		"root_generation": root.Generation,
		"client_id":       cmd.DownloaderID,
		"unit_ids":        cmd.UnitIDs,
	})
	jobID := s.idgen.NewUUID()
	_, err = qtx.EnqueueJob(ctx, db.EnqueueJobParams{
		ID:          jobID,
		Kind:        "download_submission",
		DedupeKey:   pgtype.Text{String: operationID.String(), Valid: true},
		Priority:    10,
		Payload:     jobPayload,
		MaxAttempts: 3,
		NotBefore:   pgtype.Timestamptz{Time: s.clock.Now(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue download job: %w", err)
	}

	// Publish outbox event
	_, err = s.publishAcquisitionNotification(ctx, qtx, "acquisition", operationID.String(), "acquisition.initiated", map[string]any{
		"operation_id": operationID.String(),
		"candidate_id": cmd.CandidateID.String(),
		"unit_ids":     cmd.UnitIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to publish outbox event: %w", err)
	}

	// Insert audit log
	auditPayload, _ := json.Marshal(cmd)
	_, err = qtx.InsertAuditLog(ctx, db.InsertAuditLogParams{
		ID:         s.idgen.NewUUID(),
		ActorType:  string(actor.Type),
		ActorID:    actor.ID,
		Action:     "acquisition.create",
		TargetType: "media_unit",
		TargetID:   cmd.UnitIDs[0].String(),
		Payload:    auditPayload,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert audit log: %w", err)
	}

	return &AcquireResult{
		OperationID:  operationID,
		Reservations: reservationIDs,
		JobID:        jobID,
		Status:       "dispatched", SourceCandidateID: cmd.CandidateID,
	}, nil
}

// PreSubmitRecheck verifies that the target subscription is still active before submitting physical transfer.
func (s *AcquisitionStore) PreSubmitRecheck(ctx context.Context, subID uuid.UUID) (bool, error) {
	sub, err := s.queries.GetSubscriptionByID(ctx, subID)
	if err != nil {
		return false, fmt.Errorf("subscription not found: %w", err)
	}
	return sub.State == "active", nil
}
