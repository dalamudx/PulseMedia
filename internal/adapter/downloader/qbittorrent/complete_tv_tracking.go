package qbittorrent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// This capability exposes only bounded native reads, never Add or cancellation.
func (c *Client) CompleteTVTrackingObserver() ports.CompleteTVTrackingClient {
	return &completeTVTrackingClient{&completeTVObservationClient{c}}
}

type completeTVTrackingClient struct{ *completeTVObservationClient }

var _ ports.CompleteTVTrackingClient = (*completeTVTrackingClient)(nil)

func (c *completeTVTrackingClient) GetTorrentMetainfo(ctx context.Context, hash string) ([]byte, error) {
	if len(hash) != 40 {
		return nil, ports.ErrBadResponse
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return nil, ports.ErrBadResponse
	}
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/torrents/export?"+url.Values{"hash": {strings.ToLower(hash)}}.Encode(), nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, httpFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if err = c.apiHTTPFailure(response); err != nil {
		return nil, err
	}
	return readHTTPBody(ctx, response.Body, ports.MaxTorrentMetainfoBytes)
}

func (c *completeTVTrackingClient) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	if len(hash) != 40 && len(hash) != 64 {
		return nil, ports.ErrBadResponse
	}
	if _, e := hex.DecodeString(hash); e != nil {
		return nil, ports.ErrBadResponse
	}
	if e := c.ensureLogin(ctx); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/torrents/files?"+url.Values{"hash": {strings.ToLower(hash)}}.Encode(), nil)
	if e != nil {
		return nil, ports.ErrBadResponse
	}
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, e := client.Do(req)
	if e != nil {
		return nil, httpFailure(ctx, e)
	}
	defer func() { _ = r.Body.Close() }()
	if e = c.apiHTTPFailure(r); e != nil {
		return nil, e
	}
	body, e := readHTTPBody(ctx, r.Body, 1<<20)
	if e != nil {
		return nil, e
	}

	var rawRows []json.RawMessage
	if json.Unmarshal(body, &rawRows) != nil || rawRows == nil {
		return nil, ports.ErrBadResponse
	}
	for _, raw := range rawRows {
		if !uniqueCompleteTVObservationObject(raw) {
			return nil, ports.ErrBadResponse
		}
	}
	var rows []struct {
		Index    *int32          `json:"index"`
		Name     *string         `json:"name"`
		Size     *int64          `json:"size"`
		Progress json.RawMessage `json:"progress"`
	}
	if json.Unmarshal(body, &rows) != nil || rows == nil || len(rows) > 1000 {
		return nil, ports.ErrBadResponse
	}
	ids := map[int32]bool{}
	names := map[string]bool{}
	out := make([]ports.TorrentFile, 0, len(rows))
	for _, f := range rows {
		if f.Index == nil || f.Name == nil || f.Size == nil || *f.Index < 0 || ids[*f.Index] || *f.Size < 0 {
			return nil, ports.ErrBadResponse
		}
		progress, e := exactObservationProgress(f.Progress)
		if e != nil {
			return nil, e
		}
		name := *f.Name
		if name == "" || len(name) > 1024 || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n") || name == "." || name == ".." || strings.HasPrefix(name, "../") || names[name] {
			return nil, ports.ErrBadResponse
		}
		ids[*f.Index] = true
		names[name] = true
		out = append(out, ports.TorrentFile{Index: *f.Index, Name: name, SizeBytes: *f.Size, Progress: progress})
	}
	return out, nil
}
