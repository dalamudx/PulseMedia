package httpapi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPublicUpgradeReservedProjectionWindowPG(t *testing.T) {
	f := newPublicPreparationFixture(t)
	ctx := context.Background()
	var wanted uuid.UUID
	if err := f.a.DBPool().QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.request.SubscriptionID, f.unit).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	sub, err := postgres.Queries(f.a.DBPool()).GetSubscriptionByID(ctx, f.request.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	projector := postgres.NewWantedProjectionStore(f.a.DBPool())
	if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatalf("confirmed reservation lost its frozen Wanted before preparation: %v", err)
	}
	items, err := postgres.Queries(f.a.DBPool()).ListPendingEvaluationWantedItems(ctx, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == wanted {
			t.Fatal("confirmed reservation occupied automatic evaluation batch")
		}
	}
	f.noSend(t, 0)
	f.prepared(t, "prepare-after-automatic-evaluation")
	f.noSend(t, 1)
}

func TestPublicUpgradeReservedProjectionExplicitChangesPG(t *testing.T) {
	for _, name := range []string{"skip", "disabled", "revoked"} {
		t.Run(name, func(t *testing.T) {
			f := newPublicPreparationFixture(t)
			ctx := context.Background()
			var wanted uuid.UUID
			if err := f.a.DBPool().QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.request.SubscriptionID, f.unit).Scan(&wanted); err != nil {
				t.Fatal(err)
			}
			sub, err := postgres.Queries(f.a.DBPool()).GetSubscriptionByID(ctx, f.request.SubscriptionID)
			if err != nil {
				t.Fatal(err)
			}
			projector := postgres.NewWantedProjectionStore(f.a.DBPool())
			if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); !errors.Is(err, ports.ErrWantedProjectionChanged) {
				t.Fatal("initial reservation not deferred", err)
			}
			expected := 409
			switch name {
			case "skip":
				f.exec(t, "UPDATE wanted_item SET is_skipped=true WHERE id=$1", wanted)
			case "disabled":
				f.exec(t, "UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1", f.request.SubscriptionID)
			case "revoked":
				f.exec(t, "UPDATE app_user SET is_active=false WHERE id=(SELECT actor_id FROM movie_upgrade_authority WHERE operation_id=$1)", f.operation)
				expected = 401
			}
			if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); err != nil {
				t.Fatal("explicit change did not resume projection", err)
			}
			if w := f.command("upgrade-preparations", f.body(), "changed-confirmed-reservation", f.admin, nil); w.Code != expected {
				t.Fatalf("changed preparation status=%d expected=%d", w.Code, expected)
			}
			if f.fetches.Load() != 0 {
				t.Fatal("changed reservation fetched source")
			}
			f.noSend(t, 0)
		})
	}
}

func TestPublicUpgradeReservedProjectionExpiryPG(t *testing.T) {
	f := newPublicPreparationFixture(t, func(f *publicPreparationFixture) {
		f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '29 minutes 52 seconds' WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=$1)", f.request.CandidateID)
	})
	ctx := context.Background()
	var wanted uuid.UUID
	if err := f.a.DBPool().QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.request.SubscriptionID, f.unit).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	sub, err := postgres.Queries(f.a.DBPool()).GetSubscriptionByID(ctx, f.request.SubscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	projector := postgres.NewWantedProjectionStore(f.a.DBPool())
	if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); !errors.Is(err, ports.ErrWantedProjectionChanged) {
		t.Fatal("live reservation not deferred", err)
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err = f.a.DBPool().Exec(wait, "SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ((snapshot->>'valid_until')::timestamptz-clock_timestamp())))+0.01) FROM movie_upgrade_authority WHERE operation_id=$1", f.operation); err != nil {
		t.Fatal(err)
	}
	var expired bool
	if err = f.a.DBPool().QueryRow(ctx, "SELECT (snapshot->>'valid_until')::timestamptz<=clock_timestamp() FROM movie_upgrade_authority WHERE operation_id=$1", f.operation).Scan(&expired); err != nil || !expired {
		t.Fatal("did not cross real reservation deadline", err)
	}
	if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); err != nil {
		t.Fatal("expired reservation blocked automatic projection", err)
	}
	if w := f.command("upgrade-preparations", f.body(), "expired-confirmed-reservation", f.admin, nil); w.Code != 409 {
		t.Fatalf("expired preparation status=%d", w.Code)
	}
	if f.fetches.Load() != 0 {
		t.Fatal("expired reservation fetched source")
	}
	f.noSend(t, 0)
}
