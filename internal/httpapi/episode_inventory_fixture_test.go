package httpapi_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type inventoryObservationClient struct {
	info            ports.TorrentInfo
	files           []ports.TorrentFile
	changedOnSecond bool
	reads           int
}

func (c *inventoryObservationClient) GetTorrent(context.Context, string) (*ports.TorrentInfo, error) {
	c.reads++
	out := c.info
	if c.changedOnSecond && c.reads%2 == 0 {
		out.TotalBytes++
	}
	return &out, nil
}
func (c *inventoryObservationClient) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	return append([]ports.TorrentFile(nil), c.files...), nil
}
func (*inventoryObservationClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	return nil
}
func (*inventoryObservationClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	return nil, nil
}
func (*inventoryObservationClient) RemoveTorrent(context.Context, string, bool) error { return nil }
func (*inventoryObservationClient) Test(context.Context) error                        { return nil }
