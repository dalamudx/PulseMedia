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

func TestPublicUpgradeSendConfirmationExpiryPG(t *testing.T) {
	f := newPublicPreparationFixture(t, func(f *publicPreparationFixture) {
		f.exec(t, "UPDATE indexer_query SET finished_at=clock_timestamp()-interval '29 minutes 52 seconds' WHERE id IN (SELECT query_id FROM indexer_query_candidate WHERE candidate_id=$1)", f.request.CandidateID)
	})
	body := publicSendBody(t, f)
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
		t.Fatalf("live prepared confirmation not preserved: %v", err)
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err = f.a.DBPool().Exec(wait, "SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM ((snapshot->>'valid_until')::timestamptz-clock_timestamp())))+0.01) FROM movie_upgrade_authority WHERE operation_id=$1", f.operation); err != nil {
		t.Fatal(err)
	}
	var expired bool
	if err = f.a.DBPool().QueryRow(ctx, "SELECT (snapshot->>'valid_until')::timestamptz<=clock_timestamp() FROM movie_upgrade_authority WHERE operation_id=$1", f.operation).Scan(&expired); err != nil || !expired {
		t.Fatal("did not cross actual confirmation deadline", err)
	}
	if w := f.command("upgrade-sends", body, "expired-confirmation", f.admin, nil); w.Code != 409 {
		t.Fatalf("expired confirmation %d %s", w.Code, w.Body.String())
	}
	if _, err = projector.ProjectWanted(ctx, wanted, sub.QualityPolicyID, sub.QualityPolicyRevision); err != nil {
		t.Fatalf("expired confirmation blocked projection: %v", err)
	}
	var jobs int
	if err = f.a.DBPool().QueryRow(ctx, "SELECT count(*) FROM movie_upgrade_send_job WHERE operation_id=$1", f.operation).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("expired confirmation created job", jobs, err)
	}
}
