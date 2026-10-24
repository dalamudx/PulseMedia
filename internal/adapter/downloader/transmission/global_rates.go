package transmission

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadGlobalRatesReader = (*Client)(nil)

func (c *Client) ReadGlobalRates(ctx context.Context) (ports.DownloadGlobalRates, error) {
	var out ports.DownloadGlobalRates
	var raw json.RawMessage
	if err := c.doRPC(ctx, "session-stats", struct{}{}, &raw); err != nil {
		return out, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, ports.ErrBadResponse
	}
	out.DownloadBytesPerSecond = nativeDownloadRate(fields["downloadSpeed"])
	out.UploadBytesPerSecond = nativeDownloadRate(fields["uploadSpeed"])
	return out, nil
}
