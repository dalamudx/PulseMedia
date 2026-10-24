package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	rpcURL     string
	username   string
	password   string
	sessionID  string
	mu         sync.Mutex
}

func NewClient(baseURL, username, password string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	baseURL = strings.TrimRight(baseURL, "/")
	rpcURL := baseURL
	if !strings.HasSuffix(rpcURL, "/transmission/rpc") {
		rpcURL += "/transmission/rpc"
	}

	return &Client{
		httpClient: httpClient,
		rpcURL:     rpcURL,
		username:   username,
		password:   password,
	}, nil
}

type rpcRequest struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
	Tag       int    `json:"tag,omitempty"`
}

type rpcResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Tag       int             `json:"tag,omitempty"`
}

func (c *Client) doRPC(ctx context.Context, method string, args any, targetArgs any) error {
	reqBody, err := json.Marshal(rpcRequest{
		Method:    method,
		Arguments: args,
		Tag:       1,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal rpc request: %w", err)
	}

	// Try with existing sessionID; if 409 Conflict, capture new sessionID and retry once
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(reqBody))
		if err != nil {
			return err
		}

		// A stream reset may happen after the daemon accepted a mutation.
		// Only explicit 409 session negotiation below may construct another RPC.
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

		safeClient := *c.httpClient
		safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := safeClient.Do(req)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ports.ErrTimeout
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ports.ErrUnavailable
		}

		// Handle 409 Conflict: extract new X-Transmission-Session-Id header
		if resp.StatusCode == http.StatusConflict {
			newSID := resp.Header.Get("X-Transmission-Session-Id")
			_ = resp.Body.Close()
			if newSID != "" {
				c.mu.Lock()
				c.sessionID = newSID
				c.mu.Unlock()
				continue // retry request with new sessionID
			}
			return fmt.Errorf("%w: transmission 409 without session id", ports.ErrAuthRequired)
		}

		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return ports.ErrAuthRequired
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			var retry time.Duration
			if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 0 && seconds <= 86400 {
				retry = time.Duration(seconds) * time.Second
			}
			return ports.NewRateLimitError(retry, "download RPC rate limited")
		}
		if resp.StatusCode >= 500 {
			return ports.ErrUnavailable
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
		}

		var rpcResp rpcResponse
		body, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if e != nil {
			return ports.ErrUnavailable
		}
		if len(body) > 1<<20 || json.Unmarshal(body, &rpcResp) != nil {
			return ports.ErrBadResponse
		}

		if rpcResp.Result != "success" {
			return ports.ErrBadResponse
		}

		if targetArgs != nil && len(rpcResp.Arguments) > 0 {
			if err := json.Unmarshal(rpcResp.Arguments, targetArgs); err != nil {
				return ports.ErrBadResponse
			}
		}

		return nil
	}

	return errors.New("transmission rpc failed after session negotiation")
}

func (c *Client) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if err := ports.ValidateDownloadPayload(p); err != nil {
		return err
	}
	args := map[string]any{
		"paused": p.Paused,
	}
	if p.SavePath != "" {
		args["download-dir"] = p.SavePath
	}

	if p.Tags != "" {
		labels := make([]string, 0)
		for _, label := range strings.Split(p.Tags, ",") {
			if label = strings.TrimSpace(label); label != "" {
				labels = append(labels, label)
			}
		}
		args["labels"] = labels
	}
	if len(p.TorrentBytes) > 0 {
		args["metainfo"] = base64.StdEncoding.EncodeToString(p.TorrentBytes)
	} else if p.DownloadURL != "" {
		args["filename"] = p.DownloadURL
	} else {
		return errors.New("either TorrentBytes or DownloadURL must be provided")
	}

	var res struct {
		TorrentAdded struct {
			HashString string `json:"hashString"`
			Name       string `json:"name"`
		} `json:"torrent-added"`
	}

	return c.doRPC(ctx, "torrent-add", args, &res)
}

func (c *Client) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	infoHash = strings.ToLower(infoHash)
	args := map[string]any{"fields": []string{"hashString", "name", "downloadDir", "percentDone", "totalSize", "status", "haveValid", "labels", "rateDownload", "files", "fileStats"}, "ids": []string{infoHash}}
	var res struct {
		Torrents []torrentObservation `json:"torrents"`
	}
	if err := c.doRPC(ctx, "torrent-get", args, &res); err != nil {
		return nil, err
	}
	if res.Torrents == nil {
		return nil, ports.ErrBadResponse
	}
	if len(res.Torrents) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(res.Torrents) != 1 || !strings.EqualFold(res.Torrents[0].HashString, infoHash) {
		return nil, ports.ErrBadResponse
	}
	return res.Torrents[0].info()
}

func (c *Client) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	args := map[string]any{"fields": []string{"hashString", "name", "downloadDir", "percentDone", "totalSize", "status", "haveValid", "labels", "rateDownload"}}
	var res struct {
		Torrents []torrentObservation `json:"torrents"`
	}
	if err := c.doRPC(ctx, "torrent-get", args, &res); err != nil {
		return nil, err
	}
	if res.Torrents == nil {
		return nil, ports.ErrBadResponse
	}
	out := make([]*ports.TorrentInfo, 0, len(res.Torrents))
	for _, torrent := range res.Torrents {
		if tag != "" && !torrent.hasLabel(tag) {
			continue
		}
		info, err := torrent.info()
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	args := map[string]any{
		"ids":               []string{strings.ToLower(infoHash)},
		"delete-local-data": deleteFiles,
	}
	return c.doRPC(ctx, "torrent-remove", args, nil)
}

func (c *Client) Test(ctx context.Context) error {
	args := map[string]any{"fields": []string{"version"}}
	return c.doRPC(ctx, "session-get", args, nil)
}
