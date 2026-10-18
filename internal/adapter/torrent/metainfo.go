// Package torrent inspects bounded v1 metainfo without network or filesystem I/O.
package torrent

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Inspector struct{}

var _ ports.TorrentMetainfoInspector = Inspector{}

type node struct {
	kind       byte
	integer    int64
	data       []byte
	list       []node
	dict       map[string]node
	start, end int
}

type decoder struct {
	data       []byte
	pos, nodes int
}

// Canonical dictionaries, integer syntax and complete consumption prevent two
// decoders from interpreting different file trees for the same inspected bytes.
func (d *decoder) value(depth int) (node, bool) {
	if depth > 32 || d.nodes >= 50000 || d.pos >= len(d.data) {
		return node{}, false
	}
	d.nodes++
	n := node{kind: d.data[d.pos], start: d.pos}
	d.pos++
	switch n.kind {
	case 'i':
		end := bytes.IndexByte(d.data[d.pos:], 'e')
		if end < 1 {
			return node{}, false
		}
		s := string(d.data[d.pos : d.pos+end])
		if s[0] == '+' || s == "-0" || (len(s) > 1 && s[0] == '0') || (len(s) > 2 && s[0] == '-' && s[1] == '0') {
			return node{}, false
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return node{}, false
		}
		n.integer = v
		d.pos += end + 1
	case 'l', 'd':
		if n.kind == 'd' {
			n.dict = map[string]node{}
		}
		var previous []byte
		for {
			if d.pos >= len(d.data) {
				return node{}, false
			}
			if d.data[d.pos] == 'e' {
				d.pos++
				break
			}
			if n.kind == 'l' {
				v, ok := d.value(depth + 1)
				if !ok {
					return node{}, false
				}
				n.list = append(n.list, v)
			} else {
				key, ok := d.value(depth + 1)
				if !ok || key.kind != 's' || (previous != nil && bytes.Compare(previous, key.data) >= 0) {
					return node{}, false
				}
				previous = key.data
				v, ok := d.value(depth + 1)
				if !ok {
					return node{}, false
				}
				n.dict[string(key.data)] = v
			}
		}
	default:
		d.pos--
		begin := d.pos
		for d.pos < len(d.data) && d.data[d.pos] >= '0' && d.data[d.pos] <= '9' {
			d.pos++
		}
		if d.pos == begin || d.pos >= len(d.data) || d.data[d.pos] != ':' || (d.pos-begin > 1 && d.data[begin] == '0') {
			return node{}, false
		}
		size, err := strconv.ParseInt(string(d.data[begin:d.pos]), 10, 64)
		if err != nil || size < 0 {
			return node{}, false
		}
		d.pos++
		if size > int64(len(d.data)-d.pos) {
			return node{}, false
		}
		n.kind = 's'
		n.data = d.data[d.pos : d.pos+int(size)]
		d.pos += int(size)
	}
	n.end = d.pos
	return n, true
}

func component(n node) (string, bool) {
	if n.kind != 's' || len(n.data) == 0 || len(n.data) > 255 || !utf8.Valid(n.data) {
		return "", false
	}
	s := string(n.data)
	if s == "." || s == ".." || strings.ContainsAny(s, "/\\:<>\"|?*") || strings.TrimSpace(s) != s {
		return "", false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return "", false
		}
	}
	// Trailing periods/spaces and Windows device names have nonportable aliases.
	if strings.HasSuffix(s, ".") {
		return "", false
	}
	stem := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return "", false
	}
	return s, true
}

func filePath(n node) (string, bool) {
	if n.kind != 'l' || len(n.list) < 1 || len(n.list) > 32 {
		return "", false
	}
	parts := make([]string, 0, len(n.list))
	for _, p := range n.list {
		s, ok := component(p)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	s := strings.Join(parts, "/")
	return s, len(s) <= 4096
}

func ordinary(n node) bool {
	if _, ok := n.dict["symlink path"]; ok {
		return false
	}
	a, ok := n.dict["attr"]
	return !ok || (a.kind == 's' && len(a.data) == 0)
}

func (Inspector) InspectTorrent(input []byte) (*ports.TorrentMetainfo, error) {
	invalid := ports.ErrInvalidTorrentMetainfo
	if len(input) == 0 || len(input) > ports.MaxTorrentMetainfoBytes {
		return nil, invalid
	}
	raw := bytes.Clone(input)
	d := decoder{data: raw}
	root, ok := d.value(0)
	if !ok || root.kind != 'd' || d.pos != len(raw) {
		return nil, invalid
	}
	if encoding, exists := root.dict["encoding"]; exists && (encoding.kind != 's' || !strings.EqualFold(string(encoding.data), "utf-8")) {
		return nil, invalid
	}
	info, ok := root.dict["info"]
	if !ok || info.kind != 'd' || !ordinary(info) {
		return nil, invalid
	}
	if _, exists := info.dict["meta version"]; exists {
		return nil, invalid
	}
	if _, exists := info.dict["file tree"]; exists {
		return nil, invalid
	}
	name, ok := component(info.dict["name"])
	if !ok {
		return nil, invalid
	}
	if alias, exists := info.dict["name.utf-8"]; exists {
		v, good := component(alias)
		if !good || v != name {
			return nil, invalid
		}
	}
	out := &ports.TorrentMetainfo{TorrentBytes: raw, BytesSHA256: sha256.Sum256(raw)}
	hash := sha1.Sum(raw[info.start:info.end])
	out.InfoHash = hex.EncodeToString(hash[:])
	seen := map[string]bool{}
	add := func(p string, length node) bool {
		if length.kind != 'i' || length.integer < 0 || out.TotalBytes > math.MaxInt64-length.integer {
			return false
		}
		folded := strings.ToLower(p)
		if seen[folded] {
			return false
		}
		seen[folded] = true
		out.TotalBytes += length.integer
		out.Files = append(out.Files, media.TVTorrentFile{Path: p, Size: length.integer})
		return true
	}
	files, multi := info.dict["files"]
	if multi {
		if _, both := info.dict["length"]; both {
			return nil, invalid
		}
		if files.kind != 'l' || len(files.list) < 1 || len(files.list) > 4096 {
			return nil, invalid
		}
		for _, file := range files.list {
			if file.kind != 'd' || !ordinary(file) {
				return nil, invalid
			}
			p, good := filePath(file.dict["path"])
			if !good {
				return nil, invalid
			}
			if alias, exists := file.dict["path.utf-8"]; exists {
				v, valid := filePath(alias)
				if !valid || v != p {
					return nil, invalid
				}
			}
			if len(name)+1+len(p) > 4096 || !add(name+"/"+p, file.dict["length"]) {
				return nil, invalid
			}
		}
	} else if !add(name, info.dict["length"]) {
		return nil, invalid
	}
	// A file cannot also be a parent directory of a different file.
	for p := range seen {
		for parent := strings.LastIndexByte(p, '/'); parent >= 0; parent = strings.LastIndexByte(p[:parent], '/') {
			if seen[p[:parent]] {
				return nil, invalid
			}
		}
	}
	pieceLength, pieces := info.dict["piece length"], info.dict["pieces"]
	if out.TotalBytes < 1 || pieceLength.kind != 'i' || pieceLength.integer < 1 || pieces.kind != 's' || len(pieces.data)%20 != 0 || int64(len(pieces.data)/20) != (out.TotalBytes-1)/pieceLength.integer+1 {
		return nil, invalid
	}
	return out, nil
}
