package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Only a confirmed single unit can share a lane. Quality acceptance is evaluated
// under the new demand's frozen rules, never the original user's rank.
func (s *AcquisitionStore) attachSharedMovie(ctx context.Context, q *db.Queries, a *ports.Actor, c AcquireMovieCommand, root db.LibraryRoot, rules quality.PolicyRules, lane db.AcquisitionReservation) (*AcquireResult, error) {
	if e := q.LockDownloadAcquisition(ctx, lane.OperationID); e != nil {
		return nil, e
	}
	source, e := q.GetLibraryRootByID(ctx, lane.SourceRootID)
	if e != nil || source.Health != "online" || !source.BaselineCompleted || source.Generation != lane.SourceGeneration {
		return nil, ErrUnitAlreadyReserved
	}
	if c.SubscriptionID != nil {
		blocked, err := q.IsSubscriptionReleaseBlocked(ctx, db.IsSubscriptionReleaseBlockedParams{SubscriptionID: *c.SubscriptionID, CandidateID: lane.CandidateID})
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, ErrUnitAlreadyReserved
		}
	}
	candidate, e := q.LockSubmissionCandidate(ctx, lane.CandidateID)
	if e != nil {
		return nil, e
	}
	if candidate.SourceConfigVersion < 1 {
		return nil, ErrUnitAlreadyReserved
	}
	if candidate.SourceConfigVersion > 0 {
		idx, e := q.LockSubmissionIndexer(ctx, candidate.IndexerID)
		if e != nil || idx.ConfigVersion != candidate.SourceConfigVersion || idx.Health != "online" {
			return nil, ErrUnitAlreadyReserved
		}
	}
	if quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile) != lane.SourceStamp {
		return nil, ErrUnitAlreadyReserved
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil || !quality.EvaluateCandidate(rules, candidate.Title, profile).Passed {
		return nil, ErrUnitAlreadyReserved
	}
	job, e := q.GetAcquisitionSubmissionJob(ctx, lane.OperationID.String())
	if e != nil {
		return nil, ErrUnitAlreadyReserved
	}
	var payload struct {
		ClientID *uuid.UUID `json:"client_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil {
		return nil, ErrUnitAlreadyReserved
	}
	task, taskError := q.LockDownloadTaskByOperation(ctx, lane.OperationID)
	if taskError != nil && !errors.Is(taskError, pgx.ErrNoRows) {
		return nil, taskError
	}
	if taskError == nil && (task.State == "unknown" || task.State == "failed" || task.State == "submitting" || task.OrganizationState == "completed" || task.OrganizationState == "review_required") {
		return nil, ErrUnitAlreadyReserved
	}
	if c.DownloaderID != nil {
		if taskError == nil && task.ClientID != *c.DownloaderID {
			return nil, ErrUnitAlreadyReserved
		}
		if taskError != nil && (payload.ClientID == nil || *payload.ClientID != *c.DownloaderID) {
			return nil, ErrUnitAlreadyReserved
		}
	}
	if taskError != nil && job.State != "queued" && job.State != "running" {
		return nil, ErrUnitAlreadyReserved
	}
	unit, e := q.GetSearchUnit(ctx, c.UnitIDs[0])
	if e != nil {
		return nil, e
	}
	media, e := q.GetMediaByID(ctx, unit.MediaID)
	if e != nil {
		return nil, e
	}
	if c.UnitMetadataVersion > 0 && c.UnitMetadataVersion != unit.MetadataVersion || c.MediaMetadataVersion > 0 && c.MediaMetadataVersion != media.MetadataVersion {
		return nil, ErrRoundAborted
	}
	raw, e := json.Marshal(ports.DownloadDemandFence{RootID: root.ID, UnitID: unit.ID, MediaID: media.ID, ActorID: a.ID, ActorType: a.Type, CandidateID: candidate.ID, SourceStamp: lane.SourceStamp, PolicyID: c.PolicyID, PolicyRevision: c.PolicyRevision, SubscriptionID: c.SubscriptionID, IntentVersion: c.IntentVersion, RootGeneration: root.Generation, UnitMetadataVersion: unit.MetadataVersion, MediaMetadataVersion: media.MetadataVersion})
	if e != nil {
		return nil, e
	}
	reservation, e := q.GetActiveReservation(ctx, db.GetActiveReservationParams{UnitID: unit.ID, RootID: root.ID, Purpose: "manual_acquisition"})
	if errors.Is(e, pgx.ErrNoRows) {
		reservation, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: s.idgen.NewUUID(), UnitID: unit.ID, RootID: root.ID, Purpose: "manual_acquisition", CandidateID: candidate.ID, OperationID: lane.OperationID, ExpiresAt: pgtype.Timestamptz{}})
	}
	if e != nil {
		return nil, e
	}
	if reservation.OperationID != lane.OperationID {
		return nil, ErrUnitAlreadyReserved
	}
	if _, e = q.UpsertSharedRequirement(ctx, db.UpsertSharedRequirementParams{ID: s.idgen.NewUUID(), OperationID: lane.OperationID, UnitID: unit.ID, RootID: root.ID, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: raw}); e != nil {
		return nil, e
	}
	safe, _ := json.Marshal(map[string]any{"operation_id": lane.OperationID, "root_id": root.ID, "unit_id": unit.ID, "candidate_id": candidate.ID})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "acquisition.shared", TargetType: "acquisition", TargetID: lane.OperationID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = s.publishAcquisitionNotification(ctx, q, "acquisition", lane.OperationID.String(), "acquisition.demand_attached", map[string]any{"operation_id": lane.OperationID, "root_id": root.ID, "unit_id": unit.ID}); e != nil {
		return nil, e
	}
	return &AcquireResult{OperationID: lane.OperationID, Reservations: []uuid.UUID{reservation.ID}, JobID: job.ID, Status: "attached", SourceCandidateID: candidate.ID}, nil
}
