package httpapi_test

import (
	"context"
	"testing"
	"time"
)

// Observation scheduling runs every 30s; the UNKNOWN return can arrive just
// after the initial pass. Wait for its durable result, not an arbitrary sleep.
func waitPublicUpgradeObservation(t *testing.T, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("scheduled upgrade observation did not complete")
		case <-ticker.C:
		}
	}
}
