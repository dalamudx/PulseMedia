package qbittorrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVObserver explicitly opts this native configuration into a complete,
// bounded read contract. Generic ListTorrents implementations cannot opt in by
// accident. Only ListTorrents is used by the synchronous observation path.
func (c *Client) CompleteTVObserver() ports.DownloadClient { return &completeTVObservationClient{c} }

type completeTVObservationClient struct{ *Client }

func (c *completeTVObservationClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	if tag == "" || strings.ContainsAny(tag, ",\x00\r\n") {
		return nil, ports.ErrBadResponse
	}
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/torrents/info?tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, httpFailure(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err = c.apiHTTPFailure(resp); err != nil {
		return nil, err
	}
	body, err := readHTTPBody(ctx, resp.Body, torrentResponseLimit)
	if err != nil {
		return nil, err
	}
	return decodeCompleteTVObservations(body)
}

func decodeCompleteTVObservations(body []byte) ([]*ports.TorrentInfo, error) {
	var rows []json.RawMessage
	if json.Unmarshal(body, &rows) != nil || rows == nil {
		return nil, ports.ErrBadResponse
	}
	result := make([]*ports.TorrentInfo, 0, len(rows))
	for _, raw := range rows {
		if !uniqueCompleteTVObservationObject(raw) {
			return nil, ports.ErrBadResponse
		}
		// A missing/null tag field is unknown data, not a proven tag mismatch.
		var required struct {
			Tags *string `json:"tags"`
		}
		if json.Unmarshal(raw, &required) != nil || required.Tags == nil || strings.ContainsAny(*required.Tags, "\x00\r\n") {
			return nil, ports.ErrBadResponse
		}
		// Reuse the native strict hash/path/size/progress/state decoder per row.
		// Preserve duplicate rows: a complete ambiguous response requires review.
		one := make([]byte, 0, len(raw)+2)
		one = append(one, '[')
		one = append(one, raw...)
		one = append(one, ']')
		decoded, err := decodeTorrentObservations(one)
		if err != nil || len(decoded) != 1 {
			return nil, ports.ErrBadResponse
		}
		result = append(result, decoded[0])
	}
	return result, nil
}
