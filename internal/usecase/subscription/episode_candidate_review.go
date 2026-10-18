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

type EpisodeCandidateReviewService struct {
	repo ports.EpisodeCandidateReviewRepository
}

func NewEpisodeCandidateReviewService(repo ports.EpisodeCandidateReviewRepository) *EpisodeCandidateReviewService {
	return &EpisodeCandidateReviewService{repo: repo}
}
func (s *EpisodeCandidateReviewService) Review(ctx context.Context, a *ports.Actor, c ports.EpisodeCandidateReviewCommand, key string) (*ports.EpisodeCandidateReviewReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if _, e := uuid.Parse(a.ID); e != nil {
		return nil, ports.ErrForbidden
	}
	if !c.Confirmed || c.SubscriptionID == uuid.Nil || c.CandidateID == uuid.Nil || len(c.ExpectedInputStamp) != 64 || key == "" || len(key) > 160 || strings.TrimSpace(c.Reason) == "" || len([]rune(c.Reason)) > 240 || strings.ContainsAny(c.Reason, "\r\n") || media.UnsafeCandidateTitle(c.Reason) || strings.Contains(strings.ToLower(c.Reason), "token=") {
		return nil, ports.ErrInvalidEpisodeCandidateReview
	}
	decoded, e := hex.DecodeString(c.ExpectedInputStamp)
	if e != nil || len(decoded) != 32 || strings.ToLower(c.ExpectedInputStamp) != c.ExpectedInputStamp {
		return nil, ports.ErrInvalidEpisodeCandidateReview
	}
	raw, e := json.Marshal([]any{"episode.candidate.review.v1", c})
	if e != nil {
		return nil, ports.ErrInvalidEpisodeCandidateReview
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if old, e := s.repo.ReadEpisodeCandidateReviewReceipt(ctx, a, c, key, hash); e != nil || old != nil {
		return old, e
	}
	return s.repo.CommitEpisodeCandidateReview(ctx, a, c, key, hash)
}
