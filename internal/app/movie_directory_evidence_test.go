package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	storedownload "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMovieDirectoryEvidenceAtomicAndScoped(t *testing.T) {
	for _, mode := range []string{"multiple_videos", "partial_sidecar", "escape", "changed_config", "changed_observation", "job_tail", "audit_tail", "valid"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil {
				t.Fatal(e)
			}
			f.taskID = task.ID
			hash := strings.Repeat("a", 40)
			var filesRead atomic.Bool
			directory := filepath.Join(f.target, "release")
			files := []map[string]any{{"index": 0, "name": "release/movie.mkv", "size": 200, "progress": 1}, {"index": 1, "name": "release/subtitle.srt", "size": 8, "progress": 1}}
			switch mode {
			case "multiple_videos":
				files[1]["name"] = "release/another-feature.mkv"
			case "partial_sidecar":
				files[1]["progress"] = .5
			case "escape":
				files[1]["name"] = "../subtitle.srt"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					_, _ = io.WriteString(w, "Ok.")
				case "/api/v2/torrents/info":
					total := 208
					if mode == "changed_observation" && filesRead.Load() {
						total++
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "name": "Movie.2020.1080p.BluRay", "save_path": f.target, "content_path": directory, "progress": 1, "size": total, "downloaded": total, "state": "uploading"}})
				case "/api/v2/torrents/files":
					filesRead.Store(true)
					if mode == "changed_config" {
						if _, e := f.pool.Exec(ctx, "UPDATE download_client SET username='changed' WHERE id=$1", f.client); e != nil {
							t.Error(e)
						}
					}
					_ = json.NewEncoder(w).Encode(files)
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			encrypted, e := crypto.Encrypt(outboxTestKey, []byte("synthetic"))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE download_client SET url=$2,password_encrypted=$3,remote_path_map='{}' WHERE id=$1", f.client, server.URL, encrypted); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE download_task SET state='tracking',progress=0,total_bytes=0,downloaded_bytes=0,content_path=NULL,organization_state='pending',info_hash=$2,save_path=$3 WHERE id=$1", f.taskID, hash, f.target); e != nil {
				t.Fatal(e)
			}
			snapshot := func() string {
				var raw []byte
				if e := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('task',(SELECT to_jsonb(t) FROM download_task t WHERE id=$1),'inventory',(SELECT jsonb_agg(to_jsonb(i)) FROM download_file_inventory i),'jobs',(SELECT jsonb_agg(to_jsonb(j))FROM job j),'facts',(SELECT jsonb_agg(to_jsonb(f))FROM unit_fact f),'candidate',(SELECT jsonb_agg(to_jsonb(c))FROM release_candidate c))`, f.taskID).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				return string(raw)
			}
			before := snapshot()
			if mode == "job_tail" || mode == "audit_tail" {
				table := "job"
				condition := "NEW.kind='download_organization'"
				if mode == "audit_tail" {
					table = "audit_log"
					condition = "NEW.action='download.completed'"
				}
				_, e = f.pool.Exec(ctx, "CREATE FUNCTION directory_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF "+condition+" THEN RAISE EXCEPTION 'synthetic tail failure'; END IF; RETURN NEW;END$$;CREATE TRIGGER directory_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION directory_tail()")
				if e != nil {
					t.Fatal(e)
				}
			}
			manager := storedownload.NewManager(f.pool, f.gen, outboxTestKey)
			manager.SetClientFactory(func(_, url, username, password string) (ports.DownloadClient, error) {
				return qbittorrent.NewClient(url, username, password, nil)
			})
			result, e := manager.ObserveTask(ctx, f.op)
			if !filesRead.Load() {
				t.Fatal("fixture did not reach full inventory")
			}
			if mode != "valid" {
				if e == nil {
					t.Fatal("unsafe directory accepted", result)
				}
				if mode == "escape" && !errors.Is(e, ports.ErrBadResponse) {
					t.Fatal("wrong native path error", e)
				}
				if mode != "escape" && mode != "job_tail" && mode != "audit_tail" && !errors.Is(e, storedownload.ErrReconcilePending) {
					t.Fatal("wrong observation error", e)
				}
				if after := snapshot(); after != before {
					t.Fatal("partial directory state published")
				}
				return
			}
			if e != nil || result.State != "completed" || result.ContentPath.String != filepath.Join(directory, "movie.mkv") {
				t.Fatal("valid directory", result, e)
			}
			inv, e := f.q.GetDownloadFileInventory(ctx, f.taskID)
			if e != nil || inv.ContentPath != directory || inv.TotalBytes != 208 {
				t.Fatal("original directory proof missing", e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE download_file_inventory SET total_bytes=200 WHERE task_id=$1", f.taskID); e == nil {
				t.Fatal("inventory mutable")
			}
			atMovieInventoryMigration(t, f)
			if _, e = postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), "../../migrations"); e == nil {
				t.Fatal("movie history discarded")
			}
			if version, e := postgres.Migrate(ctx, f.pool.Config().ConnConfig.ConnString(), "../../migrations"); e != nil || version != postgres.CurrentMigrationVersion {
				t.Fatal("history upgrade", version, e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE download_task SET operation_id=gen_random_uuid() WHERE id=$1", f.taskID); e == nil {
				t.Fatal("inventory scope detached")
			}
			reviseFrozenMovieDirectory(t, ctx, f)
		})
	}
}
func TestMovieInventoryEmptyMigrationRoundTrip(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	dsn := f.pool.Config().ConnConfig.ConnString()
	atMovieInventoryMigration(t, f)
	if version, e := postgres.Rollback(ctx, dsn, "../../migrations"); e != nil || version != 54 {
		t.Fatal(version, e)
	}
	if version, e := postgres.Migrate(ctx, dsn, "../../migrations"); e != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal(version, e)
	}
}

func atMovieInventoryMigration(t *testing.T, f *organizationFixture) {
	t.Helper()
	ctx := context.Background()
	dsn := f.pool.Config().ConnConfig.ConnString()
	for version := postgres.CurrentMigrationVersion; version > 55; version-- {
		if next, err := postgres.Rollback(ctx, dsn, "../../migrations"); err != nil || next != int64(version-1) {
			t.Fatalf("rollback unrelated migration %d: version=%d error=%v", version, next, err)
		}
	}
	if err := postgres.CheckSchemaVersion(ctx, f.pool, 55); err != nil {
		t.Fatal(err)
	}
}
