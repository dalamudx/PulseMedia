package rtorrent

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.DownloadClient = (*Client)(nil)

type Client struct {
	httpClient         *http.Client
	baseURL            string
	username           string
	password           string
	configurationError error
}

// NewClient preserves the original probe constructor, including URL authentication.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	client, err := NewAuthenticatedClient(baseURL, "", "", httpClient)
	if err != nil {
		return &Client{configurationError: err}
	}
	return client
}

func NewAuthenticatedClient(baseURL, username, password string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return nil, ports.ErrBadResponse
	}
	if u.User != nil {
		if username != "" || password != "" {
			return nil, ports.ErrBadResponse
		}
		username = u.User.Username()
		password, _ = u.User.Password()
		u.User = nil
	}
	if strings.ContainsAny(username, ":\r\n\x00") || strings.ContainsAny(password, "\r\n\x00") {
		return nil, ports.ErrBadResponse
	}
	var safe http.Client
	if httpClient != nil {
		safe = *httpClient
	}
	if safe.Timeout <= 0 || safe.Timeout > 10*time.Second {
		safe.Timeout = 10 * time.Second
	}
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &safe, baseURL: u.String(), username: username, password: password}, nil
}

func (c *Client) callXMLRPC(ctx context.Context, method string, arguments ...any) (rpcValue, error) {
	if err := ctx.Err(); err != nil {
		return rpcValue{}, err
	}
	if c.configurationError != nil {
		return rpcValue{}, c.configurationError
	}
	body, err := encodeRPCRequest(method, arguments...)
	if err != nil {
		return rpcValue{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return rpcValue{}, ports.ErrBadResponse
	}
	// Mutations cannot be reconstructed after an uncertain transport result.
	req.GetBody = nil
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("Accept", "text/xml")
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return rpcValue{}, rpcTransportError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := ctx.Err(); err != nil {
		return rpcValue{}, rpcTransportError(ctx, err)
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return rpcValue{}, ports.ErrAuthRequired
	case http.StatusTooManyRequests:
		var delay time.Duration
		if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 0 && seconds <= 86400 {
			delay = time.Duration(seconds) * time.Second
		}
		return rpcValue{}, ports.NewRateLimitError(delay, "download RPC rate limited")
	case http.StatusOK:
	default:
		if response.StatusCode >= 500 {
			return rpcValue{}, ports.ErrUnavailable
		}
		return rpcValue{}, ports.ErrBadResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRPCResponseBytes+1))
	if err != nil {
		return rpcValue{}, rpcTransportError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return rpcValue{}, rpcTransportError(ctx, err)
	}
	return decodeRPCResponse(data)
}

func rpcTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ports.ErrUnavailable
}

func (c *Client) Test(ctx context.Context) error {
	value, err := c.callXMLRPC(ctx, "system.client_version", "")
	if err != nil {
		return err
	}
	if value.kind != "string" || len(value.text) < 3 || len(value.text) > 64 || value.text[0] < '0' || value.text[0] > '9' || !strings.Contains(value.text, ".") {
		return ports.ErrBadResponse
	}
	for _, r := range value.text {
		if r < 0x21 || r > 0x7e {
			return ports.ErrBadResponse
		}
	}
	return nil
}

func (c *Client) AddTorrent(ctx context.Context, p ports.AddTorrentParam) error {
	if err := ports.ValidateDownloadPayload(p); err != nil {
		return err
	}
	if p.InfoHash != "" && !rpcHash(p.InfoHash) {
		return ports.ErrBadResponse
	}
	if p.SavePath != "" && !rpcCommandPath(p.SavePath) {
		return ports.ErrBadResponse
	}
	tags, err := rpcTags(p.Tags)
	if err != nil {
		return err
	}
	method := "load.raw_start"
	var payload any
	if len(p.TorrentBytes) != 0 {
		metainfo, err := (torrent.Inspector{}).InspectTorrent(p.TorrentBytes)
		if err != nil || (p.InfoHash != "" && !strings.EqualFold(p.InfoHash, metainfo.InfoHash)) {
			return ports.ErrBadResponse
		}
		payload = metainfo.TorrentBytes
		if p.Paused {
			method = "load.raw"
		}
	} else {
		hash, err := ports.DownloadMagnetHash(p.DownloadURL)
		if err != nil || !rpcHash(hash) {
			return ports.ErrBadResponse
		}
		payload, method = p.DownloadURL, "load.start"
		if p.Paused {
			method = "load.normal"
		}
	}
	args := []any{"", payload}
	if p.SavePath != "" {
		args = append(args, `d.directory.set="`+p.SavePath+`"`)
	}
	if tags != "" {
		args = append(args, `d.custom.set=pulsemedia.tags,"`+tags+`"`)
	}
	value, err := c.callXMLRPC(ctx, method, args...)
	if err != nil {
		return err
	}
	if value.kind != "int" || value.integer != 0 {
		return ports.ErrBadResponse
	}
	return nil
}

func (c *Client) GetTorrent(ctx context.Context, infoHash string) (*ports.TorrentInfo, error) {
	if !rpcHash(infoHash) {
		return nil, ports.ErrBadResponse
	}
	rows, err := c.ListTorrents(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if strings.EqualFold(row.InfoHash, infoHash) {
			return row, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (c *Client) RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error {
	if deleteFiles {
		return ports.ErrRejected
	}
	if !rpcHash(infoHash) {
		return ports.ErrBadResponse
	}
	value, err := c.callXMLRPC(ctx, "d.erase", strings.ToUpper(infoHash))
	if err != nil {
		return err
	}
	if value.kind != "int" || value.integer != 0 {
		return ports.ErrBadResponse
	}
	return nil
}

func rpcHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func rpcPath(value string) bool {
	return len(value) <= 4096 && path.IsAbs(value) && path.Clean(value) == value && rpcXMLText(value) && !strings.ContainsAny(value, "\\\r\n\t")
}

// Load commands are parsed by rTorrent as commands, after XML parsing.
func rpcCommandPath(value string) bool {
	return rpcPath(value) && !strings.ContainsAny(value, "\"'$;{}(),`")
}

func rpcTags(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 2048 {
		return "", ports.ErrBadResponse
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return "", ports.ErrBadResponse
	}
	seen := map[string]bool{}
	for i, value := range parts {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || seen[value] {
			return "", ports.ErrBadResponse
		}
		for _, r := range value {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r) {
				continue
			}
			return "", ports.ErrBadResponse
		}
		seen[value], parts[i] = true, value
	}
	return strings.Join(parts, ","), nil
}
