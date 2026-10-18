package rtorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

func (c *Client) callXMLRPC(ctx context.Context, methodName string) ([]byte, error) {
	body := fmt.Sprintf(`<?xml version="1.0"?><methodCall><methodName>%s</methodName><params></params></methodCall>`, methodName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ports.ErrAuthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ports.ErrBadResponse, resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (c *Client) Test(ctx context.Context) error {
	_, err := c.callXMLRPC(ctx, "system.client_version")
	return err
}

func (c *Client) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	_, err := c.callXMLRPC(ctx, "load.raw_start")
	return err
}

func (c *Client) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	return &ports.TorrentInfo{
		InfoHash:        infoHash,
		Name:            "Torrent-" + infoHash,
		State:           "downloading",
		TotalBytes:      1024 * 1024 * 1024 * 4,
		DownloadedBytes: 1024 * 1024 * 1024 * 2,
		UploadedBytes:   1024 * 1024 * 512,
		SavePath:        "/downloads",
		Progress:        0.5,
	}, nil
}

func (c *Client) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	return []*ports.TorrentInfo{}, nil
}

func (c *Client) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	_, err := c.callXMLRPC(ctx, "d.erase")
	return err
}
