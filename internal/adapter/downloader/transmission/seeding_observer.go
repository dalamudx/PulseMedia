package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (c *Client) SeedingObserver() ports.SeedingReadClient { return seedingReader{client: c} }

type seedingReader struct{ client *Client }

var _ ports.SeedingReadClient = seedingReader{}

func (r seedingReader) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const prefix = "pulsemedia:op:"
	id, err := uuid.Parse(strings.TrimPrefix(tag, prefix))
	if !automaticTVHash(hash) || hash != strings.ToLower(hash) || err != nil || id == uuid.Nil || tag != prefix+id.String() {
		return nil, ports.ErrRejected
	}
	if r.client == nil {
		return nil, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rows, err := r.client.automaticTVReadTasks(bounded, map[string]any{"ids": []string{hash}, "fields": []string{"hashString", "downloadDir", "labels", "status", "totalSize", "haveValid", "error", "secondsSeeding", "uploadRatio"}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(rows) != 1 {
		return nil, ports.ErrUnavailable
	}
	row := rows[0]
	task, err := row.remote()
	if err != nil || task.InfoHash != hash {
		return nil, ports.ErrUnavailable
	}
	owned := false
	for _, label := range task.Tags {
		owned = owned || label == tag
	}
	if !owned {
		return nil, ports.ErrUnavailable
	}
	facts := seeding.Observation{Complete: task.Complete}
	if raw := row.SeedingSeconds; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 0 {
			return nil, ports.ErrBadResponse
		}
		facts.SeedingSeconds = &value
	}
	if raw := row.ShareRatio; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value float64
		if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, ports.ErrBadResponse
		}
		switch {
		case value >= 0:
			facts.ShareRatio = &value
		case value == -2:
			facts.InfiniteShareRatio = true
		case value == -1:
		default:
			return nil, ports.ErrBadResponse
		}
	}
	return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: task.SavePath, State: task.State, TotalBytes: task.TotalBytes, Facts: facts}, nil
}
