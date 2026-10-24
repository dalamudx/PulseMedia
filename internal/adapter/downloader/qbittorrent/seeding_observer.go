package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (c *Client) SeedingObserver() ports.SeedingReadClient { return seedingReader{client: c} }

type seedingReader struct{ client *Client }

var _ ports.SeedingReadClient = seedingReader{}

func (r seedingReader) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !automaticTVHash(hash) || hash != strings.ToLower(hash) || !automaticTVOperationTag(tag) {
		return nil, ports.ErrRejected
	}
	if r.client == nil {
		return nil, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := (&automaticTVReader{client: r.client}).read(bounded, "info", url.Values{"hashes": {hash}})
	if err != nil {
		return nil, err
	}
	tasks, err := decodeAutomaticTVTasks(body)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(tasks) != 1 || tasks[0].InfoHash != hash {
		return nil, ports.ErrUnavailable
	}
	task := tasks[0]
	owned := false
	for _, label := range task.Tags {
		owned = owned || label == tag
	}
	if !owned {
		return nil, ports.ErrUnavailable
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(body, &rows) != nil || len(rows) != 1 {
		return nil, ports.ErrBadResponse
	}
	facts := seeding.Observation{Complete: task.Complete && task.TotalBytes > 0}
	if raw := rows[0]["seeding_time"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 0 {
			return nil, ports.ErrBadResponse
		}
		facts.SeedingSeconds = &value
	}
	if raw := rows[0]["ratio"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value float64
		if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, ports.ErrBadResponse
		}
		if value >= 0 {
			facts.ShareRatio = &value
		}
	}
	// qBittorrent's ratio is capped at 9999; it is not native infinity. Default
	// daemon disk capacity is not evidence for a task's possibly different save path.
	return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: task.SavePath, State: task.State, TotalBytes: task.TotalBytes, Facts: facts}, nil
}
