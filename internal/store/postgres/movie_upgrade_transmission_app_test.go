package postgres_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

type transmissionUpgradeStartup struct {
	ready chan struct{}
	once  sync.Once
}

func (s *transmissionUpgradeStartup) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("HTTP server listening")) {
		s.once.Do(func() { close(s.ready) })
	}
	return len(p), nil
}

func startTransmissionUpgradeApp(t *testing.T, f *organizationJobFixture) func() {
	t.Helper()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(bytes.Repeat([]byte{37}, 32))
	cfg.Database.URL = f.dsn
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	log := &transmissionUpgradeStartup{ready: make(chan struct{})}
	a, e := app.New(cfg, app.WithLogger(logging.NewLogger(log, "info")))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil {
					t.Error(e)
				}
			case <-time.After(20 * time.Second):
				t.Error("upgrade App did not drain")
				_ = a.Close()
			}
		})
	}
	t.Cleanup(stop)
	select {
	case <-log.ready:
	case e := <-done:
		done <- e
		t.Fatal("upgrade App startup", e)
	case <-time.After(20 * time.Second):
		t.Fatal("upgrade App startup timed out")
	}
	return stop
}
func transmissionUpgradeWait(t *testing.T, f *organizationJobFixture, query string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var ok bool
		if e := f.p.QueryRow(t.Context(), query, f.operation).Scan(&ok); e == nil && ok {
			return
		}
		select {
		case <-deadline.C:
			var state, reason string
			var claims, permits, results int
			err := f.p.QueryRow(t.Context(), `SELECT j.state,COALESCE(j.error,''),(SELECT count(*) FROM movie_upgrade_organization_job_claim c WHERE c.job_id=j.id),(SELECT count(*) FROM movie_upgrade_organization_execution_permit p WHERE p.job_id=j.id),(SELECT count(*) FROM movie_upgrade_organization_execution_result r JOIN movie_upgrade_organization_execution_permit p USING(permit_id) WHERE p.job_id=j.id) FROM movie_upgrade_organization_job b JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1`, f.operation).Scan(&state, &reason, &claims, &permits, &results)
			t.Fatalf("upgrade App condition timed out: %s state=%s reason=%s claims=%d permits=%d results=%d diagnostic=%v", query, state, reason, claims, permits, results, err)
		case <-tick.C:
		}
	}
}

func TestTransmissionMovieUpgradeAppExplicitOrganizationRestartIngestionAndRetirement(t *testing.T) {
	f := transmissionMoviePhysicalFixture(t)
	if _, e := postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	f.queued(t)
	stop := startTransmissionUpgradeApp(t, f)
	transmissionUpgradeWait(t, f, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_execution_result r JOIN movie_upgrade_organization_execution_permit p USING(permit_id) WHERE p.operation_id=$1 AND r.status='linked_pending_ingestion')`)
	stop()
	source, e := os.Stat(f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.Stat(executionTarget(t, f))
	if e != nil || !os.SameFile(source, target) {
		t.Fatal("App did not hardlink original source", e)
	}
	// A fresh App consumes trusted linked evidence left by the previous worker.
	stop = startTransmissionUpgradeApp(t, f)
	transmissionUpgradeWait(t, f, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_ingestion WHERE operation_id=$1)`)
	stop()
	retirement, e := postgres.NewMovieUpgradeRetirementStore(f.trackingFixture.preparation, rootfs.NewFilesystem())
	if e != nil {
		t.Fatal(e)
	}
	confirmRetirement79(t, f, retirement, f.sub)
	stop = startTransmissionUpgradeApp(t, f)
	transmissionUpgradeWait(t, f, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_retirement_result WHERE operation_id=$1 AND status='retired')`)
	stop()
	if f.adds.Load() != 1 || f.exports.Load() != 0 {
		t.Fatal("App replayed original native operation", f.adds.Load(), f.exports.Load())
	}
	if _, e := os.Stat(filepath.Join(f.rootPath, "original.mkv")); !os.IsNotExist(e) {
		t.Fatal("old version was not recycled", e)
	}
	current, e := os.Stat(executionTarget(t, f))
	if e != nil || !os.SameFile(source, current) {
		t.Fatal("retirement changed new file", e)
	}
	var claims, results, imports, retired int
	e = f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM movie_upgrade_organization_job_claim c JOIN movie_upgrade_organization_job j USING(job_id) WHERE j.operation_id=$1),(SELECT count(*) FROM movie_upgrade_organization_execution_result r JOIN movie_upgrade_organization_execution_permit p USING(permit_id) WHERE p.operation_id=$1),(SELECT count(*) FROM movie_upgrade_organization_ingestion WHERE operation_id=$1),(SELECT count(*) FROM movie_upgrade_retirement_result WHERE operation_id=$1)`, f.operation).Scan(&claims, &results, &imports, &retired)
	if e != nil || claims != 1 || results != 1 || imports != 1 || retired != 1 {
		t.Fatal("App lifecycle duplicated authority", claims, results, imports, retired, e)
	}
}
