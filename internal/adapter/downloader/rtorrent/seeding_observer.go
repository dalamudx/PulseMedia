package rtorrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type seedingReader struct{ client *Client }

var _ ports.SeedingReadClient = seedingReader{}

// This capability grants read access only. rTorrent removal is deliberately separate.
func (c *Client) SeedingObserver() ports.SeedingReadClient { return seedingReader{c} }

func (r seedingReader) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	id, err := uuid.Parse(strings.TrimPrefix(tag, "pulsemedia:op:"))
	if err != nil || id == uuid.Nil || tag != "pulsemedia:op:"+id.String() || !rpcHash(hash) || hash != strings.ToLower(hash) {
		return nil, ports.ErrRejected
	}
	if r.client == nil {
		return nil, ports.ErrUnavailable
	}
	rows, err := r.client.callXMLRPC(ctx, "d.multicall2", "", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate=", "d.ratio=")
	if err != nil {
		return nil, err
	}
	if rows.kind != "array" || len(rows.array) > 4096 {
		return nil, ports.ErrBadResponse
	}
	var found *ports.SeedingRemoteTorrent
	seen := map[string]bool{}
	for _, row := range rows.array {
		if row.kind != "array" || len(row.array) != 15 {
			return nil, ports.ErrBadResponse
		}
		info, err := rpcTorrent(rpcValue{kind: "array", array: row.array[:14]})
		if err != nil || seen[info.InfoHash] {
			return nil, ports.ErrBadResponse
		}
		seen[info.InfoHash] = true
		ratio := row.array[14]
		if ratio.kind != "int" || ratio.integer < 0 {
			return nil, ports.ErrBadResponse
		}
		if info.InfoHash != hash {
			continue
		}
		owns := false
		for _, label := range strings.Split(info.Tags, ",") {
			owns = owns || label == tag
		}
		if !owns {
			return nil, ports.ErrUnavailable
		}
		share := float64(ratio.integer) / 1000
		complete := info.TotalBytes > 0 && info.DownloadedBytes == info.TotalBytes && (info.State == "uploading" || info.State == "pausedUP")
		found = &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: info.SavePath, State: info.State, TotalBytes: info.TotalBytes, Facts: seeding.Observation{Complete: complete, ShareRatio: &share}}
	}
	if found == nil {
		return nil, ports.ErrNotFound
	}
	return found, nil
}

func (r seedingReader) ReadSeedingFreeSpace(ctx context.Context, downloadPath string) (ports.SeedingPathSpace, error) {
	if err := ctx.Err(); err != nil {
		return ports.SeedingPathSpace{}, err
	}
	if !rpcPath(downloadPath) {
		return ports.SeedingPathSpace{}, ports.ErrRejected
	}
	return ports.SeedingPathSpace{Path: downloadPath}, nil
}
