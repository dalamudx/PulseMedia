package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// AutomaticTVByteSender exposes byte submission and protocol acknowledgement only;
// the caller must obtain automatic TV business authority before sending.
func (c *Client) AutomaticTVByteSender() ports.DownloadClient { return &automaticTVByteClient{c} }

type automaticTVByteClient struct{ *Client }

var _ ports.DownloadClient = (*automaticTVByteClient)(nil)

func (c *automaticTVByteClient) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	labels := strings.Split(p.Tags, ",")
	if p.DownloadURL != "" || len(p.TorrentBytes) == 0 || len(p.TorrentBytes) > ports.MaxTorrentMetainfoBytes || p.Paused ||
		!automaticTVHash(p.InfoHash) || p.InfoHash != strings.ToLower(p.InfoHash) || !automaticTVPath(p.SavePath, true) ||
		len(labels) != 2 || labels[0] != "pulsemedia" {
		return ports.ErrBadResponse
	}
	const prefix = "pulsemedia:op:"
	id, err := uuid.Parse(strings.TrimPrefix(labels[1], prefix))
	if err != nil || id == uuid.Nil || labels[1] != prefix+id.String() {
		return ports.ErrBadResponse
	}
	if c.Client == nil || c.httpClient == nil {
		return ports.ErrUnavailable
	}
	// Preparation seals the source hash; preserve its original bytes without re-encoding metainfo.
	body, err := json.Marshal(rpcRequest{Method: "torrent-add", Tag: 1, Arguments: map[string]any{
		"metainfo": base64.StdEncoding.EncodeToString(p.TorrentBytes), "download-dir": p.SavePath,
		"labels": labels, "paused": false,
	}})
	if err != nil {
		return ports.ErrBadResponse
	}
	return c.automaticTVAdd(ctx, body, p.InfoHash)
}

func (c *Client) automaticTVAdd(ctx context.Context, body []byte, hash string) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
		if err != nil {
			return automaticTVReadError(ctx, err)
		}
		// HTTP/2 may otherwise replay a received POST after a stream reset.
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
		resp, err := safe.Do(req)
		if err != nil {
			return automaticTVReadError(ctx, err)
		}
		if resp.StatusCode == http.StatusConflict {
			sid = resp.Header.Get("X-Transmission-Session-Id")
			_ = resp.Body.Close()
			if err := ctx.Err(); err != nil {
				return err
			}
			if !automaticTVText(sid) || strings.TrimSpace(sid) != sid {
				return ports.ErrAuthRequired
			}
			c.mu.Lock()
			c.sessionID = sid
			c.mu.Unlock()
			// A received 409 rejects the RPC before execution; this is the sole retry.
			continue
		}
		err = automaticTVAddResponse(ctx, resp, hash)
		_ = resp.Body.Close()
		return err
	}
	return ports.ErrUnavailable
}

func automaticTVAddResponse(ctx context.Context, resp *http.Response, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ports.ErrAuthRequired
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return ports.NewRateLimitError(0, "download RPC rate limited")
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" {
		return ports.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || ctx.Err() != nil {
		return automaticTVReadError(ctx, err)
	}
	if len(data) > 1<<20 || !utf8.Valid(data) || automaticTVJSON(data) != nil {
		return ports.ErrUnavailable
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(data, &response) != nil || len(response) < 2 || len(response) > 3 {
		return ports.ErrUnavailable
	}
	for key := range response {
		if key != "result" && key != "arguments" && key != "tag" {
			return ports.ErrUnavailable
		}
	}
	var result string
	if json.Unmarshal(response["result"], &result) != nil || result != "success" {
		return ports.ErrUnavailable
	}
	if raw, ok := response["tag"]; ok {
		var tag *int64
		if json.Unmarshal(raw, &tag) != nil || tag == nil || *tag != 1 {
			return ports.ErrUnavailable
		}
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(response["arguments"], &args) != nil || len(args) != 1 {
		return ports.ErrUnavailable
	}
	// Duplicate ACKs cannot confirm this operation's labels and download directory.
	var added map[string]json.RawMessage
	if json.Unmarshal(args["torrent-added"], &added) != nil || len(added) < 2 || len(added) > 3 {
		return ports.ErrUnavailable
	}
	for key := range added {
		if key != "hashString" && key != "name" && key != "id" {
			return ports.ErrUnavailable
		}
	}
	var actual, name *string
	if json.Unmarshal(added["hashString"], &actual) != nil || actual == nil || *actual != hash ||
		json.Unmarshal(added["name"], &name) != nil || name == nil || !automaticTVText(*name) {
		return ports.ErrUnavailable
	}
	if raw, ok := added["id"]; ok {
		var id *int64
		if json.Unmarshal(raw, &id) != nil || id == nil || *id < 0 {
			return ports.ErrUnavailable
		}
	}
	return nil
}
