package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

func TestQualityConflictMigrationInvalidatesOldSignedProjectionWithoutResigning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	older := t.TempDir()
	paths, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name >= "00045" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(older, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if version, err := postgres.Migrate(ctx, dsn, older); err != nil || version != 44 {
		t.Fatal("schema44 fixture", version, err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	user, root, parent, unit, sub, wanted, file, fact, writeToken := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'quality-conflict-migration','not-a-real-password','admin')", user)
	exec("INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/fixture-quality-conflict','movie','Fixture')", root)
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'Quality conflict','movie')", parent)
	exec("INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')", unit, parent)
	exec("INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)", sub, user, parent, root)
	exec("INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'original.mkv',100,1)", file, root)
	exec("INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", file, unit)
	original := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v1","codec":"HEVC","codec_parser_version":"codec.tokens.v1"}`)
	exec("INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) VALUES($1,$2,'imported',$3,$4,$5)", fact, unit, file, root, original)
	exec(`INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version,is_satisfied,current_fact_stage,last_evaluated_at,next_evaluate_at,
 evaluated_contract_version,evaluated_write_token,evaluated_intent_version,evaluated_policy_id,evaluated_policy_revision,evaluated_unit_id,evaluated_media_id,evaluated_root_id,evaluated_root_generation,evaluated_health_revision,evaluated_media_metadata_version,evaluated_unit_metadata_version,evaluated_facts_version,evaluated_state,evaluated_skipped)
 SELECT $1,s.id,u.id,s.intent_version,true,'imported',clock_timestamp(),clock_timestamp()+interval '1 year',
 'ownership.quality.v1',$4,s.intent_version,s.quality_policy_id,s.quality_policy_revision,u.id,s.media_id,s.root_id,r.generation,r.health_revision,m.metadata_version,u.metadata_version,u.facts_version,s.state,false
 FROM subscription s JOIN media_unit u ON u.id=$3 JOIN library_root r ON r.id=s.root_id JOIN media m ON m.id=s.media_id WHERE s.id=$2`, wanted, sub, unit, writeToken)
	var current bool
	var beforeFacts int64
	var beforeEvaluated, beforeNext time.Time
	if err := pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wanted).Scan(&current); err != nil || !current {
		t.Fatal("old signed projection fixture", current, err)
	}
	if err := pool.QueryRow(ctx, "SELECT u.facts_version,w.last_evaluated_at,w.next_evaluate_at FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.id=$1", wanted).Scan(&beforeFacts, &beforeEvaluated, &beforeNext); err != nil {
		t.Fatal(err)
	}
	if version, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal("append-only current contract", version, err)
	}
	if err := pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wanted).Scan(&current); err != nil || current {
		t.Fatal("superseded signed result remained green", current, err)
	}
	var retained []byte
	var afterFacts int64
	var afterEvaluated, afterNext time.Time
	var retainedToken uuid.UUID
	var retainedContract string
	var satisfied bool
	if err := pool.QueryRow(ctx, `SELECT f.quality_profile,u.facts_version,w.last_evaluated_at,w.next_evaluate_at,w.evaluated_write_token,w.evaluated_contract_version,w.is_satisfied FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id JOIN unit_fact f ON f.id=$2 WHERE w.id=$1`, wanted, fact).Scan(&retained, &afterFacts, &afterEvaluated, &afterNext, &retainedToken, &retainedContract, &satisfied); err != nil {
		t.Fatal(err)
	}
	var originalValue, retainedValue any
	if json.Unmarshal(original, &originalValue) != nil || json.Unmarshal(retained, &retainedValue) != nil {
		t.Fatal("decode preserved facts")
	}
	a, _ := json.Marshal(originalValue)
	b, _ := json.Marshal(retainedValue)
	if !bytes.Equal(a, b) || afterFacts != beforeFacts || !afterEvaluated.Equal(beforeEvaluated) || !afterNext.Equal(beforeNext) || retainedToken != writeToken || retainedContract != "ownership.quality.v1" || !satisfied {
		t.Fatal("migration re-signed evidence or changed cached history")
	}
	var files, mappings int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM library_file WHERE id=$1),(SELECT count(*) FROM library_file_unit WHERE file_id=$1 AND unit_id=$2)", file, unit).Scan(&files, &mappings); err != nil || files != 1 || mappings != 1 {
		t.Fatal("file history disappeared", files, mappings, err)
	}
	var due bool
	if err := pool.QueryRow(ctx, "SELECT NOT p.inputs_current OR w.next_evaluate_at<=clock_timestamp() FROM wanted_item w JOIN wanted_projection_validity p ON p.id=w.id WHERE w.id=$1", wanted).Scan(&due); err != nil || !due {
		t.Fatal("future schedule hid obsolete contract", due, err)
	}
	// A still-running old binary can carry a fresh write token, not new semantics.
	exec("UPDATE wanted_item SET is_satisfied=true,evaluated_contract_version='ownership.quality.v1',evaluated_write_token=gen_random_uuid() WHERE id=$1", wanted)
	if err := pool.QueryRow(ctx, "SELECT inputs_current FROM wanted_projection_validity WHERE id=$1", wanted).Scan(&current); err != nil || current {
		t.Fatal("old semantic writer retained authority", current, err)
	}
	// Reach the schema46 semantic guard; later source-only migrations may be removed.
	for version := int64(postgres.CurrentMigrationVersion); version > 46; version-- {
		if next, rollbackErr := postgres.Rollback(ctx, dsn, "../../../migrations"); rollbackErr != nil || next != version-1 {
			t.Fatal("later schema downgrade", next, rollbackErr)
		}
	}
	if _, err := postgres.Rollback(ctx, dsn, "../../../migrations"); err == nil {
		t.Fatal("downgrade revived old quality contract")
	}
	if err := postgres.CheckSchemaVersion(ctx, pool, 46); err != nil {
		t.Fatal(err)
	}
	if quality.OwnershipContractVersion != "ownership.quality.v3" {
		t.Fatal("runtime and view semantic version diverged")
	}
}
