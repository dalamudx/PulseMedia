package jobs_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func TestRawJobIntervalsRejectNonPositive(t *testing.T) {
	for _, action := range []string{"claim", "fail", "renew"} {
		t.Run(action, func(t *testing.T) {
			queue, dsn := setupJobsTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			q := postgres.Queries(pool)
			for _, tc := range []struct {
				name   string
				millis int32
			}{{"zero", 0}, {"negative", -1}} {
				t.Run(tc.name, func(t *testing.T) {
					kind := "raw_" + action + "_" + tc.name
					job, err := queue.Enqueue(ctx, kind, "", 0, nil, time.Time{}, 3)
					if err != nil {
						t.Fatal(err)
					}
					if action != "claim" {
						job, err = queue.Claim(ctx, []string{kind}, "raw-owner", 10*time.Second)
						if err != nil || job == nil {
							t.Fatalf("claim: %v", err)
						}
					}
					snapshot := func() string {
						var s string
						if err := pool.QueryRow(ctx, "SELECT to_jsonb(j)::text FROM job j WHERE id=$1", job.ID).Scan(&s); err != nil {
							t.Fatal(err)
						}
						return s
					}
					before := snapshot()
					switch action {
					case "claim":
						_, err = q.ClaimJob(ctx, db.ClaimJobParams{Column1: []string{kind}, Column2: "raw-owner", Column3: tc.millis})
					case "fail":
						_, err = q.FailJob(ctx, db.FailJobParams{Column1: job.ID, Column2: "raw-owner", Column3: job.LeaseToken, Column4: "synthetic failure", Column5: tc.millis})
					case "renew":
						_, err = q.RenewJobLease(ctx, db.RenewJobLeaseParams{Column1: job.ID, Column2: "raw-owner", Column3: job.LeaseToken, Column4: tc.millis})
					}
					if !errors.Is(err, pgx.ErrNoRows) {
						t.Errorf("invalid raw interval accepted: %v", err)
					}
					if snapshot() != before {
						t.Error("invalid raw interval changed job")
					}
				})
			}
		})
	}
}

func TestJobDurationDefaultsAndMaximumRemainUsable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		value        time.Duration
		claimSeconds float64
		retrySeconds float64
	}{{"zero_defaults", 0, 30, 5}, {"negative_defaults", -time.Millisecond, 30, 5}, {"maximum", time.Duration(math.MaxInt32) * time.Millisecond, float64(math.MaxInt32) / 1000, float64(math.MaxInt32) / 1000}} {
		t.Run(tc.name, func(t *testing.T) {
			queue, dsn := setupJobsTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if _, err := queue.Enqueue(ctx, tc.name, "", 0, nil, time.Time{}, 3); err != nil {
				t.Fatal(err)
			}
			job, err := queue.Claim(ctx, []string{tc.name}, "duration-owner", tc.value)
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			var remaining float64
			if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM lease_until-clock_timestamp())::float8 FROM job WHERE id=$1", job.ID).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining < tc.claimSeconds*0.9 || remaining > tc.claimSeconds {
				t.Fatalf("claim duration corrupted: %v", remaining)
			}
			renewal := 10 * time.Second
			expectedRenewal := 10.0
			if tc.name == "maximum" {
				renewal = tc.value
				expectedRenewal = tc.claimSeconds
			}
			if err := queue.RenewLease(ctx, job.ID, "duration-owner", job.LeaseToken, renewal); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM lease_until-clock_timestamp())::float8 FROM job WHERE id=$1", job.ID).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining < expectedRenewal*0.9 || remaining > expectedRenewal {
				t.Fatalf("renewal duration corrupted: %v", remaining)
			}
			if err := queue.Fail(ctx, job.ID, "duration-owner", job.LeaseToken, "synthetic failure", tc.value); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM not_before-clock_timestamp())::float8 FROM job WHERE id=$1 AND state='queued'", job.ID).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining < tc.retrySeconds*0.9 || remaining > tc.retrySeconds {
				t.Fatalf("retry duration corrupted: %v", remaining)
			}
		})
	}
}
