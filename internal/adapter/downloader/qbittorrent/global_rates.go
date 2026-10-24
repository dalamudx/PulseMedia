package qbittorrent

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

var _ ports.DownloadGlobalRatesReader = (*Client)(nil)

func (c *Client) ReadGlobalRates(ctx context.Context) (ports.DownloadGlobalRates, error) {
	var out ports.DownloadGlobalRates
	if err := c.ensureLogin(ctx); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/transfer/info", nil)
	if err != nil {
		return out, ports.ErrBadResponse
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, httpFailure(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err = c.apiHTTPFailure(resp); err != nil {
		return out, err
	}
	body, err := readHTTPBody(ctx, resp.Body, 1<<20)
	if err != nil {
		return out, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return out, ports.ErrBadResponse
	}
	out.DownloadBytesPerSecond = nativeDownloadRate(fields["dl_info_speed"])
	out.UploadBytesPerSecond = nativeDownloadRate(fields["up_info_speed"])
	return out, nil
}
