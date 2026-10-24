//go:build linux

package rootfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

type sidecarFixture struct {
	root *Root
	path string
	req  SidecarRequest
}

func newSidecarFixture(t *testing.T) sidecarFixture {
	t.Helper()
	path, err := os.MkdirTemp(os.TempDir(), "pulse-sidecar-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	if err := os.MkdirAll(filepath.Join(path, "Show/Season"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Show/Season/video.mkv"), []byte("original seeding bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	roots, release, err := LockRoots(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	r := roots[path]
	id, err := r.Inspect("Show/Season/video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := walk(r.fd, []string{"Show", "Season"}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sidecarStat(dir)
	if e := unix.Close(dir); e != nil {
		t.Fatal(e)
	}
	if err != nil {
		t.Fatal(err)
	}
	return sidecarFixture{r, path, SidecarRequest{SourceRelative: "Show/Season/video.mkv", SourceIdentity: id, DestinationDirectory: "Show/Season", DestinationIdentity: sidecarDirID(st), Name: "movie.nfo", Content: []byte("<movie>complete metadata</movie>")}}
}

func (f sidecarFixture) final() string {
	return filepath.Join(f.path, f.req.DestinationDirectory, f.req.Name)
}
func (f sidecarFixture) assertSource(t *testing.T) {
	t.Helper()
	id, err := f.root.Inspect(f.req.SourceRelative)
	if err != nil || id != f.req.SourceIdentity {
		t.Fatalf("source identity changed: %+v %v", id, err)
	}
	body, err := os.ReadFile(filepath.Join(f.path, f.req.SourceRelative))
	if err != nil || string(body) != "original seeding bytes" {
		t.Fatalf("source changed: %q %v", body, err)
	}
}
func (f sidecarFixture) assertNoStages(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(f.path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".pulsemedia-sidecar-") {
			t.Errorf("staging leak: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func (f sidecarFixture) destination(t *testing.T, path string) sidecarFixture {
	t.Helper()
	p, err := sidecarPath(path, true)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := walk(f.root.fd, p, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sidecarStat(fd)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	f.req.DestinationDirectory, f.req.DestinationIdentity = path, sidecarDirID(st)
	return f
}

func TestSidecarPublish(t *testing.T) {
	for _, name := range []string{"movie.nfo", "tvshow.nfo", "poster.jpg", "logo.png", "clearart.png", "banner.jpg", "disc.png"} {
		t.Run(name, func(t *testing.T) {
			f := newSidecarFixture(t)
			f.req.Name = name
			if name == "tvshow.nfo" {
				f = f.destination(t, "Show")
			}
			if name == "poster.jpg" {
				f = f.destination(t, ".")
				f.req.Content = []byte{0xff, 0xd8, 0, 1, 0xff, 0xd9}
			}
			out, err := f.root.PublishSidecar(t.Context(), f.req)
			if err != nil || out.Disposition != SidecarCreated || !out.Published {
				t.Fatalf("publish: %+v %v", out, err)
			}
			body, err := os.ReadFile(f.final())
			if err != nil || !bytes.Equal(body, f.req.Content) {
				t.Fatalf("body: %x %v", body, err)
			}
			st, err := os.Stat(f.final())
			if err != nil || st.Mode().Perm() != 0644 {
				t.Fatalf("metadata is not readable by the media-server UID: %v %v", st, err)
			}
			id, err := f.root.Inspect(filepath.Join(f.req.DestinationDirectory, name))
			if err != nil || id != out.Identity {
				t.Fatalf("final identity: %+v %v", id, err)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSidecarExistingPreserved(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := newSidecarFixture(t)
			var err error
			switch kind {
			case "regular":
				err = os.WriteFile(f.final(), []byte("user metadata"), 0600)
			case "symlink":
				err = os.Symlink("video.mkv", f.final())
			case "directory":
				err = os.Mkdir(f.final(), 0700)
			case "fifo":
				err = unix.Mkfifo(f.final(), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(f.final())
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.root.PublishSidecar(t.Context(), f.req)
			want := SidecarUnsafeExisting
			if kind == "regular" {
				want = SidecarPreservedRegular
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatalf("unsafe error: %v", err)
			}
			if out.Disposition != want || out.Published {
				t.Fatalf("existing: %+v %v", out, err)
			}
			after, err := os.Lstat(f.final())
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing replaced: %v", err)
			}
			if kind == "regular" {
				body, err := os.ReadFile(f.final())
				if err != nil || string(body) != "user metadata" {
					t.Fatalf("existing modified: %q %v", body, err)
				}
			}
			if kind == "symlink" {
				target, err := os.Readlink(f.final())
				if err != nil || target != "video.mkv" {
					t.Fatalf("symlink modified: %q %v", target, err)
				}
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSidecarInvalidRequests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*sidecarFixture)
	}{
		{"empty body", func(f *sidecarFixture) { f.req.Content = nil }},
		{"too large", func(f *sidecarFixture) { f.req.Content = make([]byte, MaxSidecarBytes+1) }},
		{"short name", func(f *sidecarFixture) { f.req.Name = "m" }},
		{"invalid name", func(f *sidecarFixture) { f.req.Name = "episode.nfo" }},
		{"name escape", func(f *sidecarFixture) { f.req.Name = "../movie.nfo" }},
		{"absolute source", func(f *sidecarFixture) { f.req.SourceRelative = "/Show/Season/video.mkv" }},
		{"source escape", func(f *sidecarFixture) { f.req.SourceRelative = "Show/../Show/Season/video.mkv" }},
		{"empty source", func(f *sidecarFixture) { f.req.SourceRelative = "" }},
		{"dot source", func(f *sidecarFixture) { f.req.SourceRelative = "." }},
		{"nul source", func(f *sidecarFixture) { f.req.SourceRelative += "\x00" }},
		{"absolute destination", func(f *sidecarFixture) { f.req.DestinationDirectory = "/Show" }},
		{"destination escape", func(f *sidecarFixture) { f.req.DestinationDirectory = "Show/../Show" }},
		{"empty destination", func(f *sidecarFixture) { f.req.DestinationDirectory = "" }},
		{"unrelated destination", func(f *sidecarFixture) { f.req.DestinationDirectory = "Other" }},
		{"source as destination", func(f *sidecarFixture) { f.req.DestinationDirectory = f.req.SourceRelative }},
		{"wrong source device", func(f *sidecarFixture) { f.req.SourceIdentity.Device++ }},
		{"wrong source inode", func(f *sidecarFixture) { f.req.SourceIdentity.Inode++ }},
		{"wrong source size", func(f *sidecarFixture) { f.req.SourceIdentity.Size++ }},
		{"wrong source mtime", func(f *sidecarFixture) { f.req.SourceIdentity.Mtime++ }},
		{"wrong directory device", func(f *sidecarFixture) { f.req.DestinationIdentity.Device++ }},
		{"wrong directory inode", func(f *sidecarFixture) { f.req.DestinationIdentity.Inode++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSidecarFixture(t)
			tc.change(&f)
			out, err := f.root.PublishSidecar(t.Context(), f.req)
			if err == nil || out.Published || out.Disposition != SidecarNotPublished {
				t.Fatalf("invalid accepted: %+v %v", out, err)
			}
			// There are exactly the three original entries: Show, Season and video.mkv.
			count := 0
			if err := filepath.WalkDir(f.path, func(_ string, _ os.DirEntry, err error) error { count++; return err }); err != nil {
				t.Fatal(err)
			}
			if count != 4 {
				t.Fatalf("invalid request created entries: %d", count)
			}
		})
	}
}

func TestSidecarRejectsSymlinkAndNonregularSource(t *testing.T) {
	for _, kind := range []string{"symlink source", "directory source", "fifo source", "symlink parent"} {
		t.Run(kind, func(t *testing.T) {
			f := newSidecarFixture(t)
			source := filepath.Join(f.path, f.req.SourceRelative)
			if kind == "symlink parent" {
				if err := os.Rename(filepath.Join(f.path, "Show/Season"), filepath.Join(f.path, "Show/Actual")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("Actual", filepath.Join(f.path, "Show/Season")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink source":
					err = os.Symlink("missing", source)
				case "directory source":
					err = os.Mkdir(source, 0700)
				case "fifo source":
					err = unix.Mkfifo(source, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.root.PublishSidecar(t.Context(), f.req)
			if err == nil || out.Published {
				t.Fatalf("unsafe source: %+v %v", out, err)
			}
			if _, err := os.Lstat(f.final()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created final: %v", err)
			}
			f.assertNoStages(t)
		})
	}
}

func TestSidecarConcurrentNoReplace(t *testing.T) {
	f := newSidecarFixture(t)
	const writers = 16
	var wg sync.WaitGroup
	results := make(chan SidecarResult, writers)
	bodies := make([][]byte, writers)
	for i := range writers {
		bodies[i] = bytes.Repeat([]byte{byte(i)}, 128*1024)
		wg.Go(func() {
			req := f.req
			req.Content = bodies[i]
			out, err := f.root.PublishSidecar(t.Context(), req)
			if err != nil {
				t.Errorf("writer: %+v %v", out, err)
			}
			results <- out
		})
	}
	wg.Wait()
	close(results)
	created := 0
	for out := range results {
		if out.Disposition == SidecarCreated && out.Published {
			created++
		} else if out.Disposition != SidecarPreservedRegular || out.Published {
			t.Errorf("unexpected: %+v", out)
		}
	}
	if created != 1 {
		t.Fatalf("created %d times", created)
	}
	body, err := os.ReadFile(f.final())
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, candidate := range bodies {
		matched = matched || bytes.Equal(body, candidate)
	}
	if !matched {
		t.Fatal("final is partial or mixed")
	}
	f.assertSource(t)
	f.assertNoStages(t)
}

func TestSidecarWriteFailuresAndCancellation(t *testing.T) {
	for _, point := range []string{"before call", "staged", "during write", "short write", "write error", "file sync", "before_publish", "published", "directory sync"} {
		t.Run(point, func(t *testing.T) {
			f := newSidecarFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ops := sidecarOps{checkpoint: func(at string) {
				if at == point {
					cancel()
				}
			}}
			wantErr := error(context.Canceled)
			switch point {
			case "before call":
				cancel()
			case "during write":
				ops.write = func(fd int, p []byte) (int, error) { n, err := unix.Write(fd, p[:1]); cancel(); return n, err }
			case "short write":
				ops.write = func(int, []byte) (int, error) { return 0, nil }
				wantErr = io.ErrShortWrite
			case "write error":
				ops.write = func(fd int, p []byte) (int, error) { n, _ := unix.Write(fd, p[:1]); return n, unix.ENOSPC }
				wantErr = unix.ENOSPC
			case "file sync":
				ops.sync = func(int) error { return unix.EIO }
				wantErr = unix.EIO
			case "directory sync":
				calls := 0
				ops.sync = func(fd int) error {
					calls++
					if calls == 2 {
						return unix.EIO
					}
					return unix.Fsync(fd)
				}
				wantErr = unix.EIO
			}
			out, err := f.root.publishSidecar(ctx, f.req, ops)
			if !errors.Is(err, wantErr) {
				t.Fatalf("want %v: %+v %v", wantErr, out, err)
			}
			published := point == "published" || point == "directory sync"
			if out.Published != published {
				t.Fatalf("wrong physical outcome: %+v", out)
			}
			if published {
				if out.Disposition != SidecarUncertain {
					t.Fatalf("claimed success: %+v", out)
				}
				body, err := os.ReadFile(f.final())
				if err != nil || !bytes.Equal(body, f.req.Content) {
					t.Fatalf("uncertain final lost/partial: %q %v", body, err)
				}
			} else if _, err := os.Lstat(f.final()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial final: %v", err)
			}
			// Cancellation has not closed the root or released the physical flock.
			if _, err := f.root.Identity(); err != nil {
				t.Fatalf("root closed: %v", err)
			}
			contender, err := Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer contender.Close()
			if err := unix.Flock(contender.fd, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
				t.Fatalf("physical lock lost: %v", err)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSidecarRevalidatesBindings(t *testing.T) {
	for _, point := range []string{"before_publish", "published"} {
		for _, mutation := range []string{"parent rename", "ancestor rename", "source replace", "source modify"} {
			t.Run(point+"/"+mutation, func(t *testing.T) {
				f := newSidecarFixture(t)
				originalFinal := f.final()
				ops := sidecarOps{checkpoint: func(at string) {
					if at != point {
						return
					}
					switch mutation {
					case "parent rename", "ancestor rename":
						rel := "Show/Season"
						if mutation == "ancestor rename" {
							rel = "Show"
						}
						old := filepath.Join(f.path, rel)
						if err := os.Rename(old, old+"-old"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(filepath.Base(old)+"-old", old); err != nil {
							t.Fatal(err)
						}
						if mutation == "parent rename" {
							originalFinal = filepath.Join(f.path, "Show/Season-old/movie.nfo")
						} else {
							originalFinal = filepath.Join(f.path, "Show-old/Season/movie.nfo")
						}
					case "source replace":
						path := filepath.Join(f.path, f.req.SourceRelative)
						if err := os.Rename(path, path+"-old"); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("foreign source"), 0600); err != nil {
							t.Fatal(err)
						}
					case "source modify":
						if err := os.WriteFile(filepath.Join(f.path, f.req.SourceRelative), []byte("changed"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}}
				out, err := f.root.publishSidecar(t.Context(), f.req, ops)
				if err == nil {
					t.Fatalf("changed binding accepted: %+v", out)
				}
				if point == "published" {
					if !out.Published || out.Disposition != SidecarUncertain {
						t.Fatalf("lost actual publication: %+v", out)
					}
					body, err := os.ReadFile(originalFinal)
					if err != nil || !bytes.Equal(body, f.req.Content) {
						t.Fatalf("actual final deleted: %v", err)
					}
				} else {
					if out.Published {
						t.Fatalf("published after rename: %+v", out)
					}
					if _, err := os.Lstat(originalFinal); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("created after rename: %v", err)
					}
				}
				f.assertNoStages(t)
			})
		}
	}
}

func TestSidecarFinalAppearsBeforePublication(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newSidecarFixture(t)
			ops := sidecarOps{checkpoint: func(point string) {
				if point != "before_publish" {
					return
				}
				var err error
				switch kind {
				case "regular":
					err = os.WriteFile(f.final(), []byte("late user file"), 0600)
				case "symlink":
					err = os.Symlink("video.mkv", f.final())
				case "directory":
					err = os.Mkdir(f.final(), 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			out, err := f.root.publishSidecar(t.Context(), f.req, ops)
			if out.Published {
				t.Fatalf("overwrote late final: %+v", out)
			}
			st, statErr := os.Lstat(f.final())
			if statErr != nil {
				t.Fatal(statErr)
			}
			if kind == "regular" {
				if err != nil || out.Disposition != SidecarPreservedRegular || !st.Mode().IsRegular() {
					t.Fatalf("late regular: %+v %v", out, err)
				}
				body, err := os.ReadFile(f.final())
				if err != nil || string(body) != "late user file" {
					t.Fatalf("late regular modified: %q %v", body, err)
				}
			} else if !errors.Is(err, ErrConflict) || out.Disposition != SidecarUnsafeExisting || (kind == "directory" && !st.IsDir()) || (kind == "symlink" && st.Mode()&os.ModeSymlink == 0) {
				t.Fatalf("late unsafe entry: %+v %v", out, err)
			}
			f.assertSource(t)
			f.assertNoStages(t)
		})
	}
}

func TestSidecarShortWritesComplete(t *testing.T) {
	f := newSidecarFixture(t)
	f.req.Content = bytes.Repeat([]byte("complete bounded body"), 8192)
	writes := 0
	ops := sidecarOps{write: func(fd int, p []byte) (int, error) {
		writes++
		return unix.Write(fd, p[:min(137, len(p))])
	}}
	out, err := f.root.publishSidecar(t.Context(), f.req, ops)
	if err != nil || out.Disposition != SidecarCreated || writes < 2 {
		t.Fatalf("short writes: %+v %v (%d)", out, err, writes)
	}
	body, err := os.ReadFile(f.final())
	if err != nil || !bytes.Equal(body, f.req.Content) {
		t.Fatalf("incomplete short writes: %v", err)
	}
	f.assertSource(t)
	f.assertNoStages(t)
}

func TestSidecarRootRenameIsExternalValidation(t *testing.T) {
	f := newSidecarFixture(t)
	// A root FD remains a capability to the original directory. The caller must
	// reopen/revalidate its configured absolute namespace before accepting output.
	old := filepath.Join(f.path, "pinned")
	container := filepath.Join(f.path, "container")
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.path, "Show"), old); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, filepath.Join(container, "Show")); err != nil {
		t.Fatal(err)
	}
	// Hold the physical slot for the capability being renamed.
	roots, release, err := LockRoots(t.Context(), container)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r := roots[container]
	if err := os.Rename(container, container+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	out, err := r.PublishSidecar(t.Context(), f.req)
	if err != nil || !out.Published {
		t.Fatalf("pinned root: %+v %v", out, err)
	}
	if _, err := os.Lstat(filepath.Join(container, "movie.nfo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root touched: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(container+"-old", "Show/Season/movie.nfo")); err != nil || !bytes.Equal(body, f.req.Content) {
		t.Fatalf("original root output: %v", err)
	}
	f.assertNoStages(t)
}

func TestSidecarStageReplacementPreserved(t *testing.T) {
	f := newSidecarFixture(t)
	var reused string
	ops := sidecarOps{checkpoint: func(point string) {
		if point != "before_publish" {
			return
		}
		entries, err := os.ReadDir(filepath.Dir(f.final()))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".pulsemedia-sidecar-") {
				reused = filepath.Join(filepath.Dir(f.final()), entry.Name())
			}
		}
		if reused == "" {
			t.Fatal("missing stage")
		}
		if err := os.Rename(reused, reused+"-owned"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reused, []byte("foreign stage"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	out, err := f.root.publishSidecar(t.Context(), f.req, ops)
	if !errors.Is(err, ErrIdentity) || !out.Published || out.Disposition != SidecarUncertain {
		t.Fatalf("stage replacement: %+v %v", out, err)
	}
	body, err := os.ReadFile(reused)
	if err != nil || string(body) != "foreign stage" {
		t.Fatalf("reused stage removed/modified: %q %v", body, err)
	}
	body, err = os.ReadFile(f.final())
	if err != nil || !bytes.Equal(body, f.req.Content) {
		t.Fatalf("published foreign inode: %q %v", body, err)
	}
	f.assertSource(t)
	// The renamed original and replacement belong to this test fixture and are
	// removed only by its owner; the publisher deliberately leaves both alone.
}
