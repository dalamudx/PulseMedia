package ports

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

var ErrTVSelectionChanged = errors.New("TV selection evidence changed")

// ValidateTVSelectionRequest bounds both work and cross-object lookups before I/O.
func ValidateTVSelectionRequest(r TVSelectionRequest) error {
	if r.SubscriptionID == uuid.Nil || r.Season < 1 || r.Season > 999 || len(r.Packs)+len(r.Episodes) > 100 {
		return quality.ErrInvalidTVSelection
	}
	if err := quality.ValidateTVSelectionPolicy(r.PackPolicy); err != nil {
		return err
	}
	ids := map[uuid.UUID]bool{}
	reviews := map[uuid.UUID]bool{}
	for _, group := range [][]TVSelectionCandidateRef{r.Packs, r.Episodes} {
		for _, ref := range group {
			if ref.CandidateID == uuid.Nil || ref.ReviewID == uuid.Nil || ids[ref.CandidateID] || reviews[ref.ReviewID] {
				return quality.ErrInvalidTVSelection
			}
			ids[ref.CandidateID] = true
			reviews[ref.ReviewID] = true
		}
	}
	return nil
}

