package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// SeedingRemover is a single local attempt, used only after a durable permit.
func (c *Client) SeedingRemover(target ports.SeedingRemovalTarget, authorize ports.SeedingRemovalAuthorizer) (ports.SeedingRemovalClient, error) {
	if authorize == nil {
		return nil, ports.ErrRejected
	}
	const prefix = "pulsemedia:op:"
	id, err := uuid.Parse(strings.TrimPrefix(target.OperationTag, prefix))
	if !automaticTVHash(target.InfoHash) || target.InfoHash != strings.ToLower(target.InfoHash) || err != nil || id == uuid.Nil || target.OperationTag != prefix+id.String() || !automaticTVPath(target.SavePath, true) || target.TotalBytes <= 0 {
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
	body, err := json.Marshal(rpcRequest{Method: "torrent-remove", Tag: 1, Arguments: map[string]any{"ids": []string{r.target.InfoHash}, "delete-local-data": false}})
	if err != nil {
		return ports.SeedingRemovalNotDispatched, ports.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(bounded, http.MethodPost, r.client.rpcURL, bytes.NewReader(body))
	if err != nil {
		return ports.SeedingRemovalNotDispatched, ports.ErrUnavailable
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	if r.client.username != "" || r.client.password != "" {
		req.SetBasicAuth(r.client.username, r.client.password)
	}
	r.client.mu.Lock()
	sid := r.client.sessionID
	r.client.mu.Unlock()
	if sid != "" {
		req.Header.Set("X-Transmission-Session-Id", sid)
	}
	safe := *r.client.httpClient
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Unlike read-only RPCs, this mutation is never retried, even on a new 409.
	resp, err := safe.Do(req)
	if err != nil {
		if bounded.Err() != nil {
			return ports.SeedingRemovalUnknown, bounded.Err()
		}
		return ports.SeedingRemovalUnknown, ports.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusConflict {
		return ports.SeedingRemovalUnknown, ports.ErrAuthRequired
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return ports.SeedingRemovalUnknown, ports.NewRateLimitError(0, "seeding removal rate limited")
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" {
		return ports.SeedingRemovalUnknown, ports.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if bounded.Err() != nil {
		return ports.SeedingRemovalUnknown, bounded.Err()
	}
	if err != nil || len(data) > 1<<20 || !utf8.Valid(data) || automaticTVJSON(data) != nil {
		return ports.SeedingRemovalUnknown, ports.ErrBadResponse
	}
	var response struct {
		Result    string                     `json:"result"`
		Arguments map[string]json.RawMessage `json:"arguments"`
		Tag       *int64                     `json:"tag"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil || response.Result != "success" || response.Arguments == nil || len(response.Arguments) != 0 || response.Tag == nil || *response.Tag != 1 {
		return ports.SeedingRemovalUnknown, ports.ErrBadResponse
	}
	return ports.SeedingRemovalAcknowledged, nil
}
