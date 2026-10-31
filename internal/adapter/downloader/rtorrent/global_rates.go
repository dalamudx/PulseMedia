package rtorrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadGlobalRatesReader = (*Client)(nil)

func (c *Client) ReadGlobalRates(ctx context.Context) (ports.DownloadGlobalRates, error) {
	var out ports.DownloadGlobalRates
	down, err := c.callXMLRPC(ctx, "throttle.global_down.rate")
	if err != nil {
		return out, err
	}
	if down.kind != "int" || down.integer < 0 {
		return out, ports.ErrBadResponse
	}
	up, err := c.callXMLRPC(ctx, "throttle.global_up.rate")
	if err != nil {
		return out, err
	}
	if up.kind != "int" || up.integer < 0 {
		return out, ports.ErrBadResponse
	}
	out.DownloadBytesPerSecond, out.UploadBytesPerSecond = &down.integer, &up.integer
	return out, nil
}
