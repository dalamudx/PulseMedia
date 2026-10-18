package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

func TestTransferMigrationQuarantinesLegacyPlans(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	olderDir := t.TempDir()
	paths, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name >= "00016" {
			continue
		}
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(olderDir, name), text, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := postgres.Migrate(ctx, dsn, olderDir); err != nil || v != 15 {
		t.Fatalf("prepare v15 database: %d %v", v, err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Using SQL against the actual pre-v16 schema rather than new generated columns.
	root := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO library_root (id,path,media_type,name) VALUES ($1,'/test-only-library','movie','Movies')", root); err != nil {
		t.Fatal(err)
	}
	old, completed := uuid.New(), uuid.New()
	for id, state := range map[uuid.UUID]string{old: "executing", completed: "completed"} {
		if _, err := pool.Exec(ctx, "INSERT INTO transfer_plan (id,operation_id,root_id,state,strategy) VALUES ($1,$2,$3,$4,'hardlink')", id, uuid.New(), root, state); err != nil {
			t.Fatal(err)
		}
	}
	// Seed the actual pre-organization schema; new generated task columns do not exist yet.
	pre := db.New(pool)
	idx, candidate, client, oldOperation := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted,categories) VALUES($1,'fixture','https://example.test',$2,'[]')", idx, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile) VALUES($1,$2,'fixture','Movie',$3,0,'{}')", candidate, idx, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := pre.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "fixture", ClientType: "qbittorrent", Url: "http://fixture", Username: "fixture", PasswordEncrypted: []byte("fixture"), RemotePathMap: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO download_task (id,operation_id,client_id,candidate_id,info_hash,state,save_path) VALUES ($1,$2,$3,$4,'aaa','completed','/downloads')", uuid.New(), oldOperation, client, candidate); err != nil {
		t.Fatal(err)
	}
	if v, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatalf("upgrade to fenced schema: %d %v", v, err)
	}
	q := db.New(pool)
	oldTask, err := q.GetDownloadTaskByOpID(ctx, oldOperation)
	if err != nil || oldTask.OrganizationState != "review_required" || oldTask.TransferPlanID.Valid {
		t.Fatalf("legacy completion inferred execution evidence: %+v %v", oldTask, err)
	}
	legacy, err := q.GetTransferPlanByID(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.State != "review_required" || legacy.RootPath.Valid || legacy.LeaseUntil.Valid {
		t.Fatalf("unverified legacy plan not quarantined: %+v", legacy)
	}
	if _, err := q.ClaimTransferPlan(ctx, db.ClaimTransferPlanParams{ID: old, Owner: legacy.ExecutionOwner}); err == nil {
		t.Fatal("legacy executor was reclaimed without frozen evidence")
	}
	done, err := q.GetTransferPlanByID(ctx, completed)
	if err != nil || done.State != "completed" {
		t.Fatalf("terminal state was replayed: %s %v", done.State, err)
	}
}
