package httpapi_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

type completeTVLegacyObserver struct {
	ports.DownloadClient
	calls atomic.Int32
	info  *ports.TorrentInfo
}

func (p *completeTVLegacyObserver) GetTorrent(context.Context, string) (*ports.TorrentInfo, error) {
	p.calls.Add(1)
	return p.info, nil
}
func (p *completeTVLegacyObserver) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	p.calls.Add(1)
	return []*ports.TorrentInfo{p.info}, nil
}
func (p *completeTVLegacyObserver) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	p.calls.Add(1)
	return nil, ports.ErrUnavailable
}

func TestCompleteTVObservationLegacyIsolation(t *testing.T) {
	for _, state := range []string{"unknown", "submitting", "tracking"} {
		for _, method := range []string{"reconcile", "observe"} {
			t.Run(state+"/"+method, func(t *testing.T) {
				f := newObservationFixture(t, state)
				task, e := db.New(f.f.a.DBPool()).GetDownloadTaskByOpID(context.Background(), f.c.OperationID)
				if e != nil {
					t.Fatal(e)
				}
				client := &completeTVLegacyObserver{info: &ports.TorrentInfo{InfoHash: task.InfoHash, SavePath: task.SavePath, TotalBytes: 17, Tags: "unrelated-operation", Progress: 0.1}}
				m := download.NewManager(f.f.a.DBPool(), idgen.New(), nil)
				m.RegisterClient(task.ClientID, client)
				before := f.snapshot(t)
				var err error
				if method == "reconcile" {
					_, err = m.ReconcileTask(context.Background(), f.c.OperationID)
				} else {
					_, err = m.ObserveTask(context.Background(), f.c.OperationID)
				}
				if !errors.Is(err, ports.ErrCompleteTVNeedsObservation) || client.calls.Load() != 0 || f.snapshot(t) != before {
					t.Fatalf("legacy bypass: err=%v calls=%d unchanged=%v", err, client.calls.Load(), f.snapshot(t) == before)
				}
			})
		}
	}
}
