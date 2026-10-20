package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestCompleteTVTrackingNativeRatesPG(t *testing.T) {
	for _, state := range []string{"tracking", "completed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			n := newTrackingFixture(t)
			n.store = postgres.NewCompleteTVTrackingStoreWithDownloadRates(n.f.a.DBPool(), idgen.New())
			original := n.info
			switch state {
			case "tracking":
				n.info = func(i int32) string { return strings.Replace(original(i), `"progress":1`, `"progress":0.5`, 1) }
			case "unknown":
				n.info = func(int32) string { return `[]` }
			}
			receipt, err := n.observe(context.Background())
			if err != nil || receipt == nil || receipt.State != state {
				t.Fatal("native tracking with production telemetry", receipt, err)
			}
			n.assert(t, state)
			var samples int
			if err := n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM download_rate_observation WHERE task_id=$1 AND source='complete_tv'`, n.request.TaskID).Scan(&samples); err != nil || samples != 1 {
				t.Fatal("atomic native telemetry sample", samples, err)
			}
		})
	}
}
