package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type TVCoverageCandidateReviewService struct {
	repo ports.TVCoverageCandidateReviewRepository
}

func NewTVCoverageCandidateReviewService(repo ports.TVCoverageCandidateReviewRepository) *TVCoverageCandidateReviewService {
	return &TVCoverageCandidateReviewService{repo}
}
func coverageReviewActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.ErrForbidden
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}
func (s *TVCoverageCandidateReviewService) Preview(ctx context.Context, a *ports.Actor, id, candidate uuid.UUID) (*ports.TVCoverageCandidateReviewObservation, error) {
	if e := coverageReviewActor(a); e != nil {
		return nil, e
	}
	if id == uuid.Nil || candidate == uuid.Nil {
		return nil, ports.ErrInvalidTVCoverageReview
	}
	return s.repo.ReadTVCoverageCandidate(ctx, a, id, candidate)
}
func (s *TVCoverageCandidateReviewService) Review(ctx context.Context, a *ports.Actor, c ports.TVCoverageCandidateReviewCommand, key string) (*ports.TVCoverageCandidateReviewReceipt, error) {
	if e := coverageReviewActor(a); e != nil {
		return nil, e
	}
	decoded, e := hex.DecodeString(c.ExpectedInputStamp)
	if e != nil || len(decoded) != 32 || strings.ToLower(c.ExpectedInputStamp) != c.ExpectedInputStamp || !c.Confirmed || c.SubscriptionID == uuid.Nil || c.CandidateID == uuid.Nil || key == "" || len(key) > 160 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || strings.ContainsAny(c.Reason, "\r\n") || media.UnsafeCandidateTitle(c.Reason) || strings.Contains(strings.ToLower(c.Reason), "token=") {
		return nil, ports.ErrInvalidTVCoverageReview
	}
	raw, _ := json.Marshal([]any{"tv.coverage.candidate.review.v1", c})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if old, e := s.repo.ReadTVCoverageCandidateReviewReceipt(ctx, a, c, key, hash); e != nil || old != nil {
		return old, e
	}
	return s.repo.CommitTVCoverageCandidateReview(ctx, a, c, key, hash)
}
