package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
)

func TestMovieUpgradePreparedConfirmationProjectionPG(t *testing.T) {
	f, commands, _, command := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	var wanted uuid.UUID
	if err := f.p.QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.sub, f.unit).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	sub, err := postgres.Queries(f.p).GetSubscriptionByID(ctx, f.sub)
	if err != nil {
		t.Fatal(err)
	}
	_, err = postgres.NewWantedProjectionStore(f.p).ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision)
	if !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatalf("prepared confirmation did not defer automatic projection: %v", err)
	}
	if _, err = commands.QueueMovieUpgradeSend(ctx, f.actor, command, "after-automatic-projection"); err != nil {
		t.Fatalf("confirmed prepared movie cannot be queued after automatic projection: %v", err)
	}
}
