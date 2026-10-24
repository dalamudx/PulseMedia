package postgres_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestMovieUpgradeNamingNativeExecutionAndIngestionPG(t *testing.T) {
	for _, stage := range []string{"normal", "ingestion-tail"} {
		t.Run(stage, func(t *testing.T) {
			physical := physicalOrganizationFixture75(t)
			version := setUpgradeNaming(t, physical, "Frozen/{title} [{tmdb_id}]/{title} ({year}).{extension}")
			prepared, err := physical.preparation.PrepareMovieUpgradeOrganization(t.Context(), physical.actor, physical.command, "m9-upgrade-execution-prepare")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = postgres.Migrate(t.Context(), physical.dsn, "../../../migrations"); err != nil {
				t.Fatal(err)
			}
			f := &organizationJobFixture{physicalOrganizationFixture: physical}
			f.repo, err = postgres.NewMovieUpgradeOrganizationJobRepository(f.trackingFixture.preparation)
			if err != nil {
				t.Fatal(err)
			}
			f.command = ports.QueueMovieUpgradeOrganization{OperationID: f.operation, PreparationID: prepared.ID, Confirmed: true}
			f.queue = jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
			var original []byte
			if err = f.p.QueryRow(t.Context(), `SELECT to_jsonb(p) FROM movie_upgrade_organization_preparation p WHERE id=$1`, prepared.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			before, old := nativeProtectedGraph(t, f.preparedUpgradeFixture), f.oldState(t)
			// A new configuration cannot change any command or target already prepared.
			if _, err = db.New(f.p).SetSetting(t.Context(), db.SetSettingParams{Key: naming.TemplateSettingKey, Value: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			queued := f.queued(t)
			lease := f.lease(t, 2*time.Minute)
			if captured, err := f.repo.CaptureMovieUpgradeOrganizationJob(t.Context(), f.actor, lease); err != nil || captured == nil {
				t.Fatal("original claim", err)
			}
			files := &nativeLinkFS{TransferFilesystem: rootfs.NewFilesystem()}
			executor := executionRepository(t, f.p, files)
			result, err := executor.ExecuteMovieUpgradeOrganization(t.Context(), f.actor, lease)
			if err != nil || result == nil || result.Status != "linked_pending_ingestion" || files.calls.Load() != 1 {
				t.Fatal("frozen custom target not executed exactly once", err, files.calls.Load())
			}
			target := executionTarget(t, f)
			if target != filepath.Join(f.rootPath, "Frozen/Fight Club [550]/Fight Club (1999).mkv") {
				t.Fatal("execution reinterpreted naming configuration")
			}
			source, err := os.Stat(f.sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			published, err := os.Stat(target)
			if err != nil || !os.SameFile(source, published) {
				t.Fatal("not the original hard link", err)
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("physical publication changed old protection")
			}
			ingest, err := postgres.NewMovieUpgradeOrganizationIngestionRepository(f.trackingFixture.preparation, files, outbox.New(f.p, idgen.New(), nativeUpgradeClock{}))
			if err != nil {
				t.Fatal(err)
			}
			if stage == "ingestion-tail" {
				f.exec(t, `CREATE FUNCTION naming_ingestion_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'naming ingestion tail'; END $$`)
				f.exec(t, `CREATE CONSTRAINT TRIGGER naming_ingestion_tail AFTER INSERT ON movie_upgrade_organization_ingestion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION naming_ingestion_tail()`)
				if receipt, err := ingest.IngestMovieUpgradeOrganization(t.Context(), f.actor, lease.ID); err == nil || receipt != nil {
					t.Fatal("injected transaction tail published ingestion")
				}
				assertIngestionAbsent(t, f)
				if old != f.oldState(t) || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
					t.Fatal("ingestion rollback changed old protections")
				}
				f.exec(t, `DROP TRIGGER naming_ingestion_tail ON movie_upgrade_organization_ingestion`)
			}
			imported, err := ingest.IngestMovieUpgradeOrganization(t.Context(), f.actor, lease.ID)
			if err != nil || imported == nil || imported.Status != "imported_pending_retirement" || imported.ExecutionAllowed {
				t.Fatal("frozen target ingestion", err)
			}
			recovered, err := ingest.IngestMovieUpgradeOrganization(t.Context(), f.actor, lease.ID)
			if err != nil || recovered == nil || *recovered != *imported {
				t.Fatal("ingestion original result changed", err)
			}
			replay, err := f.repo.QueueMovieUpgradeOrganization(t.Context(), f.actor, f.command, "execute-command")
			if err != nil || replay == nil || *replay != *queued {
				t.Fatal("original execute command did not restore its receipt", err)
			}
			if files.calls.Load() != 1 || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("recovery repeated physical work or changed old protection")
			}
			after, err := os.Stat(target)
			if err != nil || !os.SameFile(published, after) {
				t.Fatal("recovery replaced the published target", err)
			}
			var current []byte
			if err = f.p.QueryRow(t.Context(), `SELECT to_jsonb(p) FROM movie_upgrade_organization_preparation p WHERE id=$1`, prepared.ID).Scan(&current); err != nil || !bytes.Equal(original, current) {
				t.Fatal("execution mutated original prepared material", err)
			}
			var frozen struct {
				Snapshot struct {
					Physical struct {
						Version int64 `json:"naming_version"`
					} `json:"physical"`
				} `json:"snapshot"`
			}
			if json.Unmarshal(current, &frozen) != nil || frozen.Snapshot.Physical.Version != version {
				t.Fatal("original naming version lost")
			}
			var facts, permits, events int
			if err = f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM movie_upgrade_organization_execution_permit WHERE operation_id=$1),(SELECT count(*) FROM movie_upgrade_organization_ingestion WHERE operation_id=$1),(SELECT count(*) FROM outbox_event WHERE event_type='media.imported')`, f.operation).Scan(&permits, &facts, &events); err != nil || permits != 1 || facts != 1 || events != 1 {
				t.Fatal("duplicate permit, ingestion or event", err, permits, facts, events)
			}
		})
	}
}
