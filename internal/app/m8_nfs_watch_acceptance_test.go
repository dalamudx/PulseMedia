//go:build linux

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func TestM8NFSRemoteWritePeriodicOrganization(t *testing.T) {
	if os.Getenv("PULSE_M8_NFS_WATCH_ACCEPTANCE") != "1" {
		t.Skip("explicit owned kernel NFS required")
	}
	m8UntrackedOrganizationRequired(t)
	work, mount := os.Getenv("PULSE_M4_NFS_WORK"), os.Getenv("PULSE_M4_NFS_ROOT")
	if !strings.HasPrefix(work, "/data/temp/pulse-m4-nfs.") || mount != filepath.Join(work, "client") {
		t.Fatal("owned NFS namespace required")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(mount, &stat); err != nil || stat.Type != unix.NFS_SUPER_MAGIC {
		t.Fatal("real kernel NFS required", err)
	}
	for _, action := range []string{"hardlink", "copy", "move", "hardlink_rollback"} {
		t.Run(action, func(t *testing.T) {
			rollback := action == "hardlink_rollback"
			if rollback {
				action = "hardlink"
			}
			directories := &m8OrganizationDirectories{}
			var err error
			directories.source, err = os.MkdirTemp(mount, "m8-source-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(directories.source); err != nil {
					t.Error("remove NFS source", err)
				}
			})
			directories.target, err = os.MkdirTemp(mount, "m8-target-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(directories.target); err != nil {
					t.Error("remove NFS target", err)
				}
			})
			directories.remoteSource = filepath.Join(work, "export", filepath.Base(directories.source))
			filesystem := rootfs.NewFilesystem()
			cap, err := filesystem.OpenRoot(t.Context(), directories.source)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := cap.Identity(t.Context())
			if err != nil {
				cap.Close()
				t.Fatal(err)
			}
			hints := make(chan ports.RootChangeHint, 1)
			err = filesystem.WatchRoot(t.Context(), ports.RootWatchRequest{Root: cap, ConfiguredPath: directories.source, FrozenIdentity: identity}, hints)
			cap.Close()
			if !errors.Is(err, ports.ErrFileWatchUnavailable) || len(hints) != 0 {
				t.Fatal("NFS must not promise local notifications", err)
			}
			unsupported := false
			if action != "hardlink" {
				const name = "native-probe.bin"
				if err := os.WriteFile(filepath.Join(directories.source, name), []byte("owned NFS native publication probe"), 0600); err != nil {
					t.Fatal(err)
				}
				roots, release, err := filesystem.LockRoots(t.Context(), directories.source, directories.target)
				if err != nil {
					t.Fatal("NFS native root locks", err)
				}
				func() {
					defer release()
					identity, err := roots[directories.source].Inspect(t.Context(), name)
					if err != nil {
						t.Fatal(err)
					}
					content, err := filesystem.InspectContent(t.Context(), roots[directories.source], name, identity)
					if err != nil {
						t.Fatal(err)
					}
					key := uuid.New()
					_, err = filesystem.TransferVerified(t.Context(), roots[directories.source], name, roots[directories.target], name, content, action, key)
					if err != nil {
						if !errors.Is(err, ports.ErrFileUnsupported) {
							t.Fatalf("NFS native %s capability failed: %v", action, err)
						}
						unsupported = true
					}
				}()
				for _, path := range []string{filepath.Join(directories.source, name), filepath.Join(directories.target, name)} {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
			}
			fixture := m8UntrackedOrganizationWithDirectories(t, action, directories)
			if rollback {
				m8UntrackedOrganizationFinalizeRecoveryFixture(t, fixture)
				t.Log("M8_NFS_ROLLBACK_RECOVERY_PASS action=hardlink server=KERNEL_NFS41 published_before_rollback=true facts_rolled_back=true permit_token1_preserved=true observation_token2=true same_target_inode=true original_receipts=true downloader_calls=0")
				return
			}
			history := fixture.confirmationHistory(t)
			receipt, raw := fixture.queue(t)
			fixture.start(t)
			if unsupported {
				fixture.wait(t, "unsupported NFS action reaches finite observation-only failure", func() bool {
					return fixture.predicate(t, `SELECT state='dead' AND attempts=3 AND max_attempts=3 FROM job WHERE id=$1`, receipt.Operation.JobID)
				})
				fixture.stop(t)
				fixture.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND job_token=1 AND execution_token=1`, 1, receipt.Operation.ID)
				fixture.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
				fixture.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
				fixture.count(t, `SELECT count(*) FROM library_file_unit`, 0)
				fixture.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, 0)
				fixture.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.organization.imported'`, 0)
				if got := m8UntrackedOrganizationIdentity(t, fixture.source, fixture.sourceRelative); got != fixture.sourceIdentity {
					t.Fatal("unsupported NFS operation changed source identity")
				}
				if _, err := os.Lstat(filepath.Join(fixture.target, fixture.confirmation.Material.Input.TargetRelativePath)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsupported NFS operation published a target")
				}
				fixture.recoverOffline(t, receipt, raw, "failed")
				if history != fixture.confirmationHistory(t) {
					t.Fatal("unsupported NFS operation changed confirmation")
				}
				t.Logf("M8_NFS_UNSUPPORTED_PROTECTION_PASS action=%s watcher=UNAVAILABLE remote_write=SERVER_EXPORT discovery=ORIGINAL_60S_SWEEP noreplace=UNSUPPORTED source_preserved=true target_absent=true permit_original=true attempts=3 observation_only_recovery=true imported=0 original_receipt_offline=true downloader_calls=0", action)
				return
			}
			fixture.completed(t, receipt)
			fixture.stop(t)
			fixture.verifyImport(t, receipt, action)
			fixture.recoverOffline(t, receipt, raw, "completed")
			if history != fixture.confirmationHistory(t) {
				t.Fatal("NFS execution changed original confirmation")
			}
			t.Logf("M8_NFS_PERIODIC_ORGANIZATION_PASS action=%s filesystem=KERNEL_NFS41 watcher=UNAVAILABLE remote_write=SERVER_EXPORT discovery=ORIGINAL_60S_SWEEP identity_confirmation=EXPLICIT inode_and_bytes=VERIFIED original_receipt_offline=true downloader_calls=0 immediate_notification=NOT_CLAIMED", action)
		})
	}
}
