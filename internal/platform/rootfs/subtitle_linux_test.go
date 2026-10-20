//go:build linux

package rootfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

func subtitleFixture(t *testing.T) sidecarFixture {
	t.Helper()
	f := newSidecarFixture(t)
	f.req.Name = "video.zh-cn.default.srt"
	f.req.Content = []byte("1\n00:00:00,000 --> 00:00:01,000\n中文字幕\n")
	return f
}

func hashFixture(t *testing.T, size int64) sidecarFixture {
	t.Helper()
	f := subtitleFixture(t)
	file, err := os.OpenFile(filepath.Join(f.path, f.req.SourceRelative), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	f.req.SourceIdentity, err = f.root.Inspect(f.req.SourceRelative)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSubtitleHashExact(t *testing.T) {
	f := hashFixture(t, 49157)
	data := make([]byte, 49157)
	for i := range data {
		data[i] = byte((i*71 + i/4096*13 + i/37) % 251)
	}
	if err := os.WriteFile(filepath.Join(f.path, f.req.SourceRelative), data, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := f.root.Inspect(f.req.SourceRelative)
	if err != nil {
		t.Fatal(err)
	}
	// Independently fixed offsets for a length not divisible by three.
	expected := make([]string, 0, 4)
	for _, offset := range []int{4096, 32770, 16385, 40965} {
		expected = append(expected, fmt.Sprintf("%x", md5.Sum(data[offset:offset+4096])))
	}
	got, err := NewFilesystem().HashSubtitleVideo(t.Context(), &capability{f.root}, f.req.SourceRelative, id)
	if err != nil || got.Hash != strings.Join(expected, ";") || got.SizeBytes != int64(len(data)) {
		t.Fatalf("hash: %+v %v want %v", got, err, expected)
	}
}

func TestSubtitleHashSparseBounded(t *testing.T) {
	f := hashFixture(t, 1<<40)
	calls, total := 0, 0
	got, err := f.root.hashSubtitleVideo(t.Context(), f.req.SourceRelative, f.req.SourceIdentity, func(fd int, p []byte, offset int64) (int, error) {
		calls++
		total += len(p)
		return unix.Pread(fd, p, offset)
	})
	zero := fmt.Sprintf("%x", md5.Sum(make([]byte, 4096)))
	if err != nil || calls != 4 || total != 16384 || got.SizeBytes != 1<<40 || got.Hash != strings.Join([]string{zero, zero, zero, zero}, ";") {
		t.Fatalf("bounded hash: %+v %v reads=%d bytes=%d", got, err, calls, total)
	}
}

func TestSubtitleHashIdentityFields(t *testing.T) {
	f := hashFixture(t, 16384)
	for _, field := range []string{"device", "inode", "size", "mtime"} {
		t.Run(field, func(t *testing.T) {
			id := f.req.SourceIdentity
			switch field {
			case "device":
				id.Device++
			case "inode":
				id.Inode++
			case "size":
				id.Size++
			case "mtime":
				id.Mtime++
			}
			calls := 0
			out, err := f.root.hashSubtitleVideo(t.Context(), f.req.SourceRelative, id, func(int, []byte, int64) (int, error) { calls++; return 0, nil })
			if !errors.Is(err, ports.ErrFileIdentity) || calls != 0 || out != (ports.SubtitleVideoHash{}) {
				t.Fatalf("identity: %+v %v reads=%d", out, err, calls)
			}
		})
	}
}

func TestSubtitleHashMutation(t *testing.T) {
	for _, change := range []string{"write", "leaf", "parent", "symlink", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := hashFixture(t, 49157)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			out, err := f.root.hashSubtitleVideo(ctx, f.req.SourceRelative, f.req.SourceIdentity, func(fd int, p []byte, offset int64) (int, error) {
				n, e := unix.Pread(fd, p, offset)
				calls++
				if calls == 1 {
					source := filepath.Join(f.path, f.req.SourceRelative)
					switch change {
					case "write":
						if e := os.WriteFile(source, bytes.Repeat([]byte("x"), 49157), 0600); e != nil {
							t.Fatal(e)
						}
						mtime := time.Unix(0, f.req.SourceIdentity.Mtime).Add(time.Second)
						if e := os.Chtimes(source, mtime, mtime); e != nil {
							t.Fatal(e)
						}
					case "leaf", "symlink":
						if e := os.Rename(source, source+".old"); e != nil {
							t.Fatal(e)
						}
						if change == "leaf" {
							if e := os.WriteFile(source, make([]byte, 49157), 0600); e != nil {
								t.Fatal(e)
							}
						} else {
							if e := os.Symlink(source+".old", source); e != nil {
								t.Fatal(e)
							}
						}
					case "parent":
						parent := filepath.Dir(source)
						if e := os.Rename(parent, parent+".old"); e != nil {
							t.Fatal(e)
						}
						if e := os.Mkdir(parent, 0700); e != nil {
							t.Fatal(e)
						}
						if e := os.WriteFile(source, make([]byte, 49157), 0600); e != nil {
							t.Fatal(e)
						}
					case "cancel":
						cancel()
					}
				}
				return n, e
			})
			if err == nil || out != (ports.SubtitleVideoHash{}) {
				t.Fatalf("mutation accepted: %+v %v", out, err)
			}
			if change == "cancel" && (!errors.Is(err, context.Canceled) || calls != 1) {
				t.Fatalf("cancel: %v reads=%d", err, calls)
			}
		})
	}
}

func TestSubtitleHashRejectsUnsafe(t *testing.T) {
	f := hashFixture(t, 16384)
	fs := NewFilesystem()
	for _, rel := range []string{"../video.mkv", "/video.mkv", "Show//Season/video.mkv", "Show/./Season/video.mkv", "Show\\Season/video.mkv", "Show/Season/video.mkv/", "Show/Season/video.txt"} {
		out, err := fs.HashSubtitleVideo(t.Context(), &capability{f.root}, rel, f.req.SourceIdentity)
		if err == nil || out != (ports.SubtitleVideoHash{}) {
			t.Fatalf("unsafe %q: %+v %v", rel, out, err)
		}
	}
	source := filepath.Join(f.path, f.req.SourceRelative)
	if err := os.Symlink(source, filepath.Join(f.path, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.HashSubtitleVideo(t.Context(), &capability{f.root}, "link.mkv", f.req.SourceIdentity); !errors.Is(err, ports.ErrFileSymlink) {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Dir(source), filepath.Join(f.path, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.HashSubtitleVideo(t.Context(), &capability{f.root}, "linked/video.mkv", f.req.SourceIdentity); !errors.Is(err, ports.ErrFileSymlink) {
		t.Fatalf("parent symlink: %v", err)
	}
}

func TestSubtitleHashMinimum(t *testing.T) {
	for _, size := range []int64{0, 16383, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := hashFixture(t, size)
			out, err := NewFilesystem().HashSubtitleVideo(t.Context(), &capability{f.root}, f.req.SourceRelative, f.req.SourceIdentity)
			if size < 16384 {
				if !errors.Is(err, ports.ErrSubtitleVideoTooSmall) || out != (ports.SubtitleVideoHash{}) {
					t.Fatalf("small: %+v %v", out, err)
				}
			} else if err != nil || out.Hash == "" {
				t.Fatalf("minimum: %+v %v", out, err)
			}
		})
	}
}

func TestSubtitleFilesystemCapability(t *testing.T) {
	fs := NewFilesystem()
	f := subtitleFixture(t)
	for _, root := range []ports.TransferRoot{nil, foreignSidecarRoot{}, (*capability)(nil), &capability{}} {
		if _, err := fs.HashSubtitleVideo(t.Context(), root, f.req.SourceRelative, f.req.SourceIdentity); !errors.Is(err, ports.ErrFileIdentity) {
			t.Fatalf("forged hash: %v", err)
		}
		if out, err := fs.PublishSubtitle(t.Context(), root, f.req); !errors.Is(err, ports.ErrFileIdentity) || out.Published {
			t.Fatalf("forged publish: %+v %v", out, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fs.HashSubtitleVideo(ctx, nil, "", ports.FileIdentity{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel hash: %v", err)
	}
	if _, err := fs.PublishSubtitle(ctx, nil, f.req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel publish: %v", err)
	}
}

func TestSubtitlePublishAndRecovery(t *testing.T) {
	for _, suffix := range []string{"mkv", "mp4", "avi", "mov", "m4v", "ts", "m2ts", "webm", "wmv", "mpg", "mpeg", "MKV"} {
		t.Run(suffix, func(t *testing.T) {
			f := subtitleFixture(t)
			rel := "Show/Season/A.movie." + suffix
			if err := os.Rename(filepath.Join(f.path, f.req.SourceRelative), filepath.Join(f.path, rel)); err != nil {
				t.Fatal(err)
			}
			f.req.SourceRelative = rel
			f.req.Name = "A.movie.zh-cn.default.srt"
			var fs ports.MediaSubtitleFilesystem = NewFilesystem()
			root := &capability{f.root}
			out, err := fs.PublishSubtitle(t.Context(), root, f.req)
			if err != nil || out.Disposition != SidecarCreated || !out.Published {
				t.Fatalf("publish: %+v %v", out, err)
			}
			body, err := os.ReadFile(f.final())
			if err != nil || !bytes.Equal(body, f.req.Content) {
				t.Fatalf("bytes: %q %v", body, err)
			}
			st, err := os.Stat(f.final())
			if err != nil || st.Mode().Perm() != 0644 {
				t.Fatalf("mode: %v %v", st, err)
			}
			out, err = fs.PublishSubtitle(t.Context(), root, f.req)
			if err != nil || out.Disposition != SidecarPreservedRegular || out.Published {
				t.Fatalf("recovery: %+v %v", out, err)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSubtitlePublishRestrictions(t *testing.T) {
	for _, change := range []string{"wrong-name", "path-name", "ancestor", "other-parent", "extension", "empty", "large", "source-identity", "destination-identity", "escape", "source-symlink"} {
		t.Run(change, func(t *testing.T) {
			f := subtitleFixture(t)
			switch change {
			case "wrong-name":
				f.req.Name = "video.zh-cn.srt"
			case "path-name":
				f.req.Name = "../video.zh-cn.default.srt"
			case "ancestor":
				f = f.destination(t, "Show")
			case "other-parent":
				f.req.DestinationDirectory = "Show/Other"
			case "extension":
				f.req.SourceRelative = "Show/Season/video.txt"
			case "empty":
				f.req.Content = nil
			case "large":
				f.req.Content = make([]byte, ports.MaxSubtitleBytes+1)
			case "source-identity":
				f.req.SourceIdentity.Mtime++
			case "destination-identity":
				f.req.DestinationIdentity.Inode++
			case "escape":
				f.req.SourceRelative = "Show/Season/../video.mkv"
			case "source-symlink":
				source := filepath.Join(f.path, f.req.SourceRelative)
				if err := os.Rename(source, source+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source+".old", source); err != nil {
					t.Fatal(err)
				}
			}
			out, err := NewFilesystem().PublishSubtitle(t.Context(), &capability{f.root}, f.req)
			if err == nil || out.Published {
				t.Fatalf("invalid publish: %+v %v", out, err)
			}
			f.assertNoStages(t)
		})
	}
	f := subtitleFixture(t)
	if _, err := f.root.PublishSidecar(t.Context(), f.req); !errors.Is(err, ErrSidecarRequest) {
		t.Fatalf("expanded sidecar whitelist: %v", err)
	}
}

func TestSubtitleExistingPreserved(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := subtitleFixture(t)
			var err error
			switch kind {
			case "regular":
				err = os.WriteFile(f.final(), []byte("user subtitles"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(f.path, f.req.SourceRelative), f.final())
			case "directory":
				err = os.Mkdir(f.final(), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(f.final())
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.root.PublishSubtitle(t.Context(), f.req)
			if kind == "regular" {
				if err != nil || out.Disposition != SidecarPreservedRegular {
					t.Fatalf("regular: %+v %v", out, err)
				}
				body, e := os.ReadFile(f.final())
				if e != nil || string(body) != "user subtitles" {
					t.Fatalf("user bytes: %q %v", body, e)
				}
			} else if err == nil || out.Disposition != SidecarUnsafeExisting {
				t.Fatalf("unsafe: %+v %v", out, err)
			}
			after, e := os.Lstat(f.final())
			if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || out.Published {
				t.Fatalf("entry modified: %+v %v", out, e)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSubtitlePublishFaultRecovery(t *testing.T) {
	for _, point := range []string{"staged", "before_publish", "published"} {
		t.Run(point, func(t *testing.T) {
			f := subtitleFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out, err := f.root.publishSidecar(ctx, f.req, sidecarOps{subtitle: true, checkpoint: func(at string) {
				if at == point {
					cancel()
				}
			}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %+v %v", out, err)
			}
			if point == "published" {
				if !out.Published || out.Disposition != SidecarUncertain {
					t.Fatalf("lost publication: %+v", out)
				}
				recovered, e := f.root.PublishSubtitle(t.Context(), f.req)
				if e != nil || recovered.Published || recovered.Disposition != SidecarPreservedRegular {
					t.Fatalf("recovery: %+v %v", recovered, e)
				}
			} else if out.Published {
				t.Fatalf("premature publish: %+v", out)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSubtitlePublishRootParentAndLimit(t *testing.T) {
	f := subtitleFixture(t)
	if err := os.Rename(filepath.Join(f.path, f.req.SourceRelative), filepath.Join(f.path, "video.mkv")); err != nil {
		t.Fatal(err)
	}
	f.req.SourceRelative = "video.mkv"
	f = f.destination(t, ".")
	f.req.Content = bytes.Repeat([]byte("s"), ports.MaxSubtitleBytes)
	out, err := f.root.PublishSubtitle(t.Context(), f.req)
	if err != nil || !out.Published || out.Disposition != SidecarCreated {
		t.Fatalf("root/limit: %+v %v", out, err)
	}
	f.assertSource(t)
	f.assertNoStages(t)
}
