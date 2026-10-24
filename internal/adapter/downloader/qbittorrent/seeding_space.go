package qbittorrent

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/url"
	"strings"
	"time"
)

func (r seedingReader) ReadSeedingFreeSpace(ctx context.Context, downloadPath string) (ports.SeedingPathSpace, error) {
	out := ports.SeedingPathSpace{Path: downloadPath}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !automaticTVAbsolutePath(downloadPath) {
		return out, ports.ErrRejected
	}
	if r.client == nil {
		return out, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reader := automaticTVReader(r)
	readDefault := func() (string, error) {
		raw, err := reader.readNative(bounded, "app/defaultSavePath", nil)
		if err != nil {
			return "", err
		}
		value := strings.TrimRight(string(raw), "\r\n")
		if !automaticTVAbsolutePath(value) {
			return "", ports.ErrBadResponse
		}
		return strings.TrimRight(value, "/\\"), nil
	}
	before, err := readDefault()
	if err != nil {
		return out, err
	}
	if before != strings.TrimRight(downloadPath, "/\\") {
		return out, nil
	}
	body, err := reader.readNative(bounded, "sync/maindata", url.Values{"rid": {"0"}})
	if err != nil {
		return out, err
	}
	if !automaticTVUniqueJSON(body) {
		return out, ports.ErrBadResponse
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return out, ports.ErrBadResponse
	}
	var full bool
	if json.Unmarshal(fields["full_update"], &full) != nil || !full {
		return out, ports.ErrBadResponse
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(fields["server_state"], &state) != nil || state == nil {
		return out, ports.ErrBadResponse
	}
	raw := state["free_space_on_disk"]
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var free int64
	if json.Unmarshal(raw, &free) != nil {
		return out, ports.ErrBadResponse
	}
	if free < 0 {
		return out, nil
	}
	after, err := readDefault()
	if err != nil {
		return out, err
	}
	if before != after {
		return out, nil
	}
	out.FreeBytes = &free
	return out, nil
}
