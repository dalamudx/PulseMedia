package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestRestoreIsolationRetainsUpgradeWorkPG(t *testing.T) {
	for _, status := range []string{"unknown", "confirmed", "tracking-cooldown"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var n *nativeUpgradeFixture
			var addCount func() int32
			if status == "tracking-cooldown" {
				f := nativeTrackingJobFixture(t)
				f.complete.Store(false)
				if count, err := f.runtime.ScheduleMovieUpgradeTracking(ctx); err != nil || count != 1 {
					t.Fatal("initial tracking job", count, err)
				}
				request := f.claim(t, 30*time.Second)
				result, err := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, request)
				if err != nil || result == nil || result.Outcome != "tracking" {
					t.Fatal("tracking result", err)
				}
				f.counts(t, 1, 1, 1)
				if count, err := f.runtime.ScheduleMovieUpgradeTracking(ctx); err != nil || count != 0 {
					t.Fatal("expected successor still in cooldown", count, err)
				}
				n = f.nativeUpgradeFixture
				addCount = f.adds.Load
			} else {
				ack := "Ok."
				if status == "unknown" {
					ack = "ambiguous"
				}
				n = r3Native(t, ack, true)
				addCount = n.adds.Load
			}
			if version, err := postgres.Migrate(ctx, n.dsn, migrationsDir); err != nil || version != postgres.CurrentMigrationVersion {
				t.Fatal("current restore schema", version, err)
			}
			var runnable int
			if err := n.p.QueryRow(ctx, `SELECT count(*) FROM job WHERE state IN ('queued','running')`).Scan(&runnable); err != nil || runnable != 0 {
				t.Fatal("job must not mask background upgrade recovery", runnable, err)
			}
			old := nativeProtectedGraph(t, n.preparedUpgradeFixture)
			restore := uuid.New()
			if err := postgres.ActivateRestoreIsolation(ctx, n.p, restore); err != nil {
				t.Fatal(err)
			}
			err := postgres.ReconcileRestoreIsolation(ctx, n.p, postgres.RestoreReconciliation{RestoreID: restore, Operator: "restore test operator", DownloaderEvidence: "original downloader compared", FilesystemEvidence: "original files compared", OrphanEvidence: "external orphan inventory reviewed"})
			if err == nil {
				t.Fatal("active upgrade released on operator statements alone")
			}
			if err := postgres.RequireRestoreReconciled(ctx, n.p); !errors.Is(err, postgres.ErrRestoreIsolated) {
				t.Fatal("isolation lost", err)
			}
			if addCount() != 1 || nativeProtectedGraph(t, n.preparedUpgradeFixture) != old {
				t.Fatal("restore decision changed original upgrade protection or resent Add")
			}
			var audits int
			if err := n.p.QueryRow(ctx, `SELECT count(*) FROM restore_isolation_audit WHERE action='reconcile'`).Scan(&audits); err != nil || audits != 0 {
				t.Fatal("rejected release wrote success audit", audits, err)
			}
		})
	}
}
