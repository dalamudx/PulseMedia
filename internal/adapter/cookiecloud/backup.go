package cookiecloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.CookieCloudReader = (*Client)(nil)

// ReadBackup never sends a password or asks the server to decrypt a backup.
// The result preserves scope; it is not permission to change a site credential.
func (c *Client) ReadBackup(ctx context.Context, serverURL, userUUID, password string) (*ports.CookieCloudBackup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(serverURL)
	if err != nil || endpoint == nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.RawPath != "" || userUUID == "" || len(userUUID) > 128 || password == "" || len(password) > 4096 {
		return nil, ports.ErrRejected
	}
	for _, r := range userUUID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return nil, ports.ErrRejected
		}
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/get/" + userUUID
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ports.ErrRejected
	}
	request.Header.Set("Accept", "application/json")
	client := *c.httpClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 {
		client.Timeout = 15 * time.Second
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, cookieReadError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ports.ErrAuthRequired
	case http.StatusNotFound:
		return nil, ports.ErrNotFound
	case http.StatusTooManyRequests:
		seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		if seconds < 0 || seconds > 86400 {
			seconds = 0
		}
		return nil, ports.NewRateLimitError(time.Duration(seconds)*time.Second, "CookieCloud request limited")
	default:
		if response.StatusCode >= 500 {
			return nil, ports.ErrUnavailable
		}
		return nil, ports.ErrBadResponse
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxEncryptedBackupBytes+1))
	if err != nil {
		return nil, cookieReadError(ctx, err)
	}
	defer clear(raw)
	if len(raw) > maxEncryptedBackupBytes || !uniqueBackupJSON(raw) {
		return nil, ports.ErrBadResponse
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		return nil, ports.ErrBadResponse
	}
	var encrypted, kind string
	if json.Unmarshal(envelope["encrypted"], &encrypted) != nil || encrypted == "" {
		return nil, ports.ErrBadResponse
	}
	if value, ok := envelope["crypto_type"]; ok && json.Unmarshal(value, &kind) != nil {
		return nil, ports.ErrBadResponse
	}
	plain, err := DecryptBackup(encrypted, kind, userUUID, password)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !uniqueBackupJSON(plain) {
		return nil, ports.ErrBadResponse
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(plain, &data) != nil {
		return nil, ports.ErrBadResponse
	}
	var groups map[string][]ports.CookieCloudCookie
	if json.Unmarshal(data["cookie_data"], &groups) != nil || groups == nil || len(groups) > 4096 {
		return nil, ports.ErrBadResponse
	}
	total := 0
	for group, items := range groups {
		if group == "" || len(group) > 253 || items == nil {
			return nil, ports.ErrBadResponse
		}
		total += len(items)
		if total > 10000 {
			return nil, ports.ErrBadResponse
		}
		for _, item := range items {
			if !validCookiePair(item.Name, item.Value) {
				return nil, ports.ErrBadResponse
			}
		}
	}
	return &ports.CookieCloudBackup{Cookies: groups}, nil
}

func cookieReadError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

func validCookiePair(name, value string) bool {
	if name == "" || len(name) > 1024 || len(value) > 16384 {
		return false
	}
	for _, r := range name {
		if r < 33 || r > 126 || strings.ContainsRune("()<>@,;:\\\"/[]?={} ", r) {
			return false
		}
	}
	for _, r := range value {
		if r < 33 || r > 126 || strings.ContainsRune("\",;\\", r) {
			return false
		}
	}
	return true
}

func uniqueBackupJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var read func(int) bool
	read = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return false
				}
				keys[name] = true
				if !read(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !read(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !read(0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}
