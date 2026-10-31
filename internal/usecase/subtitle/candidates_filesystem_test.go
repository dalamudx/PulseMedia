//go:build linux

package subtitle

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCandidateNativeFilesystemNoOverwriteAndSourceIdentity(t *testing.T) {
	for _, scenario := range []string{"create", "preserve", "symlink", "changed-source"} {
		t.Run(scenario, func(t *testing.T) {
			_, fake, _, _ := fixture()
			rootPath := t.TempDir()
			if e := os.Mkdir(filepath.Join(rootPath, "unexpected layout"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("marker"), 0600); e != nil {
				t.Fatal(e)
			}
			source := filepath.Join(rootPath, filepath.FromSlash(fake.snapshot.FileRelative))
			if e := os.WriteFile(source, bytes.Repeat([]byte{7}, 32768), 0600); e != nil {
				t.Fatal(e)
			}
			decoderProbe := scenario == "create" && os.Getenv("PULSE_MP_SUBTITLE_DECODER") == "1"
			runDecoder := func(args ...string) []byte {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				data, e := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
				if e != nil {
					t.Fatalf("pinned ffmpeg: %v: %s", e, data)
				}
				return data
			}
			if decoderProbe {
				runDecoder("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:duration=3:rate=24", "-c:v", "mpeg4", "-y", source)
			}
			files := rootfs.NewFilesystem()
			root, e := files.OpenRoot(t.Context(), rootPath)
			if e != nil {
				t.Fatal(e)
			}
			rootID, e := root.Identity(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			fileID, e := root.Inspect(t.Context(), fake.snapshot.FileRelative)
			root.Close()
			if e != nil {
				t.Fatal(e)
			}
			fake.snapshot.RootPath = rootPath
			fake.snapshot.RootIdentity = rootID
			fake.snapshot.FileIdentity = fileID
			destination := filepath.Join(rootPath, "unexpected layout", "actual-video.zh-cn.default.srt")
			original := []byte("user-owned subtitles\n")
			switch scenario {
			case "preserve":
				if e = os.WriteFile(destination, original, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.srt")
				if e = os.WriteFile(outside, original, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(outside, destination); e != nil {
					t.Fatal(e)
				}
			case "changed-source":
				if e = os.Remove(source); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(source, bytes.Repeat([]byte{8}, 49152), 0600); e != nil {
					t.Fatal(e)
				}
			}
			repo := &candidateFakeRepository{base: fake}
			service := NewCandidateService(repo, files, nil)
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
			out, e := service.Confirm(t.Context(), actor, fake.snapshot.Source.FileID, uuid.New(), 1)
			if scenario == "symlink" || scenario == "changed-source" {
				if e == nil || repo.completed != nil {
					t.Fatal("unsafe file accepted", out, e)
				}
				if scenario == "changed-source" {
					if _, e = os.Lstat(destination); !os.IsNotExist(e) {
						t.Fatal("stale source created subtitle")
					}
				} else {
					data, read := os.ReadFile(destination)
					if read != nil || !bytes.Equal(data, original) {
						t.Fatal("symlink target changed", read)
					}
				}
				return
			}
			if e != nil {
				t.Fatal(out, e)
			}
			data, e := os.ReadFile(destination)
			if e != nil {
				t.Fatal(e)
			}
			expected := []byte(chineseSRT)
			if scenario == "preserve" {
				expected = original
			}
			if !bytes.Equal(data, expected) {
				t.Fatal("unexpected published bytes")
			}
			info, e := os.Stat(destination)
			if e != nil {
				t.Fatal(e)
			}
			if decoderProbe {
				plain := runDecoder("-v", "error", "-i", source, "-ss", "1.5", "-map", "0:v:0", "-frames:v", "1", "-f", "framemd5", "-")
				rendered := runDecoder("-v", "error", "-i", source, "-ss", "1.5", "-map", "0:v:0", "-vf", "subtitles=filename='"+destination+"'", "-frames:v", "1", "-f", "framemd5", "-")
				if bytes.Equal(plain, rendered) {
					t.Fatal("published Chinese SRT did not render on the video frame")
				}
				t.Log("published Chinese SRT decoded and changed the rendered video frame")
			}
			if scenario == "create" && info.Mode().Perm() != 0644 {
				t.Fatal("subtitle mode", info.Mode())
			}
		})
	}
}
