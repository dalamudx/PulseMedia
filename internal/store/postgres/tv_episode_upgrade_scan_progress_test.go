package postgres_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

func TestTVEpisodeUpgradeScanProgressKeepsAuthorityPG(t *testing.T) {
	f := newTVUpgradeFixture(t)
	before, err := f.service.Preview(t.Context(), f.actor, f.request)
	if err != nil || before == nil || !before.Improvement {
		t.Fatal("initial preview", err)
	}
	execIntent(t, f.pool, "UPDATE library_root SET scan_token = scan_token + 1, mutation_seq = mutation_seq + 1, updated_at = clock_timestamp() WHERE id=$1", f.tv)
	after, err := f.service.Preview(t.Context(), f.actor, f.request)
	if err != nil || after == nil || !after.Improvement || after.Version != before.Version {
		t.Fatal("scan progress invalidated unchanged facts", err)
	}
	reserved := f.command(t, "reserve", uuid.Nil, before.Version, "scan-progress-reserve")
	execIntent(t, f.pool, "UPDATE library_root SET scan_token = scan_token + 1, updated_at = clock_timestamp() WHERE id=$1", f.tv)
	prepared := f.command(t, "prepare", reserved.OperationID, reserved.Version, "scan-progress-prepare")
	execIntent(t, f.pool, "UPDATE library_root SET health='offline',health_revision=health_revision+1,updated_at=clock_timestamp() WHERE id=$1", f.tv)
	_, err = f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: prepared.OperationID, ExpectedVersion: prepared.Version, Confirmed: true}, "changed-root-health-send")
	if err == nil || f.native.calls.Load() != 0 {
		t.Fatal("real root health change authorized sending", err)
	}
}
