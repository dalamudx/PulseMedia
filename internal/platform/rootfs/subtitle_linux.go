//go:build linux

package rootfs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

// PublishSubtitle uses the same synchronous FD publication and recovery contract
// as PublishSidecar, with a separate, exact subtitle request gate.
func (r *Root) PublishSubtitle(ctx context.Context, req SidecarRequest) (SidecarResult, error) {
	return r.publishSidecar(ctx, req, sidecarOps{subtitle: true})
}

func (*Filesystem) PublishSubtitle(ctx context.Context, root ports.TransferRoot, req ports.SidecarRequest) (ports.SidecarResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, err
	}
	cap, ok := root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.SidecarResult{Disposition: ports.SidecarNotPublished}, ports.ErrFileIdentity
	}
	out, err := cap.root.PublishSubtitle(ctx, req)
	return out, sidecarPortError(err)
}

func (*Filesystem) HashSubtitleVideo(ctx context.Context, root ports.TransferRoot, relative string, expected ports.FileIdentity) (ports.SubtitleVideoHash, error) {
	if err := ctx.Err(); err != nil {
		return ports.SubtitleVideoHash{}, err
	}
	cap, ok := root.(*capability)
	if !ok || cap == nil || cap.root == nil {
		return ports.SubtitleVideoHash{}, ports.ErrFileIdentity
	}
	out, err := cap.root.hashSubtitleVideo(ctx, relative, expected, unix.Pread)
	if errors.Is(err, ports.ErrSubtitleVideoTooSmall) {
		return ports.SubtitleVideoHash{}, ports.ErrSubtitleVideoTooSmall
	}
	return out, sidecarPortError(err)
}

// The per-call read seam lets tests observe bounded reads and change the source
// during I/O without a mutable global hook or a detached reader.
func (r *Root) hashSubtitleVideo(ctx context.Context, relative string, expected ports.FileIdentity, read func(int, []byte, int64) (int, error)) (out ports.SubtitleVideoHash, err error) {
	defer func() {
		err = errors.Join(err, ctx.Err())
		if err != nil {
			out = ports.SubtitleVideoHash{}
		}
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if _, err = sidecarPath(relative, false); err != nil {
		return out, err
	}
	if !subtitleVideo(relative) {
		return out, ErrSidecarRequest
	}
	if r == nil {
		return out, ErrIdentity
	}
	file, current, err := r.source(relative)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if current != expected {
		return out, ErrIdentity
	}
	if current.Size < ports.MinSubtitleVideoBytes {
		return out, ports.ErrSubtitleVideoTooSmall
	}
	offsets := [...]int64{4096, (current.Size / 3) * 2, current.Size / 3, current.Size - 8192}
	var block [4096]byte
	hashes := make([]string, 0, len(offsets))
	for _, offset := range offsets {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		n, e := read(int(file.Fd()), block[:], offset)
		if e != nil {
			return out, e
		}
		if n != len(block) {
			return out, io.ErrUnexpectedEOF
		}
		sum := md5.Sum(block[:])
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	st, err := sidecarStat(int(file.Fd()))
	if err != nil {
		return out, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || identity(st) != expected {
		return out, ErrIdentity
	}
	// Rewalk from the capability, not the old parent FD: detect replaced parents
	// as well as a replaced leaf while the original inode remains pinned.
	reopened, now, err := r.source(relative)
	if err != nil {
		return out, err
	}
	err = reopened.Close()
	if err != nil {
		return out, err
	}
	if now != expected {
		return out, ErrIdentity
	}
	return ports.SubtitleVideoHash{Hash: strings.Join(hashes, ";"), SizeBytes: current.Size}, nil
}
