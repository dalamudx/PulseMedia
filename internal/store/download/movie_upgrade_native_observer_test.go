package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestMovieUpgradeNativeObservationReturnProof(t *testing.T) {
	binding := ports.MovieUpgradeObservationBinding{ObservationID: uuid.New(), OperationID: uuid.New(), TaskID: uuid.New(), Owner: uuid.New(), Token: uuid.New(), ObservationSHA256: "observation", PermitSHA256: "permit"}
	scope := &movieUpgradeObservationAttempt{}
	proof := &movieUpgradeObservationReturn{issuer: scope, binding: binding, outcome: "exact_match", returnedAt: time.Now()}
	scope.returned = proof
	if _, err := scope.VerifyMovieUpgradeObservation(context.Background(), proof, binding); err != nil {
		t.Fatal(err)
	}
	copy := *proof
	other := &movieUpgradeObservationAttempt{returned: proof}
	changed := binding
	changed.Token = uuid.New()
	for _, tc := range []struct {
		name    string
		scope   *movieUpgradeObservationAttempt
		proof   any
		binding ports.MovieUpgradeObservationBinding
	}{
		{"caller_boolean", scope, struct{ Returned bool }{true}, binding},
		{"copied", scope, &copy, binding},
		{"different_attempt", other, proof, binding},
		{"wrong_binding", scope, proof, changed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.scope.VerifyMovieUpgradeObservation(context.Background(), tc.proof, tc.binding); err == nil {
				t.Fatal("untrusted proof accepted")
			}
		})
	}
	scope.running = true
	if _, err := scope.VerifyMovieUpgradeObservation(context.Background(), proof, binding); err == nil {
		t.Fatal("unreturned invocation accepted")
	}
	scope.running = false
	scope.closed = true
	if _, err := scope.VerifyMovieUpgradeObservation(context.Background(), proof, binding); err == nil {
		t.Fatal("closed attempt accepted")
	}
}
