package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestPublicUpgradeReplaceIfBetterPG(t *testing.T) {
	for _, cutoff := range []int{300, 0} {
		t.Run(map[bool]string{true: "after_cutoff", false: "without_cutoff"}[cutoff > 0], func(t *testing.T) {
			f := newPublicUpgradeFixtureWithCutoff(t, cutoff)
			if cutoff > 0 {
				f.enable(t)
			} else if w := f.command("upgrade-modes", f.modeBody(t), "no-cutoff-stop", f.admin, nil); w.Code != 409 {
				t.Fatal("cutoff mode admitted without threshold", w.Code, w.Body.String())
			}
			prior := upgradeResult(t, f.call(f.body(), f.admin, nil))
			if cutoff > 0 && (prior.Improvement || !prior.Comparable || prior.CurrentScore == nil || *prior.CurrentScore < cutoff || !strings.Contains(prior.Reason, "already_reached_cutoff")) {
				t.Fatal("cutoff mode did not stop", prior)
			}
			stale, _ := json.Marshal(map[string]any{"current_file_id": f.request.CurrentFileID, "candidate_id": f.request.CandidateID, "expected": prior.Version, "expected_client_digest": prior.ExpectedClientDigest, "confirmed": true})
			body := f.modeBody(t, ports.UpgradeReplaceIfBetter)
			enabled := f.command("upgrade-modes", body, "replacement-mode", f.admin, nil)
			if enabled.Code != 202 {
				t.Fatal(enabled.Code, enabled.Body.String())
			}
			f.project(t)
			if w := f.command("upgrade-reservations", string(stale), "stale-mode-reservation", f.admin, nil); w.Code != 409 {
				t.Fatal("old mode observation authorized reservation", w.Code, w.Body.String())
			}
			fresh := f.reservationBody(t)
			reserved := f.command("upgrade-reservations", fresh, "replacement-reservation", f.admin, nil)
			if reserved.Code != 202 {
				t.Fatal(reserved.Code, reserved.Body.String())
			}
			var receipt ports.UpgradeReservationResult
			if e := json.Unmarshal(reserved.Body.Bytes(), &receipt); e != nil {
				t.Fatal(e)
			}
			var mode string
			var physical int
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT snapshot->>'mode',(SELECT count(*) FROM download_task WHERE operation_id=$1) FROM movie_upgrade_authority WHERE operation_id=$1`, receipt.OperationID).Scan(&mode, &physical); e != nil || mode != string(ports.UpgradeReplaceIfBetter) || physical != 0 || f.calls.Load() != 0 {
				t.Fatal("mode not frozen or reservation performed I/O", mode, physical, e)
			}
			assertUpgradeProjectionProtected(t, f)
			disabled := f.command("upgrade-modes", f.modeBody(t, ports.UpgradeDisabled), "disable-replacement", f.admin, nil)
			if disabled.Code != 202 {
				t.Fatal(disabled.Code, disabled.Body.String())
			}
			if replay := f.command("upgrade-modes", body, "replacement-mode", f.admin, nil); replay.Code != 202 || replay.Body.String() != enabled.Body.String() {
				t.Fatal("original mode receipt lost", replay.Code)
			}
			if replay := f.command("upgrade-reservations", fresh, "replacement-reservation", f.admin, nil); replay.Code != 202 || replay.Body.String() != reserved.Body.String() {
				t.Fatal("original reservation receipt lost", replay.Code)
			}
			if w := f.command("upgrade-reservations", fresh, "disabled-new-reservation", f.admin, nil); w.Code != 409 {
				t.Fatal("disabled mode authorized new reservation", w.Code, w.Body.String())
			}
		})
	}
}

func assertUpgradeProjectionProtected(t *testing.T, f *publicUpgradeFixture) {
	t.Helper()
	var wanted uuid.UUID
	if e := f.a.DBPool().QueryRow(context.Background(), `SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2`, f.request.SubscriptionID, f.unit).Scan(&wanted); e != nil {
		t.Fatal(e)
	}
	if deferred, e := f.a.Queries().IsMovieUpgradeSendProjectionDeferred(context.Background(), wanted); e != nil || !deferred {
		t.Fatal("projection write is not deferred", deferred, e)
	}
	pending, e := f.a.Queries().ListPendingEvaluationWantedItems(context.Background(), 128)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range pending {
		if w.SubscriptionID == f.request.SubscriptionID && w.UnitID == f.unit {
			t.Fatal("evaluator selected frozen upgrade for recomputation")
		}
	}
}

func TestPublicUpgradeReplaceModePreparedProjectionProtectedPG(t *testing.T) {
	f := newPublicPreparationFixtureWithMode(t, ports.UpgradeReplaceIfBetter, 300)
	assertUpgradeProjectionProtected(t, f.publicUpgradeFixture)
	f.prepared(t, "replace-prepared")
	assertUpgradeProjectionProtected(t, f.publicUpgradeFixture)
}

func TestPublicUpgradeReplaceModeInvalidatesPreparationPG(t *testing.T) {
	f := newPublicPreparationFixtureWithMode(t, ports.UpgradeReplaceIfBetter, 300)
	body := f.modeBody(t, ports.UpgradeStopAtCutoff)
	if w := f.command("upgrade-modes", body, "stop-replacement", f.admin, nil); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.project(t)
	if w := f.command("upgrade-preparations", f.body(), "changed-mode-preparation", f.admin, nil); w.Code != 409 {
		t.Fatal("changed mode continued frozen preparation", w.Code, w.Body.String())
	}
	var tasks, lanes, reservations int
	if e := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM download_task WHERE operation_id=$1),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched'))`, f.operation).Scan(&tasks, &lanes, &reservations); e != nil || tasks != 0 || lanes != 1 || reservations != 1 || f.fetches.Load() != 0 {
		t.Fatal("stale preparation performed I/O or lost protection", tasks, lanes, reservations, f.fetches.Load(), e)
	}
}

func TestPublicUpgradeReplaceModeHistoryPreventsDowngradePG(t *testing.T) {
	f := newPublicUpgradeFixtureWithCutoff(t, 300)
	for i, mode := range []ports.UpgradeMode{ports.UpgradeReplaceIfBetter, ports.UpgradeDisabled} {
		if w := f.command("upgrade-modes", f.modeBody(t, mode), []string{"replacement-history", "disabled-history"}[i], f.admin, nil); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var mode string
	var authorities int
	if e := f.a.DBPool().QueryRow(context.Background(), `SELECT upgrade_mode,(SELECT count(*) FROM movie_upgrade_authority) FROM subscription WHERE id=$1`, f.request.SubscriptionID).Scan(&mode, &authorities); e != nil || mode != "disabled" || authorities != 0 {
		t.Fatal(mode, authorities, e)
	}
	ctx := context.Background()
	for version := postgres.CurrentMigrationVersion; version > 80; version-- {
		if got, err := postgres.Rollback(ctx, f.a.Config().Database.URL, "../../migrations"); err != nil || got != int64(version-1) {
			t.Fatalf("rollback unrelated migration %d: version=%d error=%v", version, got, err)
		}
	}
	if _, e := postgres.Rollback(ctx, f.a.Config().Database.URL, "../../migrations"); e == nil || !strings.Contains(e.Error(), "replacement mode history prevents downgrade") {
		t.Fatal("disabled subscription erased replacement history", e)
	}
	if err := postgres.CheckSchemaVersion(ctx, f.a.DBPool(), 80); err != nil {
		t.Fatal("failed downgrade changed schema version", err)
	}
}
