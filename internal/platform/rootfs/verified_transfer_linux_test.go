//go:build linux

package rootfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type verifiedFixture struct {
	fs       *rootfs.Filesystem
	src, dst string
	roots    map[string]ports.TransferRoot
	proof    ports.TransferContent
	body     []byte
}

func newVerifiedFixture(t *testing.T, body []byte) *verifiedFixture {
	t.Helper()
	f := &verifiedFixture{fs: rootfs.NewFilesystem(), src: t.TempDir(), dst: t.TempDir(), body: body}
	if err := os.WriteFile(filepath.Join(f.src, "movie.mkv"), body, 0600); err != nil {
		t.Fatal(err)
	}
	roots, release, err := f.fs.LockRoots(t.Context(), f.src, f.dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	f.roots = roots
	id, err := roots[f.src].Inspect(t.Context(), "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	f.proof, err = f.fs.InspectContent(t.Context(), roots[f.src], "movie.mkv", id)
	if err != nil || f.proof.SHA256 != sha256.Sum256(body) {
		t.Fatalf("source content: %+v %v", f.proof, err)
	}
	return f
}

func (f *verifiedFixture) transfer(ctx context.Context, action string, key uuid.UUID) (ports.VerifiedTransferResult, error) {
	return f.fs.TransferVerified(ctx, f.roots[f.src], "movie.mkv", f.roots[f.dst], "Movie/video.mkv", f.proof, action, key)
}

func TestVerifiedTransferCopyAndMove(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			body := bytes.Repeat([]byte("complete video content\x00"), 256*1024)
			f := newVerifiedFixture(t, body)
			if err := os.Link(filepath.Join(f.src, "movie.mkv"), filepath.Join(f.src, "seed.mkv")); err != nil {
				t.Fatal(err)
			}
			key := uuid.New()
			out, err := f.transfer(t.Context(), action, key)
			if err != nil || out.Target.SHA256 != f.proof.SHA256 || out.SourceRemoved != (action == "move") {
				t.Fatalf("transfer: %+v %v", out, err)
			}
			if out.Target.File.Device == f.proof.File.Device && out.Target.File.Inode == f.proof.File.Inode {
				t.Fatal("copy reused the source inode")
			}
			data, err := os.ReadFile(filepath.Join(f.dst, "Movie/video.mkv"))
			if err != nil || !bytes.Equal(data, body) {
				t.Fatalf("published bytes: %v", err)
			}
			_, err = os.Stat(filepath.Join(f.src, "movie.mkv"))
			if action == "move" && !errors.Is(err, os.ErrNotExist) || action == "copy" && err != nil {
				t.Fatalf("original source: %v", err)
			}
			data, err = os.ReadFile(filepath.Join(f.src, "seed.mkv"))
			if err != nil || !bytes.Equal(data, body) {
				t.Fatalf("seeding link changed: %v", err)
			}
			again, err := f.transfer(t.Context(), action, key)
			if err != nil || again != out {
				t.Fatalf("observe full postcondition: %+v %v", again, err)
			}
		})
	}
}

func TestVerifiedTransferChecksFullContent(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 512*1024)
	for _, kind := range []string{"target", "source", "move-source-with-complete-target"} {
		t.Run(kind, func(t *testing.T) {
			f := newVerifiedFixture(t, body)
			changed := bytes.Clone(body)
			changed[len(changed)/2+123] ^= 0x7f
			source := filepath.Join(f.src, "movie.mkv")
			target := filepath.Join(f.dst, "Movie/video.mkv")
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			action := "copy"
			if kind == "target" {
				if err := os.WriteFile(target, changed, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(source, changed, 0600); err != nil {
					t.Fatal(err)
				}
				mt := time.Unix(0, f.proof.File.Mtime)
				if err := os.Chtimes(source, mt, mt); err != nil {
					t.Fatal(err)
				}
				id, err := f.roots[f.src].Inspect(t.Context(), "movie.mkv")
				if err != nil || id != f.proof.File {
					t.Fatalf("fixture must preserve the frozen stat: %+v %v", id, err)
				}
				if kind == "move-source-with-complete-target" {
					action = "move"
					if err := os.WriteFile(target, body, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := f.transfer(t.Context(), action, uuid.New()); err == nil {
				t.Fatal("same-size content substitution accepted")
			}
			data, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(data, map[bool][]byte{true: body, false: changed}[kind == "target"]) {
				t.Fatalf("source was removed or changed: %v", err)
			}
			if kind == "source" {
				if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("corrupt source published: %v", err)
				}
			} else {
				data, err = os.ReadFile(target)
				want := body
				if kind == "target" {
					want = changed
				}
				if err != nil || !bytes.Equal(data, want) {
					t.Fatalf("existing target overwritten: %v", err)
				}
			}
		})
	}
}

func TestVerifiedTransferRecoversPrivateMove(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "changed-content"}[corrupt], func(t *testing.T) {
			body := bytes.Repeat([]byte("frozen move bytes"), 128*1024)
			f := newVerifiedFixture(t, body)
			key := uuid.New()
			dir := filepath.Join(f.src, ".pulsemedia_transfers", key.String())
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			quarantine := filepath.Join(dir, "move-source")
			if err := os.Rename(filepath.Join(f.src, "movie.mkv"), quarantine); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				changed := bytes.Clone(body)
				changed[len(changed)/2] ^= 1
				if err := os.WriteFile(quarantine, changed, 0600); err != nil {
					t.Fatal(err)
				}
				mt := time.Unix(0, f.proof.File.Mtime)
				if err := os.Chtimes(quarantine, mt, mt); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(f.dst, "Movie/video.mkv")
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, body, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := f.transfer(t.Context(), "move", key)
			if corrupt {
				if !errors.Is(err, ports.ErrFileIdentity) {
					t.Fatalf("changed quarantine: %v", err)
				}
				if _, err := os.Stat(quarantine); err != nil {
					t.Fatalf("unknown file deleted: %v", err)
				}
			} else {
				if err != nil || !out.SourceRemoved {
					t.Fatalf("recover move: %+v %v", out, err)
				}
				if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source cleanup not complete: %v", err)
				}
			}
		})
	}
}

func TestVerifiedTransferReflinkCapability(t *testing.T) {
	f := newVerifiedFixture(t, bytes.Repeat([]byte("reflink content"), 128*1024))
	key := uuid.New()
	supported, err := f.fs.ProbeReflink(t.Context(), f.roots[f.src], "movie.mkv", f.roots[f.dst], f.proof.File, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dst, ".pulsemedia_transfers", key.String(), "reflink-probe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe left a file: %v", err)
	}
	out, err := f.transfer(t.Context(), "reflink", key)
	if supported {
		if err != nil || out.Target.SHA256 != f.proof.SHA256 {
			t.Fatalf("supported clone: %+v %v", out, err)
		}
	} else {
		if !errors.Is(err, ports.ErrFileUnsupported) {
			t.Fatalf("unsupported clone must not fall back to copy: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.dst, "Movie/video.mkv")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported clone published: %v", err)
		}
	}
}

func TestVerifiedTransferRejectsUnsafePathsAndStages(t *testing.T) {
	for _, kind := range []string{"escape", "target-symlink", "stage-symlink", "special-stage", "self-move", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			f := newVerifiedFixture(t, []byte("protected source"))
			outside := t.TempDir()
			key := uuid.New()
			ctx := t.Context()
			source, target := f.roots[f.src], f.roots[f.dst]
			rel := "Movie/video.mkv"
			want := ports.ErrFileSymlink
			switch kind {
			case "escape":
				rel = "../escape.mkv"
				want = ports.ErrFileEscape
			case "target-symlink":
				if err := os.Mkdir(filepath.Join(f.dst, "Movie"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "protected"), filepath.Join(f.dst, rel)); err != nil {
					t.Fatal(err)
				}
			case "stage-symlink":
				if err := os.Symlink(outside, filepath.Join(f.dst, ".pulsemedia_transfers")); err != nil {
					t.Fatal(err)
				}
			case "special-stage":
				dir := filepath.Join(f.dst, ".pulsemedia_transfers", key.String())
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(filepath.Join(dir, "content"), 0600); err != nil {
					t.Fatal(err)
				}
				want = ports.ErrFileConflict
			case "self-move":
				target = source
				rel = "./movie.mkv"
				want = ports.ErrFileConflict
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			_, err := f.fs.TransferVerified(ctx, source, "movie.mkv", target, rel, f.proof, "move", key)
			if !errors.Is(err, want) {
				t.Fatalf("unsafe transfer: want %v, got %v", want, err)
			}
			data, err := os.ReadFile(filepath.Join(f.src, "movie.mkv"))
			if err != nil || !bytes.Equal(data, f.body) {
				t.Fatalf("source changed: %v", err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("outside root changed: %v %v", entries, err)
			}
		})
	}
}

func TestVerifiedTransferCanceledStageCanRecover(t *testing.T) {
	f := newVerifiedFixture(t, bytes.Repeat([]byte("stage bytes\x00"), 6*1024*1024))
	key := uuid.New()
	dir := filepath.Join(f.dst, ".pulsemedia_transfers", key.String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_MODIFY); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var data [4096]byte
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, e := unix.Poll(poll, 100); e != nil {
				return
			}
			if poll[0].Revents&unix.POLLIN == 0 {
				continue
			}
			if _, e := unix.Read(fd, data[:]); e != nil && !errors.Is(e, unix.EAGAIN) {
				return
			}
			info, e := os.Stat(filepath.Join(dir, "content"))
			if e == nil && info.Size() > 0 {
				cancel()
				return
			}
		}
	}()
	_, err = f.transfer(ctx, "copy", key)
	cancel()
	<-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy did not stop at a real write: %v", err)
	}
	if _, e := os.Stat(filepath.Join(f.dst, "Movie/video.mkv")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("canceled copy published: %v", e)
	}
	if _, e := os.Stat(filepath.Join(dir, "content")); e != nil {
		t.Fatalf("missing recovery stage: %v", e)
	}
	out, err := f.transfer(t.Context(), "copy", key)
	if err != nil || out.Target.SHA256 != f.proof.SHA256 {
		t.Fatalf("resume canceled stage: %+v %v", out, err)
	}
	if _, e := os.Stat(filepath.Join(dir, "content")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("published stage still present: %v", e)
	}
}

func TestVerifiedTransferAcrossFilesystems(t *testing.T) {
	if os.Getenv("PULSE_ROOTFS_PRIVATE_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "--mount", "--propagation", "private", executable, "-test.run=^TestVerifiedTransferAcrossFilesystems$", "-test.v")
		command.Env = append(os.Environ(), "PULSE_ROOTFS_PRIVATE_CHILD=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("private filesystem namespace: %v\n%s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		return
	}
	src, dst := t.TempDir(), t.TempDir()
	if err := unix.Mount("tmpfs", dst, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=32m,mode=0700"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(dst, 0); err != nil {
			t.Errorf("unmount owned filesystem: %v", err)
		}
	}()
	fs := rootfs.NewFilesystem()
	roots, release, err := fs.LockRoots(t.Context(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a, err := roots[src].Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := roots[dst].Identity(t.Context())
	if err != nil || a.Device == b.Device {
		t.Fatalf("not two physical filesystems: %+v %+v %v", a, b, err)
	}
	body := bytes.Repeat([]byte("cross-device media"), 128*1024)
	for _, action := range []string{"copy", "move"} {
		name := action + ".mkv"
		if err := os.WriteFile(filepath.Join(src, name), body, 0600); err != nil {
			t.Fatal(err)
		}
		id, err := roots[src].Inspect(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := fs.InspectContent(t.Context(), roots[src], name, id)
		if err != nil {
			t.Fatal(err)
		}
		if action == "copy" {
			if err := fs.Hardlink(t.Context(), roots[src], name, roots[dst], "hardlink.mkv", id); !errors.Is(err, unix.EXDEV) {
				t.Fatalf("physical EXDEV proof: %v", err)
			}
			supported, err := fs.ProbeReflink(t.Context(), roots[src], name, roots[dst], id, uuid.New())
			if err != nil || supported {
				t.Fatalf("cross-device clone capability: %v %v", supported, err)
			}
			if _, err := fs.TransferVerified(t.Context(), roots[src], name, roots[dst], "clone.mkv", proof, "reflink", uuid.New()); !errors.Is(err, ports.ErrFileUnsupported) {
				t.Fatalf("cross-device clone: %v", err)
			}
		}
		key := uuid.New()
		result, err := fs.TransferVerified(t.Context(), roots[src], name, roots[dst], name, proof, action, key)
		if err != nil || result.Target.SHA256 != sha256.Sum256(body) || result.Target.File.Device != b.Device || result.SourceRemoved != (action == "move") {
			t.Fatalf("cross-device %s: %+v %v", action, result, err)
		}
		again, err := fs.TransferVerified(t.Context(), roots[src], name, roots[dst], name, proof, action, key)
		if err != nil || again != result {
			t.Fatalf("cross-device postcondition: %+v %v", again, err)
		}
	}
	file, err := os.Create(filepath.Join(src, "large.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(64 * 1024 * 1024); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := roots[src].Inspect(t.Context(), "large.mkv")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := fs.InspectContent(t.Context(), roots[src], "large.mkv", id)
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	if _, err := fs.TransferVerified(t.Context(), roots[src], "large.mkv", roots[dst], "large.mkv", proof, "copy", key); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("disk-full failure: %v", err)
	}
	if _, err := roots[dst].Inspect(t.Context(), "large.mkv"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial data published: %v", err)
	}
	if now, err := roots[src].Inspect(t.Context(), "large.mkv"); err != nil || now != id {
		t.Fatalf("disk-full changed source: %+v %v", now, err)
	}
	if err := unix.Mount("tmpfs", dst, "tmpfs", unix.MS_REMOUNT|unix.MS_NODEV|unix.MS_NOSUID, "size=160m,mode=0700"); err != nil {
		t.Fatal(err)
	}
	result, err := fs.TransferVerified(t.Context(), roots[src], "large.mkv", roots[dst], "large.mkv", proof, "copy", key)
	if err != nil || result.Target.SHA256 != proof.SHA256 || result.Target.File.Size != id.Size {
		t.Fatalf("restore capacity and recover: %+v %v", result, err)
	}
}
