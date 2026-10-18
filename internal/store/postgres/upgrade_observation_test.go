package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func frozenClientDigest(t *testing.T, f *frozenUpgradeFixture) string {
	t.Helper()
	v, e := f.store.ReadFrozenUpgradeObservation(context.Background(), f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: f.candidate})
	if e != nil {
		t.Fatal(e)
	}
	if v.ClientID != f.client || len(v.ClientConfigDigest) != 64 {
		t.Fatal("invalid observation", v)
	}
	return v.ClientConfigDigest
}
