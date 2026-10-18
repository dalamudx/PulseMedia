package app

import (
	"bytes"
	"context"
	"errors"
	qb "github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"os"
	"testing"
)

func TestAppRunFileInventoryExactCompletion(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		name := "standard"
		if mapped {
			name = "mapped"
		}
		t.Run(name, func(t *testing.T) {
			runReviewedEpisodeFullPipeline(t, mapped, episodePipelineProbe{incompleteInventory: true})
		})
	}
}
func verifyIncompleteInventory(t *testing.T, ctx context.Context, f *organizationFixture, op uuid.UUID, source string, payload []byte) *download.Manager {
	t.Helper()
	manager := download.NewManager(downloadstore.NewRepository(f.pool, f.gen, outboxTestKey))
	manager.SetClientFactory(func(_, base, user, password string) (ports.DownloadClient, error) {
		return qb.NewClient(base, user, password, nil)
	})
	if _, e := manager.ObserveTask(ctx, op); !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("incomplete inventory accepted", e)
	}
	var state string
	var manifests, facts, organizations, active int
	if e := f.pool.QueryRow(ctx, `SELECT t.state,(SELECT count(*) FROM download_file_inventory),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL) FROM download_task t WHERE t.operation_id=$1`, op).Scan(&state, &manifests, &facts, &organizations, &active); e != nil || state != "tracking" || manifests != 0 || facts != 0 || organizations != 0 || active != 1 {
		t.Fatal("incomplete inventory changed facts or protection", state, manifests, facts, organizations, active, e)
	}
	actual, e := os.ReadFile(source)
	if e != nil || !bytes.Equal(actual, payload) {
		t.Fatal("incomplete inventory changed file", e)
	}
	return manager
}
