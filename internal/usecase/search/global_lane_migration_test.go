package search_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
)

func TestGlobalLaneMigrationQuarantinesConflictingLegacyOperations(t *testing.T) {
	// Seed the pre-lane schema directly: a current search would create immutable
	// Schema 53 page evidence and must not be discarded to manufacture history.
	f := runtimeFixture(t)
	ctx := context.Background()
	rootB, candidateID := uuid.New(), uuid.New()
	var unitID uuid.UUID
	if e := f.pool.QueryRow(ctx, "SELECT unit_id FROM wanted_item WHERE id=$1", f.wanted).Scan(&unitID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, "INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,'movie','Legacy Root B',true)", rootB, t.TempDir()); e != nil {
		t.Fatal(e)
	}
	dsn := f.pool.Config().ConnConfig.ConnString()
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 24; expected-- {
		if v, e := postgres.Rollback(ctx, dsn, migrationsDir); e != nil || v != expected {
			t.Fatal(v, e)
		}
	}
	link, e := crypto.Encrypt(make([]byte, 32), []byte("https://tracker.invalid/legacy-fixture"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'Fight.Club.1999.1080p.BluRay','legacy-lane', $3,1024)", candidateID, f.idx, link); e != nil {
		t.Fatal(e)
	}
	for _, root := range []uuid.UUID{f.root, rootB} {
		op := uuid.New()
		if _, e := f.pool.Exec(ctx, "INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state) VALUES($1,$2,$3,'manual_acquisition',$4,$5,'reserved')", uuid.New(), unitID, root, candidateID, op); e != nil {
			t.Fatal(e)
		}
		payload, _ := json.Marshal(map[string]any{"operation_id": op, "root_id": root, "candidate_id": candidateID})
		if _, e := f.pool.Exec(ctx, "INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'download_submission','queued',$2,3)", uuid.New(), payload); e != nil {
			t.Fatal(e)
		}
	}
	if v, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	var active, conflicts, dead, roots int
	if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM acquisition_reservation WHERE legacy_conflict),(SELECT count(*) FROM job WHERE kind='download_submission' AND state='dead' AND error='legacy acquisition lane needs manual review'),(SELECT count(*) FROM unit_reservation WHERE state IN ('reserved','dispatched'))").Scan(&active, &conflicts, &dead, &roots); e != nil || active != 1 || conflicts != 1 || dead != 2 || roots != 2 {
		t.Fatal("legacy actions silently merged/replayed/released", active, conflicts, dead, roots, e)
	}
	// source26 can roll back while there's only one historical source version;
	// lane25 refuses to disappear while physical uncertainty still protects unit.
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 25; expected-- {
		if v, e := postgres.Rollback(ctx, dsn, migrationsDir); e != nil || v != expected {
			t.Fatal(v, e)
		}
	}
	if _, e := postgres.Rollback(ctx, dsn, migrationsDir); e == nil {
		t.Fatal("live unknown acquisition lane downgraded away")
	}
	var protected int
	if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL").Scan(&protected); e != nil || protected != 1 {
		t.Fatal("failed downgrade damaged global protection", protected, e)
	}
}
