package httpapi_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompleteTVTrackingRuntimeDirectoryHint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux inotify")
	}
	runCompleteTVPublicAppNative(t, true, true)
}

// Observe the kernel registration, rather than assuming elapsed time proves
// watcher readiness. No file event is sent until the nested directory is watched.
func completeTVWatchKernelReady(t *testing.T, directory string) bool {
	t.Helper()
	root, err := rootfs.NewFilesystem().OpenRoot(t.Context(), directory)
	if err != nil {
		return false
	}
	identity, err := root.Identity(t.Context())
	root.Close()
	if err != nil {
		return false
	}
	want := "ino:" + strconv.FormatInt(identity.Inode, 16)
	entries, err := os.ReadDir("/proc/self/fdinfo")
	if err != nil || len(entries) > 4096 {
		return false
	}
	for _, entry := range entries {
		raw, e := os.ReadFile(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if e != nil || len(raw) > 256*1024 {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != "inotify" {
				continue
			}
			for _, field := range fields {
				if field == want {
					return true
				}
			}
		}
	}
	return false
}

func assertCompleteTVWatchEvent(t *testing.T, pool *pgxpool.Pool, root string, operation uuid.UUID, complete func()) {
	t.Helper()
	runtimeTVWait(t, func() bool { return completeTVWatchKernelReady(t, filepath.Join(root, "Show")) })
	var prior uuid.UUID
	var updated time.Time
	var result []byte
	if err := pool.QueryRow(t.Context(), `SELECT id,updated_at,result FROM job WHERE kind=$1 AND payload->>'operation_id'=$2::text AND state='succeeded' AND result->>'state'='tracking' ORDER BY updated_at DESC,id DESC LIMIT 1`, ports.CompleteTVTrackingJobKind, operation).Scan(&prior, &updated, &result); err != nil {
		t.Fatal(err)
	}
	complete()
	started := time.Now()
	if err := os.WriteFile(filepath.Join(root, "Show", "Show.S01E01.mkv"), make([]byte, 512), 0644); err != nil {
		t.Fatal(err)
	}
	runtimeTVWait(t, func() bool {
		var done bool
		err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM complete_tv_tracking_hint h JOIN job j ON j.id=h.job_id JOIN download_task t ON t.id=h.task_id WHERE h.operation_id=$1 AND h.prior_job_id=$2 AND j.state='succeeded' AND j.result->>'state'='completed' AND t.state='completed' AND t.organization_state='review_required' AND t.transfer_plan_id IS NULL)`, operation, prior).Scan(&done)
		return err == nil && done
	})
	var unchanged, beforeWindow bool
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND updated_at=$2 AND result=$3::jsonb),clock_timestamp()<$2::timestamptz+interval '30 seconds',(SELECT count(*) FROM complete_tv_tracking_hint WHERE operation_id=$4)`, prior, updated, result, operation).Scan(&unchanged, &beforeWindow, &count); err != nil {
		t.Fatal(err)
	}
	if !unchanged || !beforeWindow || count != 1 {
		t.Fatal("event did not preserve its predecessor and unique early grant", unchanged, beforeWindow, count)
	}
	t.Logf("M8_COMPLETE_TV_WATCH_APP_PASS real_App=true inotify=REAL native=OWNED_HTTP hints=1 predecessor_unchanged=true before_30s=true observed_ms=%d Add_replayed=false organization=review_required", time.Since(started).Milliseconds())
}
