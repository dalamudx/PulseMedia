package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

// Exercise the real confirmation boundary against a succeeded native tracking
// receipt. An oversized trusted receipt must remain review; it is never truncated.
func TestCompleteTVOrganizationPGInventoryBoundAndTrackingBinding(t *testing.T) {
	for _, kind := range []string{"over-1000", "wrong-operation", "wrong-kind", "wrong-state"} {
		t.Run(kind, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			pool := f.n.f.a.DBPool()
			ctx := context.Background()
			j, e := f.n.f.a.Queries().GetJobByID(ctx, f.command.TrackingJobID)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "over-1000":
				var r ports.CompleteTVTrackingReceipt
				if e = json.Unmarshal(j.Result, &r); e != nil {
					t.Fatal(e)
				}
				for len(r.Files) < 1001 {
					r.Files = append(r.Files, ports.TorrentFile{Name: fmt.Sprintf("Show/attachment-%04d.txt", len(r.Files)), SizeBytes: 0, Progress: 1})
				}
				raw, _ := json.Marshal(r)
				f.n.exec(t, `UPDATE job SET result=$2 WHERE id=$1`, j.ID, raw)
			case "wrong-operation":
				f.n.exec(t, `UPDATE job SET payload=jsonb_set(payload,'{operation_id}','"00000000-0000-0000-0000-000000000001"') WHERE id=$1`, j.ID)
			case "wrong-kind":
				f.n.exec(t, `UPDATE job SET kind='download.track' WHERE id=$1`, j.ID)
			case "wrong-state":
				f.n.exec(t, `UPDATE job SET state='queued' WHERE id=$1`, j.ID)
			}
			if _, e = f.confirm("inventory"); !errors.Is(e, ports.ErrTransferReviewRequired) {
				t.Fatal("untrusted or oversized tracking accepted", e)
			}
			var plans, inventory int
			e = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM transfer_plan),(SELECT count(*) FROM download_file_inventory)`).Scan(&plans, &inventory)
			if e != nil || plans != 0 || inventory != 0 {
				t.Fatal("partial/truncated plan", plans, inventory, e)
			}
		})
	}
}
