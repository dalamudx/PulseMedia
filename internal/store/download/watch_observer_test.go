package download

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestDownloadWatchObserverStaleBeforeDispatchPG(t *testing.T) {
	f := newRatePublisherFixture(t)
	before, graph := f.taskJSON(t), f.protected(t)
	target := ports.DownloadWatchTarget{TaskID: f.task.ID, OperationID: f.task.OperationID, ClientID: f.task.ClientID}
	if _, err := f.repo.ObserveDownloadWatchTarget(t.Context(), target); !errors.Is(err, ports.ErrDownloadWatchStale) {
		t.Fatal("unbound target was not rejected")
	}
	if f.client.calls.Load() != 0 {
		t.Fatal("stale binding dispatched native I/O")
	}
	if before != f.taskJSON(t) || !reflect.DeepEqual(graph, f.protected(t)) {
		t.Fatal("stale binding changed business state")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM download_rate_observation").Scan(&count); err != nil || count != 0 {
		t.Fatal("stale binding published a rate")
	}
}

func TestDownloadWatchObserverCanceledBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m := new(Manager)
	if _, err := m.ObserveDownloadWatchTarget(ctx, ports.DownloadWatchTarget{OperationID: uuid.New()}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled observation did not stop before database/client access")
	}
}

func TestDownloadWatchReadClientDeniesMutation(t *testing.T) {
	c := new(watchReadClient)
	if err := c.AddTorrent(t.Context(), ports.AddTorrentParam{}); !errors.Is(err, ports.ErrDownloadWatchStale) {
		t.Fatal("Add permitted")
	}
	if err := c.RemoveTorrent(t.Context(), "hash", true); !errors.Is(err, ports.ErrDownloadWatchStale) {
		t.Fatal("Remove permitted")
	}
	if _, err := c.ListTorrents(t.Context(), "tag"); !errors.Is(err, ports.ErrDownloadWatchStale) {
		t.Fatal("List permitted")
	}
}
