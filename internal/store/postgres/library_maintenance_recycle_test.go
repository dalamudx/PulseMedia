package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/usecase/librarymaintenance"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type maintenanceRetiredFixture struct {
	pool           *pgxpool.Pool
	actor          *ports.Actor
	dir, recycleID string
	root           uuid.UUID
	store          *postgres.LibraryMaintenanceStore
}

func maintenanceBaseline(t *testing.T, f maintenanceRetiredFixture) {
	t.Helper()
	var exists bool
	if e := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id JOIN library_root r ON r.id=sf.root_id WHERE sf.root_id=$1 AND sr.generation=r.generation AND sf.completed_at IS NOT NULL AND sr.state='succeeded')`, f.root).Scan(&exists); e != nil {
		t.Fatal(e)
	}
	if exists {
		return
	}
	r, e := rootfs.Open(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	id, e := r.Identity()
	if e != nil {
		t.Fatal(e)
	}
	scan := uuid.New()
	for _, query := range []string{`INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded' FROM library_root WHERE id=$2`, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`} {
		args := []any{scan, f.root}
		if strings.Contains(query, "scan_finalization") {
			args = append(args, id.Device, id.Inode)
		}
		if _, e = f.pool.Exec(t.Context(), query, args...); e != nil {
			t.Fatal(e)
		}
	}
}
func maintenanceMovieRetired(t *testing.T) maintenanceRetiredFixture {
	t.Helper()
	f, r, _, sub := retirementFixture79(t)
	confirmRetirement79(t, f, r, sub)
	if e := r.ExecuteMovieUpgradeRetirement(t.Context(), f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if retirementStatus79(t, f) != "retired" {
		t.Fatal("real retirement did not complete")
	}
	out := maintenanceRetiredFixture{pool: f.p, actor: f.actor, dir: f.rootPath, recycleID: "movie:" + f.operation.String()}
	if e := f.p.QueryRow(t.Context(), `SELECT root_id FROM movie_upgrade_authority WHERE operation_id=$1`, f.operation).Scan(&out.root); e != nil {
		t.Fatal(e)
	}
	var e error
	out.store, e = postgres.NewLibraryMaintenanceStore(out.pool, rootfs.NewFilesystem(), idgen.New())
	if e != nil {
		t.Fatal(e)
	}
	maintenanceBaseline(t, out)
	return out
}
func maintenanceTVRetired(t *testing.T) maintenanceRetiredFixture {
	t.Helper()
	f := newTVUpgradeFixture(t)
	r := f.prepared(t)
	f.command(t, "send", r.OperationID, r.Version, "send")
	f.command(t, "observe", r.OperationID, r.Version, "observe")
	preview := f.command(t, "organization-preview", r.OperationID, r.Version, "org-preview")
	confirm := f.command(t, "organization-confirm", r.OperationID, preview.Version, "org-confirm")
	imported := f.command(t, "organize", r.OperationID, confirm.Version, "organize")
	preview = f.command(t, "retirement-preview", r.OperationID, imported.Version, "retire-preview")
	confirm = f.command(t, "retirement-confirm", r.OperationID, preview.Version, "retire-confirm")
	retired := f.command(t, "retire", r.OperationID, confirm.Version, "retire")
	if retired.Status != "retired" {
		t.Fatal(retired)
	}
	store, e := postgres.NewLibraryMaintenanceStore(f.pool, rootfs.NewFilesystem(), idgen.New())
	if e != nil {
		t.Fatal(e)
	}
	return maintenanceRetiredFixture{pool: f.pool, actor: f.actor, dir: f.rootPath, recycleID: "tv:" + r.OperationID.String(), root: f.tv, store: store}
}
func (f maintenanceRetiredFixture) item(t *testing.T) ports.LibraryRecycleItem {
	t.Helper()
	list, e := f.store.ListRecycle(t.Context(), f.actor, ports.LibraryRecycleQuery{Limit: 1})
	if e != nil || len(list.Items) != 1 || list.Items[0].RecycleID != f.recycleID {
		t.Fatal("read-only list", list, e)
	}
	return list.Items[0]
}
func (f maintenanceRetiredFixture) paths(t *testing.T) (string, string, string) {
	t.Helper()
	var original, recycled, replacement string
	e := f.pool.QueryRow(t.Context(), `SELECT v.original_rel,v.recycle_rel,n.rel_path FROM library_verified_recycle v JOIN library_file n ON n.id=v.replacement_file_id WHERE recycle_id=$1`, f.recycleID).Scan(&original, &recycled, &replacement)
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Join(f.dir, original), filepath.Join(f.dir, recycled), filepath.Join(f.dir, replacement)
}
func (f maintenanceRetiredFixture) ageMovieFixture(t *testing.T) {
	t.Helper()
	if !strings.HasPrefix(f.recycleID, "movie:") {
		t.Fatal("movie-only historical fixture")
	}
	// Simulate a restored historical backup after retention elapsed. Production
	// never updates this append-only result; the test explicitly owns this database.
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(t.Context(), `ALTER TABLE movie_upgrade_retirement_result DISABLE TRIGGER USER`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `WITH n AS(SELECT clock_timestamp() t) UPDATE movie_upgrade_retirement_result SET observed_at=n.t-interval '8 days',recorded_at=n.t-interval '8 days',retained_until=n.t-interval '1 day' FROM n`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `ALTER TABLE movie_upgrade_retirement_result ENABLE TRIGGER USER`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
}
func TestLibraryMaintenanceVerifiedRecycleRestorePG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			var f maintenanceRetiredFixture
			if kind == "movie" {
				f = maintenanceMovieRetired(t)
			} else {
				f = maintenanceTVRetired(t)
			}
			item := f.item(t)
			if item.AllocatedBytes != nil || item.LinkCount != nil || item.CanConfirm || item.Status != "retained" {
				t.Fatal("unmeasured list lied", item)
			}
			original, recycled, replacement := f.paths(t)
			oldBytes, e := os.ReadFile(recycled)
			if e != nil {
				t.Fatal(e)
			}
			newBytes, e := os.ReadFile(replacement)
			if e != nil {
				t.Fatal(e)
			}
			newStat, e := os.Stat(replacement)
			if e != nil {
				t.Fatal(e)
			}
			seeding := filepath.Join(f.dir, "retained-hardlink.mkv")
			if e = os.Link(recycled, seeding); e != nil {
				t.Fatal(e)
			}
			preview, e := f.store.PreviewRecycle(t.Context(), f.actor, ports.LibraryRecyclePreviewRequest{RecycleID: f.recycleID, Action: "restore"})
			if e != nil || preview == nil || !preview.CanConfirm || preview.AllocatedBytes == nil || preview.LinkCount == nil || *preview.LinkCount < 2 {
				t.Fatal("measured preview", preview, e)
			}
			out, e := librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, ports.LibraryMaintenanceCommand{Action: "restore", RecycleID: f.recycleID, ExpectedVersion: preview.Version, Confirmed: true}, "restore-original")
			if e != nil || out == nil || out.Status != "completed" || out.FileID == item.OldFileID {
				t.Fatal("restore", out, e)
			}
			got, e := os.ReadFile(original)
			if e != nil || !bytes.Equal(got, oldBytes) {
				t.Fatal("old bytes not restored", e)
			}
			got, e = os.ReadFile(replacement)
			after, e2 := os.Stat(replacement)
			if e != nil || e2 != nil || !bytes.Equal(got, newBytes) || !os.SameFile(after, newStat) {
				t.Fatal("replacement changed", e, e2)
			}
			got, e = os.ReadFile(seeding)
			if e != nil || !bytes.Equal(got, oldBytes) {
				t.Fatal("hardlink changed", e)
			}
			var valid bool
			e = f.pool.QueryRow(t.Context(), `SELECT old.retired_at IS NOT NULL AND n.retired_at IS NULL AND restored.retired_at IS NULL AND (SELECT count(*) FROM unit_fact WHERE source_file_id=restored.id AND stage='imported' AND revoked_at IS NULL)=1 AND (SELECT count(*) FROM unit_fact WHERE source_file_id=n.id AND revoked_at IS NULL)>0 FROM library_file old,library_file n,library_file restored WHERE old.id=$1 AND n.id=$2 AND restored.id=$3`, item.OldFileID, item.ReplacementFileID, out.FileID).Scan(&valid)
			if e != nil || !valid {
				t.Fatal("rederived facts or history", valid, e)
			}
			receipt, e := f.store.ReadCommand(t.Context(), f.actor, "restore-original")
			if e != nil || *receipt != *out {
				t.Fatal("receipt", receipt, e)
			}
			if status := f.item(t).Status; status != "restored" {
				t.Fatal(status)
			}
		})
	}
}
func TestLibraryMaintenanceRecycleRefusalPG(t *testing.T) {
	for _, kind := range []string{"before-retention", "target-collision", "symlink", "replacement-tamper"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceMovieRetired(t)
			original, recycled, replacement := f.paths(t)
			action := "restore"
			switch kind {
			case "before-retention":
				action = "cleanup"
			case "target-collision":
				if e := os.WriteFile(original, []byte("protected target"), 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Remove(recycled); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(replacement, recycled); e != nil {
					t.Fatal(e)
				}
			case "replacement-tamper":
				if e := os.WriteFile(replacement, []byte("changed new bytes"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.store.PreviewRecycle(t.Context(), f.actor, ports.LibraryRecyclePreviewRequest{RecycleID: f.recycleID, Action: action}); e == nil {
				t.Fatal("accepted", kind)
			}
			var n int
			if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM library_maintenance_permit`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}
func TestLibraryMaintenanceExpiredCleanupPG(t *testing.T) {
	f := maintenanceMovieRetired(t)
	f.ageMovieFixture(t)
	_, recycled, replacement := f.paths(t)
	seeding := filepath.Join(f.dir, "kept-seeding.mkv")
	if e := os.Link(recycled, seeding); e != nil {
		t.Fatal(e)
	}
	bytesBefore, e := os.ReadFile(seeding)
	if e != nil {
		t.Fatal(e)
	}
	newBytes, e := os.ReadFile(replacement)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.store.PreviewRecycle(t.Context(), f.actor, ports.LibraryRecyclePreviewRequest{RecycleID: f.recycleID, Action: "cleanup"})
	if e != nil || preview == nil || !preview.CanConfirm {
		t.Fatal(preview, e)
	}
	out, e := librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, ports.LibraryMaintenanceCommand{Action: "cleanup", RecycleID: f.recycleID, ExpectedVersion: preview.Version, Confirmed: true}, "cleanup-expired")
	if e != nil || out == nil || out.Status != "completed" {
		t.Fatal(out, e)
	}
	if _, e = os.Stat(recycled); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("retired link remains", e)
	}
	for p, want := range map[string][]byte{seeding: bytesBefore, replacement: newBytes} {
		got, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(got, want) {
			t.Fatal("protected bytes lost", p, e)
		}
	}
	if f.item(t).Status != "cleaned" {
		t.Fatal("cleanup status missing")
	}
	if _, e = librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, ports.LibraryMaintenanceCommand{Action: "cleanup", RecycleID: f.recycleID, ExpectedVersion: preview.Version, Confirmed: true}, "cleanup-expired"); e != nil {
		t.Fatal("replay", e)
	}
}
func TestLibraryMaintenanceCookieAppPG(t *testing.T) {
	f := maintenanceTVRetired(t)
	password := "MP30-32-owned-admin-42!"
	hashed, e := crypto.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	var username string
	if e = f.pool.QueryRow(t.Context(), `UPDATE app_user SET password_hash=$2 WHERE id=$1 RETURNING username`, f.actor.ID, hashed).Scan(&username); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	base, stop := startTVEpisodeUpgradeHTTPApp(t, ctx, f.pool.Config().ConnConfig.ConnString(), filepath.Join(t.TempDir(), "maintenance-app"))
	defer stop()
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	request := func(method, p string, body any, key string, status int) []byte {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, e := http.NewRequestWithContext(ctx, method, base+"/api/v1"+p, reader)
		if e != nil {
			t.Fatal(e)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", base)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, e := io.ReadAll(resp.Body)
		if e != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, p, resp.StatusCode, raw, e)
		}
		return raw
	}
	request("GET", "/library-maintenance/recycle?limit=1", nil, "", 401)
	request("POST", "/auth/login", map[string]string{"username": username, "password": password}, "", 200)
	var list ports.LibraryRecycleList
	if e = json.Unmarshal(request("GET", "/library-maintenance/recycle?limit=1", nil, "", 200), &list); e != nil || len(list.Items) != 1 {
		t.Fatal(list, e)
	}
	var filtered ports.LibraryRecycleList
	if e = json.Unmarshal(request("GET", "/library-maintenance/recycle?limit=1&kind=tv&q=%2EmKv", nil, "", 200), &filtered); e != nil || len(filtered.Items) != 1 || filtered.Items[0].RecycleID != f.recycleID {
		t.Fatal("Cookie filtered history", filtered, e)
	}
	if e = json.Unmarshal(request("GET", "/library-maintenance/recycle?limit=1&kind=movie", nil, "", 200), &filtered); e != nil || len(filtered.Items) != 0 {
		t.Fatal("Cookie type isolation", filtered, e)
	}
	request("GET", "/library-maintenance/recycle?kind=tv&kind=movie", nil, "", 400)
	var preview ports.LibraryRecycleItem
	if e = json.Unmarshal(request("POST", "/library-maintenance/recycle-previews", ports.LibraryRecyclePreviewRequest{RecycleID: f.recycleID, Action: "restore"}, "", 200), &preview); e != nil || !preview.CanConfirm {
		t.Fatal(preview, e)
	}
	raw := request("POST", "/library-maintenance/commands", ports.LibraryMaintenanceCommand{Action: "restore", RecycleID: f.recycleID, ExpectedVersion: preview.Version, Confirmed: true}, "cookie-restore", 200)
	var result ports.LibraryMaintenanceResult
	if e = json.Unmarshal(raw, &result); e != nil || result.Status != "completed" {
		t.Fatal(result, e)
	}
	again := request("GET", "/library-maintenance/commands/cookie-restore", nil, "", 200)
	if !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(again)) {
		t.Fatal("GET receipt changed")
	}
	request("GET", "/library-maintenance/recycle?limit=0", nil, "", 400)
	var rename ports.LibraryReorganizationPreview
	if e = json.Unmarshal(request("POST", "/library-maintenance/reorganization-previews", ports.LibraryReorganizationRequest{FileID: result.FileID, TargetRelativePath: "Maintenance/renamed.mkv"}, "", 200), &rename); e != nil || !rename.CanConfirm {
		t.Fatal(rename, e)
	}
	var renamed ports.LibraryMaintenanceResult
	if e = json.Unmarshal(request("POST", "/library-maintenance/commands", ports.LibraryMaintenanceCommand{Action: "reorganize", FileID: result.FileID, TargetRelativePath: rename.TargetRelativePath, ExpectedVersion: rename.Version, Confirmed: true}, "cookie-rename", 200), &renamed); e != nil || renamed.Status != "completed" {
		t.Fatal(renamed, e)
	}
	request("GET", "/library-maintenance/commands/cookie-rename", nil, "", 200)
}
