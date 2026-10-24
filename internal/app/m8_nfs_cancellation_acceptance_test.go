//go:build linux

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"golang.org/x/sys/unix"
)

func TestM8NFSOrganizationBlockedCancellation(t *testing.T) {
	if os.Getenv("PULSE_M8_NFS_WATCH_ACCEPTANCE") != "1" {
		t.Skip("explicit owned kernel NFS required")
	}
	m8UntrackedOrganizationRequired(t)
	work, mount, cid, parent := os.Getenv("PULSE_M4_NFS_WORK"), os.Getenv("PULSE_M4_NFS_ROOT"), os.Getenv("PULSE_M4_NFS_CID"), os.Getenv("PULSE_M4_NFS_PARENT_MNT")
	if !strings.HasPrefix(work, "/data/temp/pulse-m4-nfs.") || mount != filepath.Join(work, "client") || len(cid) != 64 || !strings.HasPrefix(parent, "/proc/") || !strings.HasSuffix(parent, "/ns/mnt") {
		t.Fatal("owned NFS namespace required")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(mount, &stat); err != nil || stat.Type != unix.NFS_SUPER_MAGIC {
		t.Fatal("real kernel NFS required", err)
	}
	command := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 15*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "nsenter", append([]string{"--mount=" + parent, "--", "podman"}, args...)...).CombinedOutput()
	}
	owner, err := command("inspect", "-f", `{{index .Config.Labels "io.pulsemedia.nfs-owner"}}`, cid)
	if err != nil || strings.TrimSpace(string(owner)) != work {
		t.Fatal("NFS server ownership mismatch")
	}
	directories := &m8OrganizationDirectories{}
	directories.source, err = os.MkdirTemp(mount, "m8-blocked-source-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directories.source); err != nil {
			t.Error("remove owned source", err)
		}
	})
	directories.target, err = os.MkdirTemp(mount, "m8-blocked-target-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directories.target); err != nil {
			t.Error("remove owned target", err)
		}
	})
	directories.remoteSource = filepath.Join(work, "export", filepath.Base(directories.source))
	fixture := m8UntrackedOrganizationWithDirectories(t, "hardlink", directories)
	history := fixture.confirmationHistory(t)
	receipt, raw := fixture.queue(t)
	_, release, err := rootfs.NewFilesystem().LockRoots(t.Context(), directories.source, directories.target)
	if err != nil {
		t.Fatal("hold real organization physical roots", err)
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	fixture.start(t)
	fixture.wait(t, "original job claimed behind physical root slot", func() bool {
		return fixture.predicate(t, `SELECT state='running' AND attempts=1 AND lease_token=1 FROM job WHERE id=$1`, receipt.Operation.JobID)
	})
	watchdog := exec.Command("bash", "-c", `sleep 7; state=$(nsenter --mount="$1" -- podman inspect -f '{{.State.Status}}' "$2") || exit 1; case "$state" in paused) exec nsenter --mount="$1" -- podman unpause "$2";; running) exit 0;; *) exit 1;; esac`, "watchdog", parent, cid)
	if err := watchdog.Start(); err != nil {
		t.Fatal("arm independent NFS recovery watchdog", err)
	}
	defer func() {
		_, _ = command("unpause", cid)
		if err := watchdog.Wait(); err != nil {
			t.Error("NFS watchdog join failed")
		}
	}()
	if _, err := command("pause", cid); err != nil {
		t.Fatal("pause owned NFS server")
	}
	release()
	held = false
	// The worker must cross a fresh namespace check before any file permission.
	time.Sleep(250 * time.Millisecond)
	fixture.cancel()
	select {
	case err := <-fixture.done:
		fixture.done <- err
		t.Fatal("App returned while actual NFS I/O remained blocked")
	case <-time.After(300 * time.Millisecond):
	}
	fixture.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='running' AND attempts=1 AND lease_token=1`, 1, receipt.Operation.JobID)
	fixture.count(t, `SELECT count(*) FROM untracked_organization_permit`, 0)
	fixture.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
	fixture.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
	fixture.count(t, `SELECT count(*) FROM library_file_unit`, 0)
	if _, err := command("unpause", cid); err != nil {
		t.Fatal("resume owned NFS server")
	}
	fixture.stop(t)
	fixture.count(t, `SELECT count(*) FROM untracked_organization_permit`, 0)
	fixture.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
	if _, err := os.Lstat(filepath.Join(fixture.target, fixture.confirmation.Material.Input.TargetRelativePath)); !os.IsNotExist(err) {
		t.Fatal("cancelled NFS call published a target")
	}
	if got := m8UntrackedOrganizationIdentity(t, fixture.source, fixture.sourceRelative); got != fixture.sourceIdentity {
		t.Fatal("cancelled NFS call changed source")
	}
	fixture.start(t)
	fixture.completed(t, receipt)
	fixture.stop(t)
	fixture.verifyImport(t, receipt, "hardlink")
	fixture.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded' AND attempts=2 AND lease_token=2`, 1, receipt.Operation.JobID)
	fixture.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND job_token=2`, 1, receipt.Operation.ID)
	fixture.recoverOffline(t, receipt, raw, "completed")
	if history != fixture.confirmationHistory(t) {
		t.Fatal("NFS cancellation recovery changed original confirmation")
	}
	t.Log("M8_NFS_BLOCKED_ORGANIZATION_PASS server_pause=REAL cancel_waits_for_actual_io=true original_job_not_replayed=true permit_before_io_return=0 facts_before_io_return=0 target_before_restart=ABSENT retry_token2=true same_original_operation=true hardlink=IMPORTED original_receipts=true downloader_calls=0")
}
