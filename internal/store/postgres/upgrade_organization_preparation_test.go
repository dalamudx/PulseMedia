package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/transfer"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

type physicalOrganizationFixture struct {
	*organizationFixture
	preparation                      ports.MovieUpgradeOrganizationPreparationRepository
	command                          ports.PrepareMovieUpgradeOrganization
	rootPath, sourceRoot, sourcePath string
}

func physicalOrganizationFixture75(t *testing.T) *physicalOrganizationFixture {
	t.Helper()
	base := t.TempDir()
	root, source := filepath.Join(base, "library"), filepath.Join(base, "downloads")
	for _, p := range []string{root, source} {
		if e := os.Mkdir(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("root"), 0600); e != nil {
		t.Fatal(e)
	}
	oldPath := filepath.Join(root, "original.mkv")
	if e := os.WriteFile(oldPath, bytes.Repeat([]byte("o"), 100), 0600); e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(oldPath)
	if e != nil {
		t.Fatal(e)
	}
	inode := stat.Sys().(*syscall.Stat_t).Ino
	f := &physicalOrganizationFixture{rootPath: root, sourceRoot: source}
	f.organizationFixture = nativeOrganizationFixture(t, "completed", func(b *frozenUpgradeFixture) {
		b.exec(t, `UPDATE library_root SET path=$2 WHERE id=$1`, b.root, root)
		b.exec(t, `UPDATE library_root SET baseline_completed=true,health='online',marker_filename='.pulsemedia_root' WHERE id=$1`, b.root)
		mapping, _ := json.Marshal(map[string]string{root: source})
		b.exec(t, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, b.client, mapping)
		b.exec(t, `UPDATE library_file SET size_bytes=$2,mtime_ns=$3,file_fingerprint=$4 WHERE id=$1`, b.file, stat.Size(), stat.ModTime().UnixNano(), fmt.Sprintf("stat.v1:%d:%d:%d", inode, stat.Size(), stat.ModTime().UnixNano()))
		// Updating observed file metadata invalidates the original imported fact.
		// Publish the freshly observed original file before freezing any authority.
		b.exec(t, `UPDATE unit_fact SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1`, b.fact)
		var fresh uuid.UUID
		if e := b.p.QueryRow(context.Background(), `INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) SELECT gen_random_uuid(),unit_id,'imported',source_file_id,root_id,quality_profile FROM unit_fact WHERE id=$1 RETURNING id`, b.fact).Scan(&fresh); e != nil {
			t.Fatal(e)
		}
		b.fact = fresh
		b.project(t)
	})
	if v, e := postgres.Migrate(context.Background(), f.dsn, organization75MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
	auth, e := f.organization.AuthorizeMovieUpgradeOrganization(context.Background(), f.actor, f.organizationFixture.command(t), "physical-authority")
	if e != nil {
		t.Fatal(e)
	}
	review, e := f.organization.ReadMovieUpgradeOrganization(context.Background(), f.actor, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	f.sourcePath = filepath.Join(source, review.SourceRelativePath)
	if e = os.MkdirAll(filepath.Dir(f.sourcePath), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(f.sourcePath, bytes.Repeat([]byte("v"), int(review.SourceSize)), 0600); e != nil {
		t.Fatal(e)
	}
	f.command = ports.PrepareMovieUpgradeOrganization{OperationID: f.operation, AuthorizationID: auth.ID, Confirmed: true}
	store, e := postgres.NewUpgradePreparationStore(f.p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	f.preparation, e = postgres.NewMovieUpgradeOrganizationPreparationRepository(store, rootfs.NewFilesystem())
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *physicalOrganizationFixture) planCount(t *testing.T, want int) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"movie_upgrade_organization_preparation", "transfer_plan", "transfer_step"} {
		var n int
		if e := f.p.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != want {
			t.Fatal(table, n, want, e)
		}
	}
}
func TestMovieUpgradeOrganizationPhysicalPlanPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	ctx := context.Background()
	before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
	var wg sync.WaitGroup
	results := make(chan *ports.MovieUpgradeOrganizationPreparation, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "physical-prepare")
			results <- out
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var result *ports.MovieUpgradeOrganizationPreparation
	for out := range results {
		if out == nil || out.ExecutionAllowed || out.Status != "organization_prepared_not_executable" {
			t.Fatal(out)
		}
		if result != nil && !sameHistoricalJSON(out, result) {
			t.Fatal("same key changed receipt")
		}
		result = out
	}
	f.planCount(t, 1)
	var target, state string
	var token int64
	if e := f.p.QueryRow(ctx, `SELECT p.state,p.execution_token,s.target_path FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id WHERE p.id=$1`, result.PlanID).Scan(&state, &token, &target); e != nil || state != "review_required" || token != 0 {
		t.Fatal(state, token, e)
	}
	svc := transfer.NewService(f.p, idgen.New(), nil, rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	if e := svc.ExecutePlan(ctx, result.PlanID); !errors.Is(e, ports.ErrTransferExecutionRejected) {
		t.Fatal("generic execution", e)
	}
	if _, e := os.Stat(target); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("preparation created target", e)
	}
	if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
		t.Fatal("original rows changed")
	}
	f.exec(t, `UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1`, f.sub)
	if e := os.RemoveAll(f.sourceRoot); e != nil {
		t.Fatal(e)
	}
	replay, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "physical-prepare")
	if e != nil || *replay != *result {
		t.Fatal(replay, e)
	}
	read, e := f.preparation.ReadMovieUpgradeOrganizationPreparationReceipt(ctx, f.actor, f.operation, "physical-prepare")
	if e != nil || *read != *result {
		t.Fatal(read, e)
	}
	forged := f.command
	forged.AuthorizationID = uuid.New()
	if _, e = f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, forged, "physical-prepare"); e == nil {
		t.Fatal("changed command recovered")
	}
}
