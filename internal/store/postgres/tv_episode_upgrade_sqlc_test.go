package postgres_test

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestTVEpisodeUpgradeHistoricalSchemaIsolationPG(t *testing.T) {
	f := newAutomaticTVFixture(t, 136)
	ctx := t.Context()
	op := uuid.New()
	if err := postgres.RejectTVEpisodeUpgradeOperation(ctx, f.pool, op); err != nil {
		t.Fatal("ordinary operation on historical schema", err)
	}
	for _, fence := range []string{`{"goal":"tv_episode_upgrade"}`, `{"kind":"tv_episode_upgrade_v1"}`} {
		op = uuid.New()
		_, err := db.New(f.pool).CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{
			ID: uuid.New(), OperationID: op, UnitID: f.unit, RootID: f.tv,
			ActorType: "user", ActorID: f.actor.ID, SubmissionFence: []byte(fence),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = postgres.RejectTVEpisodeUpgradeOperation(ctx, f.pool, op); !errors.Is(err, ports.ErrTVEpisodeUpgradeObservation) {
			t.Fatalf("dedicated historical demand %s: %v", fence, err)
		}
	}
}
