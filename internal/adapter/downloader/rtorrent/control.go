package rtorrent

import (
	"context"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.NativeDownloadControl = (*Client)(nil)

func (c *Client) SetTorrentPaused(ctx context.Context, hash string, paused bool) error {
	if !rpcHash(hash) {
		return ports.ErrDownloadControlInvalid
	}
	method := "d.start"
	if paused {
		method = "d.stop"
	}
	// The shared transport disables redirects and request replay. An uncertain
	// acknowledgement belongs to the original persisted command, never a retry.
	ack, err := c.callXMLRPC(ctx, method, strings.ToLower(hash))
	if err != nil {
		return err
	}
	if ack.kind != "int" || ack.integer != 0 {
		return ports.ErrBadResponse
	}
	return nil
}

func (c *Client) ReadTorrentPaused(ctx context.Context, hash string) (*ports.DownloadPauseObservation, error) {
	if !rpcHash(hash) {
		return nil, ports.ErrDownloadControlInvalid
	}
	value, err := c.callXMLRPC(ctx, "d.multicall2", "", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate=", "d.state=")
	if err != nil {
		return nil, err
	}
	if value.kind != "array" || len(value.array) > 4096 {
		return nil, ports.ErrBadResponse
	}
	hash = strings.ToLower(hash)
	seen := make(map[string]bool, len(value.array))
	var result *ports.DownloadPauseObservation
	for _, row := range value.array {
		if row.kind != "array" || len(row.array) != 15 {
			return nil, ports.ErrBadResponse
		}
		info, err := rpcTorrent(rpcValue{kind: "array", array: row.array[:14]})
		if err != nil {
			return nil, err
		}
		state := row.array[14]
		if seen[info.InfoHash] || state.kind != "int" || state.integer < 0 || state.integer > 1 {
			return nil, ports.ErrBadResponse
		}
		seen[info.InfoHash] = true
		if info.InfoHash != hash {
			continue
		}
		// Inactive can mean queued for hashing or failed to start. Confirm only
		// a stable state where the persisted native intent and activity agree.
		if row.array[10].integer != 0 || row.array[11].integer != 0 || row.array[9].integer != state.integer {
			return nil, ports.ErrUnavailable
		}
		result = &ports.DownloadPauseObservation{Info: info, Paused: state.integer == 0}
	}
	if result == nil {
		return nil, ports.ErrNotFound
	}
	return result, nil
}
