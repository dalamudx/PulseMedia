//go:build linux

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sys/unix"
)

// This checkpoint runs AFTER the real observation transaction commits. Every
// filesystem operation remains the production native kernel-NFS implementation.
type m4NFSCheckpoint struct {
	ports.ScanRepository
	once       sync.Once
	checkpoint func()
}

func (r *m4NFSCheckpoint) Record(ctx context.Context, in ports.ScanObservation) (ports.ScanObservationResult, error) {
	out, err := r.ScanRepository.Record(ctx, in)
	if err == nil {
		r.once.Do(r.checkpoint)
	}
	return out, err
}

func TestM4KernelNFSFaults(t *testing.T) {
	if os.Getenv("PULSE_M4_NFS_FAULTS") != "1" {
		t.Skip("explicit isolated kernel NFS opt-in required")
	}
	rootPath, work, cid := os.Getenv("PULSE_M4_NFS_ROOT"), os.Getenv("PULSE_M4_NFS_WORK"), os.Getenv("PULSE_M4_NFS_CID")
	if !strings.HasPrefix(work, "/data/temp/pulse-m4-nfs.") || rootPath != filepath.Join(work, "client") || len(cid) != 64 {
		t.Fatal("invalid owned NFS fixture")
	}
	command := func(name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if name == "podman" {
			args = append([]string{"--mount=" + os.Getenv("PULSE_M4_NFS_PARENT_MNT"), "--", "podman"}, args...)
			name = "nsenter"
		}
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	must := func(t *testing.T, name string, args ...string) {
		t.Helper()
		if out, err := command(name, args...); err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
	}
	out, err := command("podman", "inspect", "-f", `{{index .Config.Labels "io.pulsemedia.nfs-owner"}}`, cid)
	if err != nil || strings.TrimSpace(string(out)) != work {
		t.Fatal("server ownership mismatch", err, string(out))
	}
	var fs unix.Statfs_t
	if err = unix.Statfs(rootPath, &fs); err != nil || fs.Type != unix.NFS_SUPER_MAGIC {
		t.Fatal("real kernel NFS required", err, fs.Type)
	}
	pool, _ := bootstrapTestPool(t)
	ctx := context.Background()
	q := postgres.Queries(pool)
	gen := idgen.New()
	repo := postgres.NewScanObservationStore(pool)
	scanner := library.NewScanner(gen, rootfs.NewFilesystem(), repo)
	marker := filepath.Join(rootPath, ".pulsemedia_root")
	if err = os.WriteFile(marker, []byte("owned-nfs-root"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: rootPath, MediaType: "movie", Name: "Owned kernel NFS"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := scanner.ScanRoot(ctx, root.ID)
	if err != nil || report.State != "succeeded" {
		t.Fatal("trusted baseline", report, err)
	}
	file, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: "Observed.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	media, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Owned synthetic NFS media", MediaType: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: unit.ID}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"imported", "confirmed"} {
		if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, UnitID: unit.ID, Stage: stage, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.ReadFile(filepath.Join(rootPath, "Observed.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if err = unix.Stat(filepath.Join(rootPath, "Observed.mkv"), &before); err != nil {
		t.Fatal(err)
	}
	initial, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved := func(t *testing.T) {
		t.Helper()
		var retired bool
		var facts, maps, missing int
		var watermark int64
		err := pool.QueryRow(ctx, `SELECT retired_at IS NOT NULL,(SELECT count(*) FROM unit_fact WHERE source_file_id=$1 AND revoked_at IS NULL),(SELECT count(*) FROM library_file_unit WHERE file_id=$1),(SELECT count(*) FROM missing_candidate WHERE file_id=$1 AND state='open'),(SELECT mutation_seq FROM library_root WHERE id=$2) FROM library_file WHERE id=$1`, file.ID, root.ID).Scan(&retired, &facts, &maps, &missing, &watermark)
		if err != nil || retired || facts != 2 || maps != 1 || missing != 0 || watermark != initial.MutationSeq {
			t.Errorf("preservation: retired=%v facts=%d maps=%d missing=%d watermark=%d/%d err=%v", retired, facts, maps, missing, watermark, initial.MutationSeq, err)
		}
	}
	t.Logf("BASELINE kernel_nfs=4.1 inode=%d size=%d sha256=%x run=%s", before.Ino, before.Size, sha256.Sum256(original), report.ScanRunID)
	t.Run("partial_real_directory_error_preserves_facts", func(t *testing.T) {
		// A real symlink encountered by native NFS traversal interrupts completeness.
		if err := os.Symlink("Observed.mkv", filepath.Join(rootPath, "RejectedLink")); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Remove(filepath.Join(rootPath, "RejectedLink")); err != nil {
				t.Error("remove owned rejected symlink", err)
			}
		}()
		if _, err := scanner.ScanRoot(ctx, root.ID); err == nil {
			t.Error("partial traversal accepted")
		}
		assertPreserved(t)
	})
	t.Run("paused_server_cancellation_retains_physical_slot", func(t *testing.T) {
		paused := make(chan struct{})
		// Independent watchdog is armed BEFORE pause and survives test assertions.
		watchdog := exec.Command("bash", "-c", `sleep 7; podman unpause "$1" >/dev/null 2>&1 || true`, "watchdog", cid)
		if err := watchdog.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = command("podman", "unpause", cid)
			if err := watchdog.Wait(); err != nil {
				t.Error("watchdog join", err)
			}
		}()
		scanCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var pauseErr error
		checkpoint := &m4NFSCheckpoint{ScanRepository: repo, checkpoint: func() { _, pauseErr = command("podman", "pause", cid); close(paused) }}
		first := make(chan error, 1)
		firstJoined := false
		defer func() {
			cancel()
			_, _ = command("podman", "unpause", cid)
			if !firstJoined {
				select {
				case <-first:
				case <-time.After(25 * time.Second):
					t.Error("first native worker cleanup join failed")
					_ = os.WriteFile(filepath.Join(work, "retain"), []byte("unjoined native I/O"), 0600)
				}
			}
		}()
		go func() {
			_, err := library.NewScanner(gen, rootfs.NewFilesystem(), checkpoint).ScanRoot(scanCtx, root.ID)
			first <- err
		}()
		select {
		case <-paused:
		case <-time.After(15 * time.Second):
			t.Fatal("observation checkpoint not reached")
		}
		if pauseErr != nil {
			t.Fatal(pauseErr)
		}
		// The next native OpenRoot/Fstat/ReadDir RPC must remain physically blocked.
		time.Sleep(250 * time.Millisecond)
		cancel()
		select {
		case err := <-first:
			firstJoined = true
			t.Fatalf("physical NFS call returned before server recovery: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
		var runsBefore int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM scan_run WHERE root_id=$1", root.ID).Scan(&runsBefore); err != nil {
			t.Fatal(err)
		}
		secondCtx, secondCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer secondCancel()
		second := make(chan error, 1)
		secondJoined := false
		defer func() {
			secondCancel()
			_, _ = command("podman", "unpause", cid)
			if !secondJoined {
				select {
				case <-second:
				case <-time.After(25 * time.Second):
					t.Error("second native worker cleanup join failed")
					_ = os.WriteFile(filepath.Join(work, "retain"), []byte("unjoined native I/O"), 0600)
				}
			}
		}()
		go func() { _, err := scanner.ScanRoot(secondCtx, root.ID); second <- err }()
		time.Sleep(650 * time.Millisecond)
		var runsAfter int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM scan_run WHERE root_id=$1", root.ID).Scan(&runsAfter); err != nil {
			t.Fatal(err)
		}
		if runsBefore != runsAfter {
			t.Error("second worker replayed while old physical call blocked")
		}
		assertPreserved(t)
		// Wait for the independently armed watchdog, then join both old native calls.
		select {
		case err := <-first:
			if err == nil {
				t.Error("cancelled scan succeeded")
			}
			firstJoined = true
		case <-time.After(20 * time.Second):
			t.Fatal("old native NFS I/O did not join after recovery")
		}
		select {
		case err := <-second:
			secondJoined = true
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("second worker result: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("second native worker did not join")
		}
		assertPreserved(t)
	})
	t.Run("server_stop_start_recovers_same_bytes", func(t *testing.T) {
		must(t, "podman", "stop", "--time", "3", cid)
		must(t, "podman", "start", cid)
		address, err := command("podman", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", cid)
		if err != nil || strings.TrimSpace(string(address)) != os.Getenv("PULSE_M4_NFS_ADDRESS") {
			t.Fatalf("NFS address changed across restart: address=%q err=%v", address, err)
		}
		ready := false
		for attempt := 0; attempt < 30; attempt++ {
			if _, err := command("rpcinfo", "-n", "2049", "-t", os.Getenv("PULSE_M4_NFS_ADDRESS"), "100003", "4"); err == nil {
				ready = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			t.Fatal("NFS RPC did not recover on original address")
		}
		t.Logf("NFS_SERVER_RESTART_RPC_READY address=%s", strings.TrimSpace(string(address)))
		b, err := os.ReadFile(filepath.Join(rootPath, "Observed.mkv"))
		if err != nil || sha256.Sum256(b) != sha256.Sum256(original) {
			t.Fatal("recovery digest", err)
		}
		var st unix.Stat_t
		if err = unix.Stat(filepath.Join(rootPath, "Observed.mkv"), &st); err != nil || st.Ino != before.Ino {
			t.Fatal("recovery inode", err, st.Ino, before.Ino)
		}
		assertPreserved(t)
	})
	t.Run("unmount_empty_underlay_rejected", func(t *testing.T) {
		must(t, "umount", rootPath)
		defer must(t, "mount", "-t", "nfs4", "-o", os.Getenv("PULSE_M4_NFS_OPTIONS"), os.Getenv("PULSE_M4_NFS_ADDRESS")+":/pulse", rootPath)
		if _, err := scanner.ScanRoot(ctx, root.ID); err == nil {
			t.Error("empty underlying directory accepted")
		}
		assertPreserved(t)
	})
	t.Run("replacement_mount_requires_explicit_generation", func(t *testing.T) {
		replacement := filepath.Join(work, "export", "replacement")
		if err := os.Mkdir(replacement, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(replacement, ".pulsemedia_root"), []byte("different-root"), 0600); err != nil {
			t.Fatal(err)
		}
		must(t, "umount", rootPath)
		must(t, "mount", "-t", "nfs4", "-o", os.Getenv("PULSE_M4_NFS_OPTIONS"), os.Getenv("PULSE_M4_NFS_ADDRESS")+":/pulse/replacement", rootPath)
		defer func() {
			must(t, "umount", rootPath)
			must(t, "mount", "-t", "nfs4", "-o", os.Getenv("PULSE_M4_NFS_OPTIONS"), os.Getenv("PULSE_M4_NFS_ADDRESS")+":/pulse", rootPath)
		}()
		var st unix.Stat_t
		if err := unix.Stat(rootPath, &st); err != nil {
			t.Fatal(err)
		}
		if _, err := scanner.ScanRoot(ctx, root.ID); err == nil {
			t.Error("replacement NFS source accepted without explicit new generation/baseline contract")
		}
		assertPreserved(t)
		t.Logf("REPLACEMENT_SOURCE kernel_nfs inode=%d device=%d", st.Ino, st.Dev)
	})
	t.Logf("NFS fault matrix completed for owned server %.12s", cid)
}
