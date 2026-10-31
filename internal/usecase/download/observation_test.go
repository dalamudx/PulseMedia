package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
)

type observationPreparer struct {
	p *ports.PreparedDownloadObservation
}

func (r observationPreparer) PrepareDownloadObservation(context.Context, uuid.UUID, bool, *ports.DownloadWatchTarget) (*ports.PreparedDownloadObservation, error) {
	return r.p, nil
}

type observationClient struct {
	ports.DownloadClient
	get   func(context.Context, string) (*ports.TorrentInfo, error)
	list  func(context.Context, string) ([]*ports.TorrentInfo, error)
	files func(context.Context, string) ([]ports.TorrentFile, error)
}

func (c observationClient) GetTorrent(ctx context.Context, hash string) (*ports.TorrentInfo, error) {
	return c.get(ctx, hash)
}
func (c observationClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	return c.list(ctx, tag)
}
func (c observationClient) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	return c.files(ctx, hash)
}
func TestObservationUsecaseRetainsBudgetThroughInventoryAndWriteback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	task := &ports.DownloadExecutionTask{ID: uuid.New(), InfoHash: "original"}
	info := &ports.TorrentInfo{InfoHash: "original"}
	reads := 0
	released, completed := false, false
	p := &ports.PreparedDownloadObservation{Task: task}
	p.Client = observationClient{get: func(context.Context, string) (*ports.TorrentInfo, error) {
		if released {
			t.Error("released before read")
		}
		reads++
		if reads == 2 {
			cancel()
		}
		return info, nil
	}, files: func(context.Context, string) ([]ports.TorrentFile, error) {
		if released {
			t.Error("released before files")
		}
		return []ports.TorrentFile{{Name: "movie.mkv"}}, nil
	}}
	p.InventoryRequired = func(context.Context, *ports.TorrentInfo) (bool, error) { return true, nil }
	p.Complete = func(c context.Context, r ports.DownloadObservationResult) (*ports.DownloadExecutionTask, error) {
		if released || reads != 2 || r.Second != info || len(r.Files) != 1 || !errors.Is(c.Err(), context.Canceled) {
			t.Error("completion lost physical call result")
		}
		completed = true
		return task, context.Canceled
	}
	p.Release = func() {
		if !completed {
			t.Error("released before writeback")
		}
		released = true
	}
	got, e := ObserveTask(ctx, observationPreparer{p}, uuid.New())
	if got != task || !errors.Is(e, context.Canceled) || !released {
		t.Fatal(got, e)
	}
}
func TestObservationUsecaseRequiresUniqueExactOperationTag(t *testing.T) {
	op := uuid.New()
	tag := "pulsemedia:op:" + op.String()
	for _, tags := range [][]string{{tag}, {tag, tag}, {tag + "-other"}, {"other"}} {
		p := &ports.PreparedDownloadObservation{Task: &ports.DownloadExecutionTask{ID: uuid.New()}}
		p.Client = observationClient{list: func(context.Context, string) ([]*ports.TorrentInfo, error) {
			items := []*ports.TorrentInfo{}
			for _, label := range tags {
				items = append(items, &ports.TorrentInfo{Tags: label})
			}
			return items, nil
		}}
		p.Release = func() {}
		p.Complete = func(_ context.Context, r ports.DownloadObservationResult) (*ports.DownloadExecutionTask, error) {
			if (r.Info != nil) != (len(tags) == 1 && tags[0] == tag) {
				t.Error("tag identity guessed", tags)
			}
			return p.Task, nil
		}
		if _, e := ReconcileTask(t.Context(), observationPreparer{p}, op); e != nil {
			t.Fatal(e)
		}
	}
}
