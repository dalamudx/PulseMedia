package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedReviewApplication interface {
	Request(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, ports.UntrackedReviewInput, string) (*ports.UntrackedReviewRequestReceipt, error)
	RequestReceipt(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.UntrackedReviewRequestReceipt, error)
	Read(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*ports.UntrackedReview, error)
	Confirm(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, ports.UntrackedConfirmationInput, string) (*ports.UntrackedConfirmationReceipt, error)
	ConfirmationReceipt(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.UntrackedConfirmationReceipt, error)
}

func (s *ServerHandler) SetUntrackedReviewService(service untrackedReviewApplication) {
	s.untrackedReviewSvc = service
}

func untrackedReviewProblem(ctx context.Context, err error) untrackedScopeErrorResponse {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.Is(err, ports.ErrUntrackedReviewStale) {
		err = ports.ErrEndpointVersionConflict
	}
	return untrackedScopeProblem(ctx, err)
}

func (s *ServerHandler) RequestUntrackedReview(ctx context.Context, r RequestUntrackedReviewRequestObject) (RequestUntrackedReviewResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	input := ports.UntrackedReviewInput{ScopeVersion: r.Body.ScopeVersion, FileRevision: r.Body.FileRevision, MediaType: string(r.Body.MediaType), TMDBID: r.Body.TmdbId}
	if !validUntrackedReviewInput(input) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedReviewSvc == nil {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedReviewSvc.Request(ctx, actor, r.ScopeId, r.FileId, input, r.Params.IdempotencyKey)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if !validUntrackedReviewRequestReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.Params.IdempotencyKey) || receipt.Request.Input != input {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return RequestUntrackedReview202JSONResponse{Body: untrackedReviewRequestReceiptResponse(*receipt)}, nil
}

func (s *ServerHandler) ReadUntrackedReviewRequestReceipt(ctx context.Context, r ReadUntrackedReviewRequestReceiptRequestObject) (ReadUntrackedReviewRequestReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedReviewSvc == nil {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedReviewSvc.RequestReceipt(ctx, actor, r.ScopeId, r.FileId, r.CommandKey)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if !validUntrackedReviewRequestReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.CommandKey) {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadUntrackedReviewRequestReceipt200JSONResponse{Body: untrackedReviewRequestReceiptResponse(*receipt)}, nil
}

func (s *ServerHandler) ReadUntrackedReview(ctx context.Context, r ReadUntrackedReviewRequestObject) (ReadUntrackedReviewResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.ReviewId == uuid.Nil {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedReviewSvc == nil {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	review, err := s.untrackedReviewSvc.Read(ctx, actor, r.ScopeId, r.FileId, r.ReviewId)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if !validUntrackedReviewResult(review, actor.ID, r.ScopeId, r.FileId, r.ReviewId) {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadUntrackedReview200JSONResponse{Body: untrackedReviewResponse(*review)}, nil
}

func (s *ServerHandler) ConfirmUntrackedFile(ctx context.Context, r ConfirmUntrackedFileRequestObject) (ConfirmUntrackedFileResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	input := ports.UntrackedConfirmationInput{
		ReviewID: r.Body.ReviewId, MaterialHash: r.Body.MaterialHash, ScopeVersion: r.Body.ScopeVersion,
		FileRevision: r.Body.FileRevision, CandidateKey: r.Body.CandidateKey, Action: string(r.Body.Action), TargetRelativePath: r.Body.TargetRelativePath,
	}
	if !validUntrackedConfirmationInput(input) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedReviewSvc == nil {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedReviewSvc.Confirm(ctx, actor, r.ScopeId, r.FileId, input, r.Params.IdempotencyKey)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if !validUntrackedConfirmationReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.Params.IdempotencyKey) || receipt.Material.Input != input {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return ConfirmUntrackedFile200JSONResponse{Body: untrackedConfirmationReceiptResponse(*receipt)}, nil
}

func (s *ServerHandler) ReadUntrackedConfirmationReceipt(ctx context.Context, r ReadUntrackedConfirmationReceiptRequestObject) (ReadUntrackedConfirmationReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return untrackedReviewProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedReviewSvc == nil {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedReviewSvc.ConfirmationReceipt(ctx, actor, r.ScopeId, r.FileId, r.CommandKey)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if !validUntrackedConfirmationReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.CommandKey) {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadUntrackedConfirmationReceipt200JSONResponse{Body: untrackedConfirmationReceiptResponse(*receipt)}, nil
}

func validUntrackedReviewTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999
}

func validUntrackedReviewRequest(r ports.UntrackedReviewRequest, actor string, scopeID, fileID uuid.UUID) bool {
	return r.ID != uuid.Nil && r.JobID != uuid.Nil && r.ActorID.String() == actor && r.ScopeID == scopeID && r.FileID == fileID &&
		validUntrackedReviewInput(r.Input) && validUntrackedReviewTime(r.CreatedAt)
}

func validUntrackedReviewRequestReceipt(r *ports.UntrackedReviewRequestReceipt, actor string, scopeID, fileID uuid.UUID, key string) bool {
	return r != nil && !r.ExecutionAllowed && r.CommandKey == key && validUntrackedReviewRequest(r.Request, actor, scopeID, fileID)
}

func validUntrackedReviewResult(r *ports.UntrackedReview, actor string, scopeID, fileID, reviewID uuid.UUID) bool {
	if r == nil || r.ExecutionAllowed || r.Request.ID != reviewID || !validUntrackedReviewRequest(r.Request, actor, scopeID, fileID) {
		return false
	}
	switch r.State {
	case "queued", "running", "dead":
		return r.Evidence == nil && r.RecordedAt == nil && r.MaterialHash == ""
	case "ready", "review_required":
		if r.Evidence == nil || !validUntrackedReviewHash(r.MaterialHash) || r.RecordedAt == nil || !validUntrackedReviewTime(*r.RecordedAt) || r.RecordedAt.Before(r.Request.CreatedAt) {
			return false
		}
	default:
		return false
	}
	e := r.Evidence
	selection := "automatic"
	if r.Request.Input.TMDBID > 0 {
		selection = "explicit"
	}
	if e.ParserVersion == "" || e.Selection != selection || (r.State == "ready") != e.Complete {
		return false
	}
	if !e.Complete {
		return e.Candidate == nil && e.Reason != "" && e.Reason != "ready"
	}
	c := e.Candidate
	if c != nil {
		for _, unit := range c.Units {
			if unit.UnitKey == "" || unit.TMDBEpisodeID < 0 || unit.Season < 0 || unit.Episode < 0 || (unit.AirDate != nil && !validUntrackedReviewTime(*unit.AirDate)) {
				return false
			}
		}
	}
	return c != nil && e.Reason == "ready" && c.Namespace == "tmdb."+r.Request.Input.MediaType && c.TMDBID > 0 &&
		c.Key == c.Namespace+":"+strconv.FormatInt(c.TMDBID, 10) && c.Title != "" && len(c.Units) > 0 &&
		(r.Request.Input.TMDBID == 0 || c.TMDBID == r.Request.Input.TMDBID)
}

func validUntrackedConfirmationReceipt(r *ports.UntrackedConfirmationReceipt, actor string, scopeID, fileID uuid.UUID, key string) bool {
	if r == nil || r.ExecutionAllowed || r.CommandKey != key || r.State != "confirmed_pending_organization" {
		return false
	}
	m := r.Material
	in := m.Input
	return m.ID != uuid.Nil && m.ActorID.String() == actor && validUntrackedReviewTime(m.CreatedAt) && !m.CreatedAt.Before(m.Review.Request.CreatedAt) && validUntrackedConfirmationInput(in) &&
		m.Scope.Enabled && m.Scope.ClientID != uuid.Nil && m.Scope.TargetRootID != uuid.Nil && m.Scope.TargetBaselineScanID != uuid.Nil &&
		validUntrackedReviewTime(m.Scope.ClientVersion) && validUntrackedReviewTime(m.Scope.CreatedAt) && validUntrackedReviewTime(m.Scope.UpdatedAt) &&
		m.Scope.SourceIdentity.Device >= 0 && m.Scope.SourceIdentity.Inode > 0 && m.Scope.TargetIdentity.Device >= 0 && m.Scope.TargetIdentity.Inode > 0 &&
		m.Scope.TargetRootGeneration >= 1 && m.File.Identity.Device >= 0 && m.File.Identity.Inode > 0 && m.File.Identity.Size >= 0 && m.File.Observations >= 1 &&
		validUntrackedReviewTime(m.File.FirstObservedAt) && validUntrackedReviewTime(m.File.LastObservedAt) && !m.File.LastObservedAt.Before(m.File.FirstObservedAt) &&
		m.Scope.ID == scopeID && m.Scope.OwnerID.String() == actor && m.Scope.Version == in.ScopeVersion &&
		m.Scope.TargetMediaType == m.Review.Request.Input.MediaType && m.File.ID == fileID && m.File.ScopeID == scopeID &&
		m.File.ScopeVersion == in.ScopeVersion && m.File.Revision == in.FileRevision &&
		validUntrackedReviewResult(&m.Review, actor, scopeID, fileID, in.ReviewID) && !m.Review.Stale && m.Review.State == "ready" &&
		m.Review.Request.Input.ScopeVersion == in.ScopeVersion && m.Review.Request.Input.FileRevision == in.FileRevision &&
		m.Review.MaterialHash == in.MaterialHash && m.Review.Evidence.Candidate.Key == in.CandidateKey
}

func untrackedReviewInputResponse(in ports.UntrackedReviewInput) UntrackedReviewInput {
	return UntrackedReviewInput{ScopeVersion: in.ScopeVersion, FileRevision: in.FileRevision, MediaType: in.MediaType, TmdbId: in.TMDBID}
}

func untrackedReviewRequestResponse(r ports.UntrackedReviewRequest) UntrackedReviewRequest {
	return UntrackedReviewRequest{Id: r.ID, ScopeId: r.ScopeID, FileId: r.FileID, ActorId: r.ActorID, Input: untrackedReviewInputResponse(r.Input), JobId: r.JobID, CreatedAt: r.CreatedAt}
}

func untrackedReviewRequestReceiptResponse(r ports.UntrackedReviewRequestReceipt) UntrackedReviewRequestReceipt {
	return UntrackedReviewRequestReceipt{CommandKey: r.CommandKey, Request: untrackedReviewRequestResponse(r.Request), ExecutionAllowed: false}
}

func untrackedReviewResponse(r ports.UntrackedReview) UntrackedReviewView {
	out := UntrackedReviewView{Request: untrackedReviewRequestResponse(r.Request), State: r.State, MaterialHash: r.MaterialHash, RecordedAt: r.RecordedAt, Stale: r.Stale, ExecutionAllowed: false}
	if e := r.Evidence; e != nil {
		out.Evidence = &UntrackedReviewEvidence{ParserVersion: e.ParserVersion, ParsedTitle: e.ParsedTitle, ParsedYear: e.ParsedYear, Selection: e.Selection, Complete: e.Complete, Reason: e.Reason}
		if c := e.Candidate; c != nil {
			units := make([]UntrackedReviewUnit, 0, len(c.Units))
			for _, u := range c.Units {
				units = append(units, UntrackedReviewUnit{UnitKey: u.UnitKey, TmdbEpisodeId: u.TMDBEpisodeID, Season: u.Season, Episode: u.Episode, AirDate: u.AirDate})
			}
			out.Evidence.Candidate = &UntrackedReviewCandidate{Key: c.Key, Namespace: c.Namespace, TmdbId: c.TMDBID, Title: c.Title, OriginalTitle: c.OriginalTitle, Year: c.Year, Units: units}
		}
	}
	return out
}

func untrackedConfirmationReceiptResponse(r ports.UntrackedConfirmationReceipt) UntrackedConfirmationReceipt {
	m := r.Material
	f := m.File
	return UntrackedConfirmationReceipt{
		CommandKey: r.CommandKey, State: r.State, ExecutionAllowed: false,
		Material: UntrackedConfirmationMaterial{
			Id: m.ID, ActorId: m.ActorID, Scope: untrackedScopeResponse(m.Scope), Review: untrackedReviewResponse(m.Review), CreatedAt: m.CreatedAt,
			File:  UntrackedDiscoveryObservation{Id: f.ID, ScopeId: f.ScopeID, ScopeVersion: f.ScopeVersion, Revision: f.Revision, RelativePath: f.RelativePath, Device: f.Identity.Device, Inode: f.Identity.Inode, Size: f.Identity.Size, Mtime: f.Identity.Mtime, FirstObservedAt: f.FirstObservedAt, LastObservedAt: f.LastObservedAt, Observations: f.Observations},
			Input: UntrackedConfirmationInput{ReviewId: m.Input.ReviewID, MaterialHash: m.Input.MaterialHash, ScopeVersion: m.Input.ScopeVersion, FileRevision: m.Input.FileRevision, CandidateKey: m.Input.CandidateKey, Action: m.Input.Action, TargetRelativePath: m.Input.TargetRelativePath},
		},
	}
}

func (r untrackedScopeErrorResponse) VisitRequestUntrackedReviewResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedReviewResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedReviewRequestReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitConfirmUntrackedFileResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedConfirmationReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
