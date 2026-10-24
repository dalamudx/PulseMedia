package httpapi

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestUntrackedReviewHTTPDeadHasNoEvidence(t *testing.T) {
	owner, scope, file, review := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	r := ports.UntrackedReview{Request: ports.UntrackedReviewRequest{ID: review, ScopeID: scope, FileID: file, ActorID: owner, Input: ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: 1, MediaType: "movie"}, JobID: uuid.New(), CreatedAt: time.Now().UTC()}, State: "dead"}
	if !validUntrackedReviewResult(&r, owner.String(), scope, file, review) {
		t.Fatal("readable failed request rejected")
	}
	r.Evidence = &ports.UntrackedReviewEvidence{}
	if validUntrackedReviewResult(&r, owner.String(), scope, file, review) {
		t.Fatal("dead request carried evidence")
	}
}
