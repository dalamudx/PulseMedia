package qbittorrent

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// SeedingRemover binds a local single-use client to an already permitted task.
// Constructing another client cannot replace the caller's permanent SQL permit.
func (c *Client) SeedingRemover(target ports.SeedingRemovalTarget, authorize ports.SeedingRemovalAuthorizer) (ports.SeedingRemovalClient, error) {
	if authorize == nil {
		return nil, ports.ErrRejected
	}
	if !automaticTVHash(target.InfoHash) || target.InfoHash != strings.ToLower(target.InfoHash) || !automaticTVOperationTag(target.OperationTag) || !automaticTVAbsolutePath(target.SavePath) || target.TotalBytes <= 0 {
		return nil, ports.ErrRejected
	}
	if c == nil || c.httpClient == nil {
		return nil, ports.ErrUnavailable
	}
	return &seedingRemover{client: c, target: target, authorize: authorize}, nil
}

type seedingRemover struct {
	client    *Client
	target    ports.SeedingRemovalTarget
	authorize ports.SeedingRemovalAuthorizer
	used      atomic.Bool
}

var _ ports.SeedingRemovalClient = (*seedingRemover)(nil)

func (r *seedingRemover) RemoveRetainingFiles(ctx context.Context) (ports.SeedingRemovalOutcome, error) {
	if r == nil || !r.used.CompareAndSwap(false, true) {
		return ports.SeedingRemovalNotDispatched, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return ports.SeedingRemovalNotDispatched, err
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	observed, err := (seedingReader{client: r.client}).ReadSeedingTorrent(bounded, r.target.InfoHash, r.target.OperationTag)
	if err != nil {
		if err == ports.ErrNotFound {
			return ports.SeedingRemovalAbsent, nil
		}
		return ports.SeedingRemovalNotDispatched, err
	}
	if observed == nil || observed.InfoHash != r.target.InfoHash || observed.OperationTag != r.target.OperationTag || observed.SavePath != r.target.SavePath || observed.TotalBytes != r.target.TotalBytes || !observed.Facts.Complete {
		return ports.SeedingRemovalNotDispatched, ports.ErrRejected
	}
	if err = r.authorize(bounded, *observed); err != nil {
		return ports.SeedingRemovalNotDispatched, err
	}
	if err = bounded.Err(); err != nil {
		return ports.SeedingRemovalNotDispatched, err
	}
	form := url.Values{"hashes": {r.target.InfoHash}, "deleteFiles": {"false"}}
	req, err := http.NewRequestWithContext(bounded, http.MethodPost, r.client.baseURL+"/api/v2/torrents/delete", strings.NewReader(form.Encode()))
	if err != nil {
		return ports.SeedingRemovalNotDispatched, ports.ErrUnavailable
	}
	// Prevent replay of the mutation after transport failure, including HTTP/2 resets.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	safe := *r.client.httpClient
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safe.Do(req)
	if err != nil {
		if bounded.Err() != nil {
			return ports.SeedingRemovalUnknown, bounded.Err()
		}
		return ports.SeedingRemovalUnknown, ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if err = r.client.apiHTTPFailure(resp); err != nil {
		return ports.SeedingRemovalUnknown, err
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" {
		return ports.SeedingRemovalUnknown, ports.ErrBadResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if bounded.Err() != nil {
		return ports.SeedingRemovalUnknown, bounded.Err()
	}
	if err != nil || len(data) > 8192 || strings.TrimSpace(string(data)) != "" {
		return ports.SeedingRemovalUnknown, ports.ErrBadResponse
	}
	return ports.SeedingRemovalAcknowledged, nil
}
