package jobs_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestInvalidJobDurationsNeverChangeState(t *testing.T) {
	for _, action := range []string{"claim", "fail", "renew"} {
		t.Run(action, func(t *testing.T) {
			queue, dsn := setupJobsTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			cases := []struct {
				name     string
				duration time.Duration
			}{{"sub_millisecond", time.Nanosecond}, {"overflow_sign", (time.Duration(math.MaxInt32) + 1) * time.Millisecond}, {"overflow_positive", ((1 << 32) + 1000) * time.Millisecond}}
			if action == "renew" {
				cases = append(cases, struct {
					name     string
					duration time.Duration
				}{"zero", 0}, struct {
					name     string
					duration time.Duration
				}{"negative", -time.Millisecond})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					kind := action + "_" + tc.name
					job, err := queue.Enqueue(ctx, kind, "", 0, nil, time.Time{}, 3)
					if err != nil {
						t.Fatal(err)
					}
					if action != "claim" {
						job, err = queue.Claim(ctx, []string{kind}, "live-owner", 10*time.Second)
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
						_, err = queue.Claim(ctx, []string{kind}, "live-owner", tc.duration)
					case "fail":
						err = queue.Fail(ctx, job.ID, "live-owner", job.LeaseToken, "synthetic failure", tc.duration)
					case "renew":
						err = queue.RenewLease(ctx, job.ID, "live-owner", job.LeaseToken, tc.duration)
					}
					if !errors.Is(err, jobs.ErrInvalidDuration) {
						t.Errorf("invalid duration accepted or wrong error: %v", err)
					}
					if snapshot() != before {
						t.Error("invalid duration mutated job")
					}
				})
			}
		})
	}
}
