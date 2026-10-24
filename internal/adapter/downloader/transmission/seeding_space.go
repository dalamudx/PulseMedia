package transmission

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"time"
)

func (r seedingReader) ReadSeedingFreeSpace(ctx context.Context, downloadPath string) (ports.SeedingPathSpace, error) {
	out := ports.SeedingPathSpace{Path: downloadPath}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !automaticTVPath(downloadPath, true) {
		return out, ports.ErrRejected
	}
	if r.client == nil {
		return out, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := r.client.automaticTVReadRPC(bounded, "free-space", map[string]any{"path": downloadPath})
	if err != nil {
		return out, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, ports.ErrBadResponse
	}
	var actual string
	if json.Unmarshal(fields["path"], &actual) != nil || actual != downloadPath {
		return out, ports.ErrBadResponse
	}
	size := fields["size-bytes"]
	if len(size) == 0 || string(size) == "null" {
		return out, nil
	}
	var free int64
	if json.Unmarshal(size, &free) != nil {
		return out, ports.ErrBadResponse
	}
	if free < 0 {
		return out, nil
	}
	out.FreeBytes = &free
	return out, nil
}
