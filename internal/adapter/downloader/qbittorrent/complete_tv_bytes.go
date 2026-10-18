package qbittorrent

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVByteSender explicitly opts this configured native instance into
// exact byte submission with affirmative acknowledgement. No URL fallback exists.
func (c *Client) CompleteTVByteSender() ports.DownloadClient { return &completeTVByteClient{c} }

type completeTVByteClient struct{ *Client }

func (c *completeTVByteClient) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if p.DownloadURL != "" || len(p.TorrentBytes) == 0 || len(p.TorrentBytes) > ports.MaxTorrentMetainfoBytes || !validTorrentHash(p.InfoHash) || p.SavePath == "" || p.Tags == "" {
		return ports.ErrBadResponse
	}
	if e := c.ensureLogin(ctx); e != nil {
		return e
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, e := form.CreateFormFile("torrents", "release.torrent")
	if e != nil {
		return ports.ErrBadResponse
	}
	if _, e = part.Write(p.TorrentBytes); e != nil {
		return ports.ErrBadResponse
	}
	for _, field := range [][2]string{{"savepath", p.SavePath}, {"tags", p.Tags}, {"paused", "false"}} {
		if e = form.WriteField(field[0], field[1]); e != nil {
			return ports.ErrBadResponse
		}
	}
	if e = form.Close(); e != nil {
		return ports.ErrBadResponse
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/add", &body)
	if e != nil {
		return ports.ErrBadResponse
	}
	// Go HTTP/2 retries peer stream resets using GetBody even for POST.
	// Once Add may have been received, only observation can resolve it.
	req.GetBody = nil
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, e := c.httpClient.Do(req)
	if e != nil {
		return httpFailure(ctx, e)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c.apiHTTPFailure(resp)
	}
	ack, e := readHTTPBody(ctx, resp.Body, 4096)
	if e != nil {
		return e
	}
	return confirmAddAcknowledgement(ack, p)
}
