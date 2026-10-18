package postgres_test

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseBlockMigrationRoundTripAndHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	// Exercise the schema56 guard itself, regardless of later migrations.
	dir := t.TempDir()
	paths, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name >= "00057" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if v, e := postgres.Migrate(ctx, dsn, dir); e != nil || v != 56 {
		t.Fatal(v, e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	user, root, media, sub, indexer, candidate := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	raw := []byte(`{"Resolution":"legacy-unknown","source":"unchanged"}`)
	cipher := []byte("opaque-encrypted-fixture")
	exec("INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'block-migration','fixture','member',true)", user)
	exec("INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/fixture-block-migration','movie','Fixture')", root)
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'Fixture','movie')", media)
	exec("INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)", sub, user, media, root)
	exec("INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'Fixture','https://fixture.invalid',$2)", indexer, cipher)
	exec("INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,quality_profile,size_bytes) VALUES($1,$2,'Fixture.2020.mkv','fixture',$3,$4,123)", candidate, indexer, cipher, raw)
	var before []byte
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text::bytea FROM release_candidate WHERE id=$1", candidate).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Rollback(ctx, dsn, dir); e != nil || v != 55 {
		t.Fatal("empty downgrade", v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, dir); e != nil || v != 56 {
		t.Fatal("upgrade", v, e)
	}
	var after, encrypted []byte
	if e := pool.QueryRow(ctx, "SELECT quality_profile::text::bytea,link_encrypted FROM release_candidate WHERE id=$1", candidate).Scan(&after, &encrypted); e != nil || !bytes.Equal(before, after) || !bytes.Equal(cipher, encrypted) {
		t.Fatal("historical candidate rewritten", e)
	}
	if _, e := pool.Exec(ctx, "INSERT INTO release_block(subscription_id,candidate_id,blocked) VALUES($1,$2,true)", sub, uuid.New()); e == nil {
		t.Fatal("dangling candidate accepted")
	}
	exec("INSERT INTO release_block(subscription_id,candidate_id,blocked) VALUES($1,$2,false)", sub, candidate)
	if _, e := postgres.Rollback(ctx, dsn, dir); e == nil {
		t.Fatal("unblocked history downgraded")
	}
	var present bool
	if e := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM release_block WHERE subscription_id=$1 AND candidate_id=$2 AND NOT blocked)", sub, candidate).Scan(&present); e != nil || !present {
		t.Fatal("history lost", e)
	}
}
