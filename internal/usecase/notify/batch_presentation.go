package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
)

// This v1 Chinese template is pinned for historical replays. New wording requires a new version.
// Render only typed UUID evidence, never raw titles, paths, links or integration configuration.
func applyBatchPresentation(n *ports.Notification, aggregate string, raw []byte) error {
	var p ports.NotificationBatchPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF || p.Version != "notification.media.v1" || p.MediaID == uuid.Nil || p.MediaID.String() != aggregate || len(p.Members) < 1 || len(p.Members) > 32 {
		return ports.ErrBadResponse
	}
	units, files, roots, sources := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, m := range p.Members {
		if m.SourceEventID == uuid.Nil || m.UnitID == uuid.Nil || m.FileID == uuid.Nil || m.RootID == uuid.Nil || sources[m.SourceEventID] {
			return ports.ErrBadResponse
		}
		sources[m.SourceEventID] = true
		units[m.UnitID] = true
		files[m.FileID] = true
		roots[m.RootID] = true
	}
	n.Title = "PulseMedia：媒体入库记录"
	n.Body = fmt.Sprintf("本批入库记录已提交：%d 个单元，%d 个文件，%d 个目标库。\n媒体 ID：%s\n历史事件重放不代表当前拥有状态。", len(units), len(files), len(roots), p.MediaID)
	return nil
}
