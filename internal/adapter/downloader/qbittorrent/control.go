package qbittorrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var _ ports.NativeDownloadControl = (*Client)(nil)

func (c *Client) ReadTorrentPaused(ctx context.Context, hash string) (*ports.DownloadPauseObservation, error) {
	info, e := c.GetTorrent(ctx, hash)
	if e != nil {
		return nil, e
	}
	paused := false
	switch info.State {
	case "pausedDL", "pausedUP", "stoppedDL", "stoppedUP":
		paused = true
	case "downloading", "uploading", "stalledDL", "stalledUP", "queuedDL", "queuedUP", "forcedDL", "forcedUP", "metaDL", "forcedMetaDL":
	default:
		return nil, ports.ErrBadResponse
	}
	return &ports.DownloadPauseObservation{Info: info, Paused: paused}, nil
}
func (c *Client) SetTorrentPaused(ctx context.Context, hash string, paused bool) error {
	if !validTorrentHash(hash) {
		return ports.ErrDownloadControlInvalid
	}
	if e := c.ensureLogin(ctx); e != nil {
		return e
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/app/version", nil)
	if e != nil {
		return ports.ErrBadResponse
	}
	response, e := c.httpClient.Do(request)
	if e != nil {
		return httpFailure(ctx, e)
	}
	raw, e := readHTTPBody(ctx, response.Body, 64)
	_ = response.Body.Close()
	if e != nil {
		return e
	}
	if response.StatusCode != http.StatusOK {
		return c.apiHTTPFailure(response)
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(raw)), "v"), ".")
	if len(parts) != 3 {
		return ports.ErrDownloadControlUnsupported
	}
	for _, part := range parts {
		if _, e = strconv.ParseUint(part, 10, 16); e != nil {
			return ports.ErrDownloadControlUnsupported
		}
	}
	action := ""
	switch parts[0] {
	case "4":
		if paused {
			action = "pause"
		} else {
			action = "resume"
		}
	case "5":
		if paused {
			action = "stop"
		} else {
			action = "start"
		}
	default:
		return ports.ErrDownloadControlUnsupported
	}
	body := url.Values{"hashes": {strings.ToLower(hash)}}.Encode()
	request, e = http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/"+action, strings.NewReader(body))
	if e != nil {
		return ports.ErrBadResponse
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, e = c.httpClient.Do(request)
	if e != nil {
		return httpFailure(ctx, e)
	}
	defer func() { _ = response.Body.Close() }()
	if e = c.apiHTTPFailure(response); e != nil {
		return e
	}
	ack, e := readHTTPBody(ctx, response.Body, 4096)
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(ack)) != "" {
		return ports.ErrBadResponse
	}
	return nil
}
