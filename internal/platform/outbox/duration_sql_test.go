package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestOutboxRawIntervalsRejectNonPositive(t *testing.T) {
	pool, ob := durationFixture(t)
	q := db.New(pool)
	ctx := context.Background()
	for _, action := range []string{"claim", "fail"} {
		for _, value := range []int32{0, -1} {
			consumer := uuid.NewString()
			id := seedDurationDelivery(t, pool, ob, consumer)
			var d *db.OutboxDelivery
			var err error
			if action == "fail" {
				d, err = ob.Claim(ctx, consumer, "owner", time.Minute)
				if err != nil || d == nil {
					t.Fatalf("claim: %v", err)
				}
			}
			snapshot := func() string {
				var raw string
				if e := pool.QueryRow(ctx, "SELECT to_jsonb(d)::text FROM outbox_delivery d WHERE id=$1", id).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				return raw
			}
			before := snapshot()
			if action == "claim" {
				_, err = q.ClaimOutboxDelivery(ctx, db.ClaimOutboxDeliveryParams{Column1: consumer, Column2: "owner", Column3: value})
			} else {
				_, err = q.FailOutboxDelivery(ctx, db.FailOutboxDeliveryParams{Column1: id, Column2: "owner", Column3: d.LeaseToken, Column4: "synthetic", Column5: value})
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("%s interval %d accepted: %v", action, value, err)
			}
			if snapshot() != before {
				t.Errorf("%s interval %d changed state", action, value)
			}
		}
	}
}
