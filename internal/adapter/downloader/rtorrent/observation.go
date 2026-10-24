package rtorrent

import (
	"context"
	"path"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (c *Client) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	if tag != "" {
		normal, err := rpcTags(tag)
		if err != nil || normal != tag || strings.Contains(tag, ",") {
			return nil, ports.ErrBadResponse
		}
	}
	value, err := c.callXMLRPC(ctx, "d.multicall2", "", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate=")
	if err != nil {
		return nil, err
	}
	if value.kind != "array" || len(value.array) > 4096 {
		return nil, ports.ErrBadResponse
	}
	out := make([]*ports.TorrentInfo, 0, len(value.array))
	seen := make(map[string]bool, len(value.array))
	for _, row := range value.array {
		info, err := rpcTorrent(row)
		if err != nil {
			return nil, err
		}
		if seen[info.InfoHash] {
			return nil, ports.ErrBadResponse
		}
		seen[info.InfoHash] = true
		matches := tag == ""
		for _, label := range strings.Split(info.Tags, ",") {
			if label == tag {
				matches = true
			}
		}
		if matches {
			out = append(out, info)
		}
	}
	return out, nil
}

func rpcTorrent(value rpcValue) (*ports.TorrentInfo, error) {
	bad := func() (*ports.TorrentInfo, error) { return nil, ports.ErrBadResponse }
	if value.kind != "array" || len(value.array) != 14 {
		return bad()
	}
	v := value.array
	for _, i := range []int{0, 1, 2, 3, 12} {
		if v[i].kind != "string" {
			return bad()
		}
	}
	for _, i := range []int{4, 5, 6, 7, 8, 9, 10, 11, 13} {
		if v[i].kind != "int" || v[i].integer < 0 {
			return bad()
		}
	}
	for _, i := range []int{4, 8, 9, 11} {
		if v[i].integer > 1 {
			return bad()
		}
	}
	if !rpcHash(v[0].text) || len(v[1].text) > 1024 || !rpcXMLText(v[1].text) || v[6].integer > v[5].integer {
		return bad()
	}
	tags, err := rpcTags(v[12].text)
	if err != nil {
		return bad()
	}
	total, done := v[5].integer, v[6].integer
	complete := v[8].integer == 1
	if complete && (total == 0 || done != total) {
		return bad()
	}
	var progress float32
	if total > 0 {
		progress = float32(float64(done) / float64(total))
	}
	state := "pausedDL"
	if v[9].integer == 1 {
		state = "downloading"
	}
	if complete {
		state = "pausedUP"
		if v[9].integer == 1 {
			state = "uploading"
		}
	}
	checking := v[10].integer != 0 || v[11].integer == 1
	if checking {
		state = "checkingDL"
		if complete {
			state = "checkingUP"
		}
	}
	directory, base := v[2].text, v[3].text
	if directory != "" && !rpcPath(directory) || base != "" && !rpcPath(base) {
		return bad()
	}
	contentPath := ""
	if total > 0 {
		if directory == "" || base == "" || v[1].text == "" || path.Base(v[1].text) != v[1].text || v[1].text == "." || v[1].text == ".." {
			return bad()
		}
		if v[4].integer == 1 {
			if base != directory || path.Base(base) != v[1].text {
				return bad()
			}
			directory = path.Dir(directory)
		} else if base != path.Join(directory, v[1].text) {
			return bad()
		}
		if complete && !checking {
			contentPath = base
		}
	}
	var ratio float64
	if done > 0 {
		ratio = float64(v[7].integer) / float64(done)
	}
	rate := v[13].integer
	return &ports.TorrentInfo{InfoHash: strings.ToLower(v[0].text), Name: v[1].text, SavePath: directory, ContentPath: contentPath, Progress: progress, TotalBytes: total, DownloadedBytes: done, UploadedBytes: v[7].integer, Ratio: ratio, State: state, Tags: tags, DownloadRateBytesPerSecond: &rate}, nil
}
