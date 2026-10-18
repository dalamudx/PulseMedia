package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectionContractMigrationInvalidatesWithoutRewritingHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	older := t.TempDir()
	paths, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name >= "00044" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(older, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if version, err := postgres.Migrate(ctx, dsn, older); err != nil || version != 43 {
		t.Fatal("legacy migration", version, err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, rootID, mediaID, unitID, subID, wantedID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'projection-migration','not-a-real-password','admin')", userID)
	exec("INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/fixture-projection','movie','Fixture')", rootID)
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'Projection migration','movie')", mediaID)
	exec("INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')", unitID, mediaID)
	exec("INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)", subID, userID, mediaID, rootID)
	exec(`INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version,is_satisfied,current_fact_stage,last_evaluated_at,next_evaluate_at,
 evaluated_intent_version,evaluated_policy_id,evaluated_policy_revision,evaluated_unit_id,evaluated_media_id,evaluated_root_id,evaluated_root_generation,evaluated_health_revision,evaluated_media_metadata_version,evaluated_unit_metadata_version,evaluated_facts_version,evaluated_state,evaluated_skipped)
 SELECT $1,s.id,u.id,s.intent_version,true,'imported',clock_timestamp(),clock_timestamp()+interval '1 year',s.intent_version,s.quality_policy_id,s.quality_policy_revision,u.id,s.media_id,s.root_id,r.generation,r.health_revision,m.metadata_version,u.metadata_version,u.facts_version,s.state,false
 FROM subscription s JOIN media_unit u ON u.id=$3 JOIN library_root r ON r.id=s.root_id JOIN media m ON m.id=s.media_id WHERE s.id=$2`, wantedID, subID, unitID)
	var current bool
	var factsBefore int64
	var evaluatedBefore, nextBefore time.Time
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || !current {
		t.Fatal("legacy valid stamp fixture", current, err)
	}
	if err = pool.QueryRow(ctx, "SELECT u.facts_version,w.last_evaluated_at,w.next_evaluate_at FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.id=$1", wantedID).Scan(&factsBefore, &evaluatedBefore, &nextBefore); err != nil {
		t.Fatal(err)
	}
	if version, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal(version, err)
	}
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || current {
		t.Fatal("migration kept legacy result current", current, err)
	}
	var satisfied bool
	var stage string
	var factsAfter int64
	var evaluatedAfter, nextAfter time.Time
	if err = pool.QueryRow(ctx, "SELECT w.is_satisfied,w.current_fact_stage,u.facts_version,w.last_evaluated_at,w.next_evaluate_at FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.id=$1", wantedID).Scan(&satisfied, &stage, &factsAfter, &evaluatedAfter, &nextAfter); err != nil || !satisfied || stage != "imported" || factsAfter != factsBefore || !evaluatedBefore.Equal(evaluatedAfter) || !nextBefore.Equal(nextAfter) {
		t.Fatal("migration rewrote historical result or source versions", err)
	}
	var due bool
	if err = pool.QueryRow(ctx, "SELECT NOT p.inputs_current OR w.next_evaluate_at<=clock_timestamp() FROM wanted_item w JOIN wanted_projection_validity p ON p.id=w.id WHERE w.id=$1", wantedID).Scan(&due); err != nil || !due {
		t.Fatal("far-future legacy result not due", due, err)
	}
	// An empty newly-added contract is reversible; the old outcome still exists.
	// Empty append-only contracts may sit above the original schema44 guard.
	for version := int64(postgres.CurrentMigrationVersion); version > 43; {
		next, rollbackErr := postgres.Rollback(ctx, dsn, "../../../migrations")
		if rollbackErr != nil || next != version-1 {
			t.Fatal("empty contract downgrade", next, rollbackErr)
		}
		version = next
	}
	if _, err = postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE wanted_item SET is_satisfied=is_satisfied,evaluated_contract_version=$2,evaluated_write_token=gen_random_uuid() WHERE id=$1", wantedID, quality.OwnershipContractVersion)
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || !current {
		t.Fatal("known contract fixture not valid", current, err)
	}
	exec("UPDATE wanted_item SET is_satisfied=is_satisfied WHERE id=$1", wantedID)
	if err = pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wantedID).Scan(&current); err != nil || current {
		t.Fatal("old writer kept new authority", current, err)
	}
	// Reach the schema46 semantic guard; later source-only migrations may be removed.
	for version := int64(postgres.CurrentMigrationVersion); version > 46; version-- {
		if next, rollbackErr := postgres.Rollback(ctx, dsn, "../../../migrations"); rollbackErr != nil || next != version-1 {
			t.Fatal("later schema downgrade", next, rollbackErr)
		}
	}
	if _, err = postgres.Rollback(ctx, dsn, "../../../migrations"); err == nil {
		t.Fatal("invalidated evaluator evidence discarded by downgrade")
	}
	if err = postgres.CheckSchemaVersion(ctx, pool, 46); err != nil {
		t.Fatal("failed downgrade mutated schema", err)
	}
}
