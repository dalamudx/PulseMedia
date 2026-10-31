package transmission

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

var _ ports.NativeDownloadControl = (*Client)(nil)

func controlHash(hash string) bool {
	_, e := hex.DecodeString(hash)
	return len(hash) == 40 && e == nil
}
func (c *Client) SetTorrentPaused(ctx context.Context, hash string, paused bool) error {
	if !controlHash(hash) {
		return ports.ErrDownloadControlInvalid
	}
	method := "torrent-start"
	if paused {
		method = "torrent-stop"
	}
	return c.controlRPC(ctx, method, strings.ToLower(hash))
}

// Only a received session challenge may repeat a native control request.
func (c *Client) controlRPC(ctx context.Context, method, hash string) error {
	body, _ := json.Marshal(rpcRequest{Method: method, Arguments: map[string]any{"ids": []string{hash}}, Tag: 1})
	for attempt := 0; attempt < 2; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
		if e != nil {
			return ports.ErrBadResponse
		}
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/json")
		if c.username != "" || c.password != "" {
			req.SetBasicAuth(c.username, c.password)
		}
		c.mu.Lock()
		sid := c.sessionID
		c.mu.Unlock()
		if sid != "" {
			req.Header.Set("X-Transmission-Session-Id", sid)
		}
		safe := *c.httpClient
		safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, e := safe.Do(req)
		if e != nil {
			return automaticTVReadError(ctx, e)
		}
		if resp.StatusCode == http.StatusConflict {
			sid = resp.Header.Get("X-Transmission-Session-Id")
			_ = resp.Body.Close()
			if !automaticTVText(sid) || strings.TrimSpace(sid) != sid {
				return ports.ErrAuthRequired
			}
			c.mu.Lock()
			c.sessionID = sid
			c.mu.Unlock()
			continue
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
		_ = resp.Body.Close()
		if e != nil {
			return automaticTVReadError(ctx, e)
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return ports.ErrAuthRequired
		}
		if resp.StatusCode == 429 {
			return ports.NewRateLimitError(0, "download control rate limited")
		}
		if resp.StatusCode != 200 || resp.Header.Get("Content-Range") != "" || len(raw) > 4096 || !utf8.Valid(raw) || automaticTVJSON(raw) != nil {
			return ports.ErrBadResponse
		}
		var ack struct {
			Result *string `json:"result"`
			Tag    *int64  `json:"tag"`
		}
		if json.Unmarshal(raw, &ack) != nil || ack.Result == nil || *ack.Result != "success" || (ack.Tag == nil || *ack.Tag != 1) {
			return ports.ErrBadResponse
		}
		return nil
	}
	return ports.ErrAuthRequired
}

func (c *Client) ReadTorrentPaused(ctx context.Context, hash string) (*ports.DownloadPauseObservation, error) {
	if !controlHash(hash) {
		return nil, ports.ErrDownloadControlInvalid
	}
	var response struct {
		Torrents []struct {
			Hash      *string   `json:"hashString"`
			Directory *string   `json:"downloadDir"`
			Status    *int      `json:"status"`
			Labels    *[]string `json:"labels"`
		} `json:"torrents"`
	}
	raw, e := c.automaticTVReadRPC(ctx, "torrent-get", map[string]any{"ids": []string{strings.ToLower(hash)}, "fields": []string{"hashString", "downloadDir", "status", "labels"}})
	if e != nil {
		return nil, e
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, ports.ErrBadResponse
	}
	if response.Torrents == nil {
		return nil, ports.ErrBadResponse
	}
	if len(response.Torrents) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(response.Torrents) != 1 {
		return nil, ports.ErrBadResponse
	}
	t := response.Torrents[0]
	if t.Hash == nil || !strings.EqualFold(*t.Hash, hash) || t.Directory == nil || !automaticTVPath(*t.Directory, true) || t.Status == nil || *t.Status < 0 || *t.Status > 6 || t.Labels == nil {
		return nil, ports.ErrBadResponse
	}
	for _, label := range *t.Labels {
		if !automaticTVText(label) {
			return nil, ports.ErrBadResponse
		}
	}
	return &ports.DownloadPauseObservation{Info: &ports.TorrentInfo{InfoHash: strings.ToLower(*t.Hash), SavePath: *t.Directory, Tags: strings.Join(*t.Labels, ",")}, Paused: *t.Status == 0}, nil
}
