//go:build linux

package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"golang.org/x/sys/unix"
)

func TestVerifiedActionCrossDevicePG(t *testing.T) {
	if os.Getenv("PULSE_VERIFIED_PG_PRIVATE_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		arguments := []string{"--user", "--map-root-user", "--mount", "--propagation", "private"}
		// Parent-namespace root already has mount capabilities and must retain access
		// to the external private PG socket, whose owner is outside a new user namespace.
		if os.Geteuid() == 0 {
			arguments = []string{"--mount", "--propagation", "private"}
		}
		arguments = append(arguments, executable, "-test.run=^TestVerifiedActionCrossDevicePG$", "-test.v")
		command := exec.CommandContext(ctx, "unshare", arguments...)
		command.Env = append(os.Environ(), "PULSE_VERIFIED_PG_PRIVATE_CHILD=1")
		command.WaitDelay = time.Second
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("private PG/filesystem execution: %v\n%s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		return
	}
	mount := func(t *testing.T, f *safetyFixture) {
		if err := unix.Mount("tmpfs", f.target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID, "size=4m,mode=0700"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := unix.Unmount(f.target, 0); err != nil {
				t.Errorf("unmount owned root: %v", err)
			}
		})
		if err := os.WriteFile(filepath.Join(f.target, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f, plan, step, _ := verifiedFixture(t, action, func(f *safetyFixture) { mount(t, f) })
			func() {
				roots, release, err := f.svc.files.LockRoots(t.Context(), f.source, f.target)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				target, err := roots[f.target].Identity(t.Context())
				if err != nil || target.Device == step.SourceDevice.Int64 {
					t.Fatal("not distinct filesystems", err)
				}
				if err = f.svc.files.Hardlink(t.Context(), roots[f.source], step.SourceRel.String, roots[f.target], "must-not-link.mkv", frozen(step)); !errors.Is(err, unix.EXDEV) {
					t.Fatal("missing actual EXDEV", err)
				}
			}()
			if err := f.svc.ExecutePlan(t.Context(), plan.ID); err != nil {
				t.Fatal(err)
			}
			checkVerifiedImport(t, f, plan, step, action)
		})
	}
	t.Run("full-disk-resumes-original-step", func(t *testing.T) {
		const size = 8 * 1024 * 1024
		f, plan, step, _ := verifiedFixture(t, "copy", func(f *safetyFixture) {
			mount(t, f)
			file, err := os.OpenFile(f.sourcePath, os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				t.Fatal(err)
			}
			err = file.Truncate(size)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		})
		ctx := t.Context()
		if err := f.svc.ExecutePlan(ctx, plan.ID); !errors.Is(err, unix.ENOSPC) {
			t.Fatal("missing actual ENOSPC", err)
		}
		f.noImport(t)
		if _, err := os.Stat(step.TargetPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("partial file published", err)
		}
		raw, err := f.q.GetVerifiedTransferContent(ctx, step.ID)
		if err != nil {
			t.Fatal("content was not sealed before copying", err)
		}
		var proof ports.TransferContent
		if err = json.Unmarshal(raw, &proof); err != nil {
			t.Fatal(err)
		}
		if err = unix.Mount("tmpfs", f.target, "tmpfs", unix.MS_REMOUNT|unix.MS_NODEV|unix.MS_NOSUID, "size=32m"); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", plan.ID); err != nil {
			t.Fatal(err)
		}
		if err = f.svc.ExecutePlan(ctx, plan.ID); err != nil {
			t.Fatal("original-step restart", err)
		}
		f.oneImport(t, plan.ID)
		file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: step.TargetRel.String})
		if err != nil || file.SizeBytes != size {
			t.Fatal("complete file not indexed", err)
		}
		roots, release, err := f.svc.files.LockRoots(ctx, f.source, f.target)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		result, err := f.svc.files.(ports.VerifiedTransferFilesystem).ObserveTransfer(ctx, roots[f.source], step.SourceRel.String, roots[f.target], step.TargetRel.String, proof, "copy", step.ID)
		if err != nil || result.Target.SHA256 != proof.SHA256 || result.Target.File.Size != size {
			t.Fatal("full restored content invalid", err)
		}
	})
}
