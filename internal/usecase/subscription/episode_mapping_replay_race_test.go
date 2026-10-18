package subscription_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"strings"
	"testing"
)

// Deterministic contract probe: a peer's atomic commit occurs between cache miss
// and source observation. The real PG eight-writer test covers commit atomicity.
func (r *mappingCacheMissRepo) ReadEpisodeCandidate(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (*ports.EpisodeCandidateObservation, error) {
	return nil, ports.ErrUnavailable
}

type mappingCacheMissRepo struct {
	receipt     ports.EpisodeMappingReceipt
	reads       int
	failPreview bool
	published   bool
}

func (r *mappingCacheMissRepo) ReadEpisodeSourceContext(context.Context, *ports.Actor, uuid.UUID) (*ports.EpisodeSourceContext, error) {
	if r.failPreview {
		return nil, ports.ErrUnavailable
	}
	return &ports.EpisodeSourceContext{SubscriptionID: r.receipt.SubscriptionID, MediaID: uuid.New(), InputStamp: strings.Repeat("b", 64), MappingRevision: 1, IntentVersion: 2}, nil
}
func (r *mappingCacheMissRepo) ReadEpisodeMappingReceipt(context.Context, *ports.Actor, uuid.UUID, string, string) (*ports.EpisodeMappingReceipt, error) {
	r.reads++
	if r.reads == 1 {
		return nil, nil
	}
	copy := r.receipt
	return &copy, nil
}
func (r *mappingCacheMissRepo) PublishEpisodeMapping(context.Context, *ports.Actor, ports.EpisodeMappingPreview, string, string) (*ports.EpisodeMappingReceipt, error) {
	r.published = true
	return nil, errors.New("duplicate publication")
}
func (r *mappingCacheMissRepo) ReadEpisodeMapping(context.Context, *ports.Actor, uuid.UUID) (*ports.EpisodeMappingState, error) {
	return nil, ports.ErrUnavailable
}
func TestEpisodeMappingCacheMissThenPeerCommitRecoversOriginalReceipt(t *testing.T) {
	for _, failed := range []bool{false, true} {
		r := &mappingCacheMissRepo{receipt: ports.EpisodeMappingReceipt{SubscriptionID: uuid.New(), MappingRevision: 1, IntentVersion: 2, Mode: "standard", SourceHash: strings.Repeat("c", 64)}, failPreview: failed}
		s := subscription.NewEpisodeMappingService(r, nil)
		cmd := ports.EpisodeMappingCommand{Draft: ports.EpisodeMappingDraft{Mode: "standard"}, ExpectedInputStamp: strings.Repeat("a", 64), ExpectedSourceHash: strings.Repeat("c", 64), Confirmed: true}
		admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
		out, e := s.Publish(context.Background(), admin, r.receipt.SubscriptionID, cmd, "same-key")
		if e != nil || out == nil || *out != r.receipt || r.reads != 2 || r.published {
			t.Fatal("cache miss/new observation incorrectly treated as conflict", failed, out, e, r.reads)
		}
		if _, e = s.Publish(context.Background(), &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleMember}, r.receipt.SubscriptionID, cmd, "same-key"); !errors.Is(e, ports.ErrForbidden) || r.reads != 2 {
			t.Fatal("receipt recovery elevated actor", e)
		}
	}
}
