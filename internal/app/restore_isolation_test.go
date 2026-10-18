package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestRestoreIsolationMigrationAndHistory(t *testing.T) {
	p, _ := bootstrapTestPool(t)
	ctx := context.Background()
	var version string
	if err := p.QueryRow(ctx, `SHOW server_version_num`).Scan(&version); err != nil || version != "180006" {
		t.Fatalf("expected PG18.6: %s %v", version, err)
	}
	raw, err := os.ReadFile("../../migrations/00083_restore_isolation.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("missing down")
	}
	if _, err = p.Exec(ctx, parts[1]); err != nil {
		t.Fatal("empty down:", err)
	}
	if _, err = p.Exec(ctx, parts[0]); err != nil {
		t.Fatal("empty up:", err)
	}
	id := uuid.New()
	if err = postgres.ActivateRestoreIsolation(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	r := postgres.RestoreReconciliation{RestoreID: id, Operator: "operator", DownloaderEvidence: "checked", FilesystemEvidence: "checked", OrphanEvidence: "checked"}
	for _, sql := range []string{"UPDATE restore_isolation_audit SET evidence='changed'", "DELETE FROM restore_isolation_audit", "TRUNCATE restore_isolation_audit", "DELETE FROM restore_isolation", "TRUNCATE restore_isolation", parts[1]} {
		if _, err = p.Exec(ctx, sql); err == nil {
			t.Fatalf("allowed destructive SQL: %s", sql)
		}
	}
	if err = postgres.ReconcileRestoreIsolation(ctx, p, r); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, parts[1]); err == nil {
		t.Fatal("released history downgraded")
	}
	if err = postgres.RequireRestoreReconciled(ctx, p); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreIsolationBlocksUnsealedNotificationBatch(t *testing.T) {
	for _, state := range []string{"open", "ready"} {
		t.Run(state, func(t *testing.T) {
			p, _ := bootstrapTestPool(t)
			ctx := context.Background()
			q := postgres.Queries(p)
			m, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "restore fixture", MediaType: "tv"})
			if err != nil {
				t.Fatal(err)
			}
			ch, err := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: uuid.New(), Name: "fixture", ChannelType: "webhook", ConfigEncrypted: []byte("fixture")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Exec(ctx, `INSERT INTO notification_batch(id,media_id,channel_id,state,deadline) VALUES($1,$2,$3,$4,clock_timestamp())`, uuid.New(), m.ID, ch.ID, state); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			if err = postgres.ActivateRestoreIsolation(ctx, p, id); err != nil {
				t.Fatal(err)
			}
			r := postgres.RestoreReconciliation{RestoreID: id, Operator: "operator", DownloaderEvidence: "checked", FilesystemEvidence: "checked", OrphanEvidence: "checked"}
			if err = postgres.ReconcileRestoreIsolation(ctx, p, r); err == nil {
				t.Fatal("unsealed batch released without delivery")
			}
			if err = postgres.RequireRestoreReconciled(ctx, p); !errors.Is(err, postgres.ErrRestoreIsolated) {
				t.Fatal(err)
			}
			var n int
			if err = p.QueryRow(ctx, `SELECT count(*) FROM outbox_delivery`).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestRestoreIsolationBlocksBackgroundDownloadTasks(t *testing.T) {
	for _, state := range []string{"submitting", "unknown", "tracking"} {
		t.Run(state, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE download_task SET state=$1 WHERE id=$2`, state, f.taskID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO job(id,kind,state) VALUES($1,'download_submission','dead')`, uuid.New()); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			if err := postgres.ActivateRestoreIsolation(ctx, f.pool, id); err != nil {
				t.Fatal(err)
			}
			r := postgres.RestoreReconciliation{RestoreID: id, Operator: "operator", DownloaderEvidence: "checked", FilesystemEvidence: "checked", OrphanEvidence: "checked"}
			if err := postgres.ReconcileRestoreIsolation(ctx, f.pool, r); err == nil {
				t.Fatal("background download task released with dead job")
			}
			if err := postgres.RequireRestoreReconciled(ctx, f.pool); !errors.Is(err, postgres.ErrRestoreIsolated) {
				t.Fatal(err)
			}
		})
	}
}

func TestRestoreIsolationPersistsAcrossDataDirectories(t *testing.T) {
	p, dsn := bootstrapTestPool(t)
	ctx := context.Background()
	if err := postgres.RequireRestoreReconciled(ctx, p); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := postgres.ActivateRestoreIsolation(ctx, p, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM restore_isolation_audit`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("activation audit: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		cfg := config.NewDefault()
		cfg.DataDir = dir
		cfg.Database.URL = dsn
		cfg.Server.HTTPAddr = "127.0.0.1:0"
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = a.Run(ctx)
		_ = a.Close()
		if !errors.Is(err, postgres.ErrRestoreIsolated) {
			t.Fatalf("runtime escaped fence: %v", err)
		}
		if _, err = os.Stat(filepath.Join(dir, "media")); !os.IsNotExist(err) {
			t.Fatalf("root I/O occurred: %v", err)
		}
	}
	r := postgres.RestoreReconciliation{RestoreID: id, Operator: "test operator", DownloaderEvidence: "all torrents compared", FilesystemEvidence: "all files compared", OrphanEvidence: "no external orphans"}
	if err := postgres.ReconcileRestoreIsolation(ctx, p, postgres.RestoreReconciliation{RestoreID: id}); err == nil {
		t.Fatal("empty evidence accepted")
	}
	_, err := p.Exec(ctx, `INSERT INTO job(id,kind,state) VALUES($1,'restore-test','queued')`, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.ReconcileRestoreIsolation(ctx, p, r); err == nil {
		t.Fatal("queued R3 replay allowed")
	}
	if err = postgres.RequireRestoreReconciled(ctx, p); !errors.Is(err, postgres.ErrRestoreIsolated) {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `UPDATE job SET state='dead' WHERE kind='restore-test'`); err != nil {
		t.Fatal(err)
	}
	if err = postgres.ReconcileRestoreIsolation(ctx, p, r); err != nil {
		t.Fatal(err)
	}
	if err = postgres.RequireRestoreReconciled(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `DELETE FROM restore_isolation_audit`); err == nil {
		t.Fatal("audit deletion allowed")
	}
	if err = postgres.ReconcileRestoreIsolation(ctx, p, r); err == nil {
		t.Fatal("old release accepted")
	}
}
