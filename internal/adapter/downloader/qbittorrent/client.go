package qbittorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadClient = (*Client)(nil)

type Client struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string
	mu         sync.Mutex
	loggedIn   bool
}

func NewClient(baseURL, username, password string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cookie jar: %w", err)
		}
		httpClient = &http.Client{
			Jar:     jar,
			Timeout: 15 * time.Second,
		}
	} else {
		copyClient := *httpClient
		httpClient = &copyClient
	}
	if httpClient.Jar == nil {
		jar, _ := cookiejar.New(nil)
		httpClient.Jar = jar
	}

	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
	}, nil
}

func (c *Client) ensureLogin(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loggedIn {
		return nil
	}

	loginURL := fmt.Sprintf("%s/api/v2/auth/login", c.baseURL)
	formData := url.Values{}
	formData.Set("username", c.username)
	formData.Set("password", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return ports.ErrBadResponse
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return httpFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusOK {
		body, e := readHTTPBody(ctx, resp.Body, 4096)
		if e != nil {
			return e
		}

		if string(body) == "Ok." {
			c.loggedIn = true
			return nil
		}
	}

	if resp.StatusCode == http.StatusNoContent {
		if err := c.confirmNoContentLogin(ctx); err != nil {
			return err
		}
		c.loggedIn = true
		return nil
	}
	if resp.StatusCode == http.StatusOK {
		return ports.ErrAuthRequired
	}
	return httpStatusFailure(resp)
}

func (c *Client) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if err := ports.ValidateDownloadPayload(p); err != nil {
		return err
	}
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if len(p.TorrentBytes) > 0 {
		part, err := writer.CreateFormFile("torrents", "release.torrent")
		if err != nil {
			return fmt.Errorf("failed to create multipart form file: %w", err)
		}
		if _, err := part.Write(p.TorrentBytes); err != nil {
			return fmt.Errorf("failed to write torrent bytes: %w", err)
		}
	} else if p.DownloadURL != "" {
		_ = writer.WriteField("urls", p.DownloadURL)
	} else {
		return errors.New("either TorrentBytes or DownloadURL must be provided")
	}

	if p.SavePath != "" {
		_ = writer.WriteField("savepath", p.SavePath)
	}
	if p.Tags != "" {
		_ = writer.WriteField("tags", p.Tags)
	}
	if p.Paused {
		_ = writer.WriteField("paused", "true")
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	addURL := fmt.Sprintf("%s/api/v2/torrents/add", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addURL, &body)
	if err != nil {
		return ports.ErrBadResponse
	}
	// A response loss must be observed, never retried by the HTTP transport.
	req.GetBody = nil
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ports.ErrTimeout
		}
		return httpFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusOK {
		ack, err := readHTTPBody(ctx, resp.Body, 4096)
		if err != nil {
			return err
		}
		return confirmAddAcknowledgement(ack, p)
	}

	return c.apiHTTPFailure(resp)
}

func (c *Client) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	if !validTorrentHash(infoHash) {
		return nil, ports.ErrBadResponse
	}
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}

	infoHash = strings.ToLower(infoHash)
	infoURL := fmt.Sprintf("%s/api/v2/torrents/info?hashes=%s", c.baseURL, url.QueryEscape(infoHash))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ports.ErrTimeout
		}
		return nil, httpFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if err := c.apiHTTPFailure(resp); err != nil {
		return nil, err
	}

	body, err := readHTTPBody(ctx, resp.Body, torrentResponseLimit)
	if err != nil {
		return nil, err
	}
	items, err := decodeTorrentObservations(body)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(items) != 1 || !strings.EqualFold(items[0].InfoHash, infoHash) {
		return nil, ports.ErrBadResponse
	}
	return items[0], nil
}

func (c *Client) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}

	listURL := fmt.Sprintf("%s/api/v2/torrents/info", c.baseURL)
	if tag != "" {
		listURL += fmt.Sprintf("?tag=%s", url.QueryEscape(tag))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, ports.ErrBadResponse
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, httpFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if err := c.apiHTTPFailure(resp); err != nil {
		return nil, err
	}
	body, err := readHTTPBody(ctx, resp.Body, torrentResponseLimit)
	if err != nil {
		return nil, err
	}
	return decodeTorrentObservations(body)
}

func (c *Client) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	if !validTorrentHash(infoHash) {
		return ports.ErrBadResponse
	}
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}

	deleteURL := fmt.Sprintf("%s/api/v2/torrents/delete", c.baseURL)
	formData := url.Values{}
	formData.Set("hashes", strings.ToLower(infoHash))
	formData.Set("deleteFiles", fmt.Sprintf("%t", deleteFiles))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deleteURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return ports.ErrBadResponse
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return httpFailure(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	return c.apiHTTPFailure(resp)
}

func (c *Client) Test(ctx context.Context) error {
	return c.ensureLogin(ctx)
}
