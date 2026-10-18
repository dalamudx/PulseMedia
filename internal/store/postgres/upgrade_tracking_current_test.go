package postgres_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestMovieUpgradeTrackingCurrentEvidenceDuringHTTPPG(t *testing.T) {
	for _, change := range []string{"account", "downloader"} {
		t.Run(change, func(t *testing.T) {
			f := nativeTrackingFixture(t, "Ok.")
			ctx := context.Background()
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			var once sync.Once
			hook := func() {
				once.Do(func() {
					var e error
					if change == "account" {
						_, e = f.p.Exec(ctx, `UPDATE app_user SET is_active=FALSE WHERE id=$1`, f.actor.ID)
					} else {
						_, e = f.p.Exec(ctx, `UPDATE download_client SET url=url||'/rotated' WHERE id=(SELECT client_id FROM movie_upgrade_authority WHERE operation_id=$1)`, f.operation)
					}
					if e != nil {
						t.Error(e)
					}
				})
			}
			f.onInfo.Store(&hook)
			if _, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "changed-admit", "changed-result"); e == nil {
				t.Fatal("current evidence change accepted returned result")
			}
			trackingCounts(t, f, 1, 1, 0)
			if nativeProtectedGraph(t, f.preparedUpgradeFixture) != before {
				t.Fatal("current change modified original task or protection")
			}
			calls := f.infos.Load()
			if _, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "changed-new-admit", "changed-new-result"); e == nil {
				t.Fatal("changed evidence reauthorized observation")
			}
			if f.infos.Load() != calls {
				t.Fatal("changed evidence issued another query")
			}
		})
	}
}

func TestMovieUpgradeTrackingReceiptAfterConfigChangePG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	out, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "historical-admit", "historical-result")
	if e != nil {
		t.Fatal(e)
	}
	calls := f.infos.Load()
	if _, e = f.p.Exec(ctx, `UPDATE download_client SET url=url||'/rotated' WHERE id=(SELECT client_id FROM movie_upgrade_authority WHERE operation_id=$1)`, f.operation); e != nil {
		t.Fatal(e)
	}
	replay, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "historical-admit", "historical-result")
	if e != nil || *replay != *out || f.infos.Load() != calls {
		t.Fatal("historical read required obsolete execution identity", replay, e)
	}
}
