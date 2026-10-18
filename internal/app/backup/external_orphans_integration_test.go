package backup_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" // BitTorrent v1 identity, not a security primitive.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/app/backup"
	"github.com/dalamudx/PulseMedia/internal/config"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt in with PULSE_M4_EXTERNAL_ORPHANS=1, PULSE_M4_SERVICES_DIR and an explicit
// private PULSE_TEST_DATABASE_URL (scripts/with_podman_test_postgres.sh).
// Only self-generated private, trackerless, already-present payloads are used.
// SQL seeds the backup boundary and records an explicit offline operator decision;
// this drill exercises restore/isolation, not acquisition or organization workers.
func TestM4RealExternalOrphanRestore(t *testing.T) {
	if os.Getenv("PULSE_M4_EXTERNAL_ORPHANS") != "1" {
		t.Skip("explicit external-orphan recovery drill opt-in required")
	}
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || filepath.Dir(root) != "/data/temp" || !strings.HasPrefix(filepath.Base(root), "pulse-m4-services.") {
		t.Fatal("canonical owned M4 service root required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Check both labels AND actual RW bind sources before creating any fixture.
	for _, kind := range []string{"qbittorrent", "transmission"} {
		verifyOrphanService(t, ctx, root, kind)
	}
	for _, kind := range []string{"qbittorrent", "transmission"} {
		t.Run(kind, func(t *testing.T) { drillExternalOrphan(t, ctx, root, kind) })
	}
}

func drillExternalOrphan(t *testing.T, ctx context.Context, root, kind string) {
	t.Helper()
	work, err := os.MkdirTemp("/data/temp", "pulse-m4-restore.")
	orphanMust(t, err)
	t.Cleanup(func() {
		orphanMust(t, os.RemoveAll(work))
		if _, err := os.Stat(work); !os.IsNotExist(err) {
			t.Error("owned restore scratch cleanup unconfirmed", work)
		}
	})
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, work)
	}
	put := func(name string, value []byte) string {
		p := filepath.Join(work, name)
		orphanMust(t, os.WriteFile(p, value, 0600))
		return p
	}
	sourceDir, restoreDir := filepath.Join(work, "source"), filepath.Join(work, "restore")
	orphanMust(t, os.Mkdir(sourceDir, 0700))
	orphanMust(t, os.Mkdir(restoreDir, 0700))
	key := make([]byte, 32)
	_, err = rand.Read(key)
	orphanMust(t, err)
	orphanMust(t, os.WriteFile(filepath.Join(sourceDir, "secret.key"), key, 0600))
	cipher, err := cryptoplatform.Encrypt(key, []byte("owned restore credential"))
	orphanMust(t, err)

	cluster := pgtest.GetCluster(t)
	dsn := cluster.NewTestDB(t)
	migrations, err := filepath.Abs("../../../migrations")
	orphanMust(t, err)
	_, err = postgres.Migrate(ctx, dsn, migrations)
	orphanMust(t, err)
	source, err := pgxpool.New(ctx, dsn)
	orphanMust(t, err)
	t.Cleanup(source.Close)
	q := db.New(source)
	probe, err := os.MkdirTemp(filepath.Join(root, "downloads"), "m4-restore.")
	orphanMust(t, err)
	var owned []*orphanTask
	// Early authentication/setup failures still own an empty fixture directory.
	t.Cleanup(func() {
		if len(owned) == 0 {
			orphanMust(t, os.RemoveAll(probe))
		}
	})
	orphanMust(t, os.Chmod(probe, 0755))
	// LinuxServer downloaders run as 1000; all writes stay inside this new path.
	if os.Geteuid() == 0 {
		orphanMust(t, os.Chown(probe, 1000, 1000))
	}
	monitor := newOrphanMonitor(t, kind)
	var client ports.DownloadClient
	if kind == "qbittorrent" {
		client, err = qbittorrent.NewClient(monitor.server.URL, "", "", &http.Client{Timeout: 10 * time.Second})
	} else {
		client, err = transmission.NewClient(monitor.server.URL, "", "", &http.Client{Timeout: 10 * time.Second})
	}
	orphanMust(t, err)
	orphanMust(t, client.Test(ctx))
	clientID, indexerID, rootID, mediaID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: clientID, Name: "owned restore " + kind, ClientType: kind, Url: monitor.server.URL, PasswordEncrypted: cipher, RemotePathMap: []byte(`{}`)})
	orphanMust(t, err)
	_, err = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexerID, Name: "owned restore source", Url: "https://example.invalid", ApiKeyEncrypted: cipher, Categories: []byte(`[]`)})
	orphanMust(t, err)
	_, err = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootID, Name: "owned restore files", MediaType: "movie", Path: probe})
	orphanMust(t, err)
	_, err = q.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "owned restore", MediaType: "movie"})
	orphanMust(t, err)
	_, err = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})
	orphanMust(t, err)
	tag := "pulsemedia:restore:" + uuid.NewString()
	// Register cleanup before the first possible Add, including uncertain results.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		monitor.allow.Store(true)
		clean := true
		for _, task := range owned {
			if !task.addAttempted {
				continue
			}
			actual, err := client.GetTorrent(cleanup, task.hash)
			if errors.Is(err, ports.ErrNotFound) {
				continue
			}
			if err != nil || actual.InfoHash != task.hash || path.Clean(actual.SavePath) != task.remote || actual.Name != task.name {
				t.Error("owned task cleanup identity unavailable; retained", probe)
				clean = false
				continue
			}
			if err := client.RemoveTorrent(cleanup, task.hash, false); err != nil {
				t.Error("owned task cleanup failed; retained", probe)
				clean = false
				continue
			}
			if !orphanWait(cleanup, func() bool { _, err := client.GetTorrent(cleanup, task.hash); return errors.Is(err, ports.ErrNotFound) }) {
				t.Error("owned task removal unconfirmed; retained", probe)
				clean = false
			}
		}
		if clean {
			if kind == "qbittorrent" {
				removeOrphanTag(t, cleanup, monitor.server.URL, tag)
			}
			orphanMust(t, os.RemoveAll(probe))
			if _, err := os.Stat(probe); !os.IsNotExist(err) {
				t.Error("owned downloader path cleanup unconfirmed", probe)
			} else {
				t.Logf("M4_RESTORE_EXTERNAL_CLEANUP client=%s owned_tasks_absent=true owned_files_absent=true owned_tags_absent=true", kind)
			}
		}
	})
	create := func(phase string) *orphanTask {
		t.Helper()
		task := newOrphanTask(t, probe, phase)
		owned = append(owned, task)
		if _, err := client.GetTorrent(ctx, task.hash); !errors.Is(err, ports.ErrNotFound) {
			t.Fatal("unique torrent hash was not absent before Add")
		}
		monitor.allow.Store(true)
		task.addAttempted = true
		err := client.AddTorrent(ctx, ports.AddTorrentParam{InfoHash: task.hash, TorrentBytes: task.torrent, SavePath: task.remote, Tags: tag})
		monitor.allow.Store(false)
		orphanMust(t, err) // Outcome unknown is never retried.
		assertOrphanComplete(t, ctx, client, task)
		candidateID := uuid.New()
		_, err = q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidateID, IndexerID: indexerID, Guid: task.hash, Title: task.name, LinkEncrypted: cipher, InfoHash: pgtype.Text{String: task.hash, Valid: true}, SizeBytes: int64(len(task.payload)), QualityProfile: []byte(`{}`), SourceConfigVersion: 1})
		orphanMust(t, err)
		_, err = q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: task.id, OperationID: task.operation, ClientID: clientID, CandidateID: candidateID, InfoHash: task.hash, State: "tracking", SavePath: task.remote})
		orphanMust(t, err)
		_, err = source.Exec(ctx, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,$4,$5,$6)`, task.fileID, rootID, task.rel, len(task.payload), task.stat.ModTime().UnixNano(), task.fingerprint)
		orphanMust(t, err)
		_, err = source.Exec(ctx, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, task.fileID, unitID)
		orphanMust(t, err)
		return task
	}
	before := create("before")
	identity, err := age.GenerateX25519Identity()
	orphanMust(t, err)
	o := backup.Options{DataDir: sourceDir, ConnectionFile: put("source.conn", []byte(dsn)), Archive: filepath.Join(work, "dump.age"), KeyArchive: filepath.Join(work, "key.age"), Recipient: identity.Recipient().String(), IdentityFile: put("identity", []byte(identity.String()+"\n")), Target: "pulse_restore_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Migrations: migrations}
	orphanMust(t, backup.Export(ctx, o))
	after := create("after")
	if monitor.adds.Load() != 2 || monitor.removes.Load() != 0 {
		t.Fatal("unexpected fixture mutation count")
	}
	o.DataDir = restoreDir
	// Cleanup the explicit target even if restore fails after creating it.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, err := source.Exec(cleanup, "DROP DATABASE IF EXISTS "+pgx.Identifier{o.Target}.Sanitize()+" WITH (FORCE)")
		orphanMust(t, err)
		var exists bool
		orphanMust(t, source.QueryRow(cleanup, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", o.Target).Scan(&exists))
		if exists {
			t.Error("owned restore database cleanup unconfirmed")
		}
	})
	orphanMust(t, backup.Restore(ctx, o))
	u, err := url.Parse(dsn)
	orphanMust(t, err)
	u.Path, u.RawPath = "/"+o.Target, ""
	restoredDSN := u.String()
	o.ConnectionFile = put("restored.conn", []byte(restoredDSN))
	restored, err := pgxpool.New(ctx, restoredDSN)
	orphanMust(t, err)
	t.Cleanup(restored.Close)
	restoredKey, err := os.ReadFile(filepath.Join(restoreDir, "secret.key"))
	orphanMust(t, err)
	if !bytes.Equal(key, restoredKey) {
		t.Fatal("restored main key differs")
	}
	var stored []byte
	orphanMust(t, restored.QueryRow(ctx, `SELECT password_encrypted FROM download_client WHERE id=$1`, clientID).Scan(&stored))
	plain, err := cryptoplatform.Decrypt(restoredKey, stored)
	if err != nil || string(plain) != "owned restore credential" {
		t.Fatal("restored main key cannot decrypt downloader credential")
	}
	s, err := backup.Inspect(ctx, o)
	orphanMust(t, err)
	if !s.Isolated || s.RestoreID == nil {
		t.Fatal("restored database was published without isolation")
	}
	for _, dir := range []string{restoreDir, filepath.Join(work, "alternate")} {
		orphanMust(t, os.MkdirAll(dir, 0700))
		if dir != restoreDir {
			orphanMust(t, os.WriteFile(filepath.Join(dir, "secret.key"), restoredKey, 0600))
		}
		cfg := config.NewDefault()
		cfg.DataDir, cfg.Database.URL, cfg.Server.HTTPAddr, cfg.LogLevel = dir, restoredDSN, "127.0.0.1:0", "error"
		a, err := app.New(cfg)
		orphanMust(t, err)
		if a.Readyz(ctx).Ready {
			_ = a.Close()
			t.Fatal("isolated App was ready")
		}
		runErr := a.Run(ctx)
		orphanMust(t, a.Close())
		if !errors.Is(runErr, postgres.ErrRestoreIsolated) {
			t.Fatal("App startup escaped database-wide restore fence")
		}
		if _, err := os.Stat(filepath.Join(dir, "media")); !os.IsNotExist(err) {
			t.Fatal("isolated App prepared media roots")
		}
	}
	// This transaction and both downloader inventories are observation-only.
	tx, err := restored.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	orphanMust(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	list, err := client.ListTorrents(ctx, tag)
	orphanMust(t, err)
	if len(list) != 2 {
		t.Fatal("real owned task inventory must contain pre/post backup tasks")
	}
	var orphanHashes []string
	for _, actual := range list {
		if actual.InfoHash != before.hash && actual.InfoHash != after.hash {
			t.Fatal("foreign task in uniquely tagged inventory")
		}
		var known bool
		orphanMust(t, tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_task WHERE client_id=$1 AND info_hash=$2)`, clientID, actual.InfoHash).Scan(&known))
		if !known {
			orphanHashes = append(orphanHashes, actual.InfoHash)
		}
	}
	if len(orphanHashes) != 1 || orphanHashes[0] != after.hash {
		t.Fatal("post-backup external orphan was not identified exactly")
	}
	var fileID uuid.UUID
	var fingerprint string
	orphanMust(t, tx.QueryRow(ctx, `SELECT f.id,f.file_fingerprint FROM library_file f JOIN library_file_unit m ON m.file_id=f.id AND m.unit_id=$2 AND m.retired_at IS NULL WHERE f.root_id=$1 AND f.rel_path=$3 AND f.retired_at IS NULL`, rootID, unitID, before.rel).Scan(&fileID, &fingerprint))
	if fileID != before.fileID || fingerprint != before.fingerprint {
		t.Fatal("pre-backup file identity/mapping changed")
	}
	var missing bool
	orphanMust(t, tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM library_file f JOIN library_file_unit m ON m.file_id=f.id WHERE f.root_id=$1 AND f.rel_path=$2)`, rootID, after.rel).Scan(&missing))
	if !missing {
		t.Fatal("post-backup file mapping unexpectedly restored")
	}
	orphanMust(t, tx.Commit(ctx))
	assertOrphanFiles(t, before, after)
	r := postgres.RestoreReconciliation{RestoreID: *s.RestoreID, Operator: "M4 owned external orphan drill", DownloaderEvidence: "inventory observed; pre-backup tracking task unresolved", FilesystemEvidence: "pre-backup UUID/fingerprint/mapping retained; post-backup physical file missing mapping", OrphanEvidence: "post-backup hash identified; awaiting manual disposal"}
	if backup.Reconcile(ctx, o, r) == nil {
		t.Fatal("unresolved restored download task allowed release")
	}
	if monitor.adds.Load() != 2 || monitor.removes.Load() != 0 || monitor.denied.Load() != 0 {
		t.Fatal("restore, App startup or inventories attempted automatic Add/delete")
	}
	// Explicit operator decision: remove only the verified owned orphan TASK,
	// retain its payload and missing mapping for manual review; never infer import.
	assertOrphanComplete(t, ctx, client, after)
	monitor.allow.Store(true)
	err = client.RemoveTorrent(ctx, after.hash, false)
	monitor.allow.Store(false)
	orphanMust(t, err)
	if !orphanWait(ctx, func() bool { _, err := client.GetTorrent(ctx, after.hash); return errors.Is(err, ports.ErrNotFound) }) {
		t.Fatal("manual owned orphan disposal was not observed")
	}
	assertOrphanFiles(t, before, after)
	if backup.Reconcile(ctx, o, r) == nil {
		t.Fatal("orphan disposal alone released unresolved pre-backup task")
	}
	assertOrphanComplete(t, ctx, client, before)
	// An offline operator accepts this exact completed task using the real file
	// list and bytes above. This explicit decision is not an automatic writeback.
	result, err := restored.Exec(ctx, `UPDATE download_task SET state='completed',progress=1,total_bytes=$4,downloaded_bytes=$4,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND info_hash=$2 AND save_path=$3 AND state='tracking'`, before.id, before.hash, before.remote, len(before.payload))
	orphanMust(t, err)
	if result.RowsAffected() != 1 {
		t.Fatal("manual restored task reconciliation changed unexpected rows")
	}
	var pending int
	orphanMust(t, restored.QueryRow(ctx, `SELECT count(*) FROM download_task WHERE state IN ('submitting','unknown','tracking')`).Scan(&pending))
	if pending != 0 {
		t.Fatal("unresolved restored downloads remain")
	}
	r.DownloaderEvidence = fmt.Sprintf("%s pre-backup task %s hash %s: real complete file-list and payload verified; offline operator recorded completed; no unresolved tasks", kind, before.id, before.hash)
	r.FilesystemEvidence = fmt.Sprintf("file %s mapping to unit %s retained; all device/inode/mtime/size/SHA256 unchanged; post-backup %s missing mapping retained for manual review, no imported fact created", before.fileID, unitID, after.rel)
	r.OrphanEvidence = fmt.Sprintf("post-backup hash %s absent from restored DB; operator removed only owned task with deleteFiles=false; absence observed; payload retained unchanged", after.hash)
	orphanMust(t, backup.Reconcile(ctx, o, r))
	s, err = backup.Inspect(ctx, o)
	orphanMust(t, err)
	if s.Isolated {
		t.Fatal("fully reconciled database stayed isolated")
	}
	orphanMust(t, postgres.RequireRestoreReconciled(ctx, restored))
	var evidence string
	orphanMust(t, restored.QueryRow(ctx, `SELECT evidence FROM restore_isolation_audit WHERE restore_id=$1 AND action='reconcile' AND operator_name=$2`, r.RestoreID, r.Operator).Scan(&evidence))
	for _, expected := range []string{r.DownloaderEvidence, r.FilesystemEvidence, r.OrphanEvidence} {
		if !strings.Contains(evidence, expected) {
			t.Fatal("actual reconciliation evidence missing from immutable audit")
		}
	}
	// Source database still has both original rows and mappings. No source rewrite,
	// no synthetic mapping/import for the retained orphan, no new jobs or facts.
	for _, probe := range []struct {
		pool            *pgxpool.Pool
		tasks, mappings int
	}{{source, 2, 2}, {restored, 1, 1}} {
		var tasks, mappings, jobs, facts int
		orphanMust(t, probe.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM job),(SELECT count(*) FROM unit_fact)`).Scan(&tasks, &mappings, &jobs, &facts))
		if tasks != probe.tasks || mappings != probe.mappings || jobs != 0 || facts != 0 {
			t.Fatal("restore/reconcile rewrote source or invented work/file facts")
		}
	}
	assertOrphanFiles(t, before, after)
	if monitor.adds.Load() != 2 || monitor.removes.Load() != 1 || monitor.denied.Load() != 0 {
		t.Fatal("unexpected downloader mutations during recovery")
	}
	t.Logf("M4_RESTORE_EXTERNAL_PASSED client=%s pre_hash=%s orphan_hash=%s adds=2 manual_remove=1 automatic_mutations=0 orphan_absent=true missing_mapping_retained=true file_uuid_preserved=true device_inode_mtime_size_sha256_preserved=true main_key_decrypt=true app_fenced_two_dirs=true unresolved_release_rejected=true audit_verified=true", kind, before.hash, after.hash)
}

type orphanTask struct {
	id, operation, fileID                       uuid.UUID
	hash, name, remote, rel, local, fingerprint string
	payload, torrent                            []byte
	stat                                        os.FileInfo
	addAttempted                                bool
}

func newOrphanTask(t *testing.T, probe, phase string) *orphanTask {
	t.Helper()
	task := &orphanTask{id: uuid.New(), operation: uuid.New(), fileID: uuid.New()}
	dir := filepath.Join(probe, phase)
	orphanMust(t, os.Mkdir(dir, 0755))
	if os.Geteuid() == 0 {
		orphanMust(t, os.Chown(dir, 1000, 1000))
	}
	task.name = "PulseMedia-restore-" + uuid.NewString() + ".mkv"
	task.payload = bytes.Repeat([]byte(task.name), 32)
	task.rel, task.local = path.Join(phase, task.name), filepath.Join(dir, task.name)
	task.remote = path.Join("/downloads", filepath.Base(probe), phase)
	orphanMust(t, os.WriteFile(task.local, task.payload, 0644))
	if os.Geteuid() == 0 {
		orphanMust(t, os.Chown(task.local, 1000, 1000))
	}
	var err error
	task.stat, err = os.Stat(task.local)
	orphanMust(t, err)
	st, ok := task.stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("Linux stat identity required")
	}
	task.fingerprint = fmt.Sprintf("stat.v1:%d:%d:%d", st.Ino, task.stat.Size(), task.stat.ModTime().UnixNano())
	piece := sha1.Sum(task.payload)
	info := []byte(fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi65536e6:pieces20:", len(task.payload), len(task.name), task.name))
	info = append(info, piece[:]...)
	info = append(info, []byte("7:privatei1ee")...)
	hash := sha1.Sum(info)
	task.hash = hex.EncodeToString(hash[:])
	task.torrent = append(append([]byte("d4:info"), info...), 'e')
	return task
}

func assertOrphanFiles(t *testing.T, tasks ...*orphanTask) {
	t.Helper()
	for _, task := range tasks {
		current, err := os.Stat(task.local)
		orphanMust(t, err)
		got, err := os.ReadFile(task.local)
		orphanMust(t, err)
		if !os.SameFile(task.stat, current) || task.stat.Size() != current.Size() || !task.stat.ModTime().Equal(current.ModTime()) || sha256.Sum256(got) != sha256.Sum256(task.payload) {
			t.Fatal("file physical identity or content changed", task.rel)
		}
	}
}

func assertOrphanComplete(t *testing.T, ctx context.Context, client ports.DownloadClient, task *orphanTask) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if !orphanWait(wait, func() bool {
		v, err := client.GetTorrent(wait, task.hash)
		// These tasks verify already-present data. qBittorrent's downloaded field
		// counts peer traffic; completion is proven by progress + full file list.
		return err == nil && v.InfoHash == task.hash && v.Name == task.name && path.Clean(v.SavePath) == task.remote && v.Progress == 1 && v.TotalBytes == int64(len(task.payload))
	}) {
		t.Fatal("real task identity/completion was not observed")
	}
	files, err := client.(ports.DownloadFileInventoryClient).GetTorrentFiles(ctx, task.hash)
	orphanMust(t, err)
	if len(files) != 1 || files[0].Name != task.name || files[0].Progress != 1 || files[0].SizeBytes != int64(len(task.payload)) {
		t.Fatal("real complete file inventory differs")
	}
}

func orphanWait(ctx context.Context, observe func() bool) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if observe() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func verifyOrphanService(t *testing.T, ctx context.Context, root, kind string) {
	t.Helper()
	raw, err := exec.CommandContext(ctx, "podman", "inspect", "--format", `{{json .Config.Labels}}`, "pulsemedia-m4-"+kind).Output()
	orphanMust(t, err)
	var labels map[string]string
	orphanMust(t, json.Unmarshal(raw, &labels))
	if labels["io.pulsemedia.state"] != root {
		t.Fatal("downloader ownership label mismatch")
	}
	raw, err = exec.CommandContext(ctx, "podman", "inspect", "--format", `{{json .Mounts}}`, "pulsemedia-m4-"+kind).Output()
	orphanMust(t, err)
	var mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	orphanMust(t, json.Unmarshal(raw, &mounts))
	for _, destination := range []string{"/config", "/downloads"} {
		expected := filepath.Join(root, kind)
		if destination == "/downloads" {
			expected = filepath.Join(root, "downloads")
		}
		found := false
		for _, mount := range mounts {
			if mount.Destination == destination {
				canonical, err := filepath.EvalSymlinks(mount.Source)
				if err != nil || canonical != expected || mount.Source != expected || mount.Type != "bind" || !mount.RW {
					t.Fatal("downloader bind ownership mismatch")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("required downloader bind absent")
		}
	}
}

type orphanMonitor struct {
	server                *httptest.Server
	allow                 atomic.Bool
	adds, removes, denied atomic.Int32
}

func newOrphanMonitor(t *testing.T, kind string) *orphanMonitor {
	t.Helper()
	endpoint := "http://127.0.0.1:18085"
	if kind == "transmission" {
		endpoint = "http://127.0.0.1:19091"
	}
	target, err := url.Parse(endpoint)
	orphanMust(t, err)
	m := &orphanMonitor{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) }, Transport: transport, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "owned downloader unavailable", http.StatusBadGateway)
	}}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		add, remove, mutation := r.URL.Path == "/api/v2/torrents/add", r.URL.Path == "/api/v2/torrents/delete", false
		if kind == "transmission" && r.Method == http.MethodPost {
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err != nil {
				http.Error(w, "request unavailable", 400)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var rpc struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &rpc) != nil {
				http.Error(w, "invalid RPC", 400)
				return
			}
			add, remove = rpc.Method == "torrent-add", rpc.Method == "torrent-remove"
			mutation = rpc.Method != "torrent-get" && rpc.Method != "session-get" && rpc.Method != "session-stats"
		} else if kind == "qbittorrent" {
			// Login is required even for local auth bypass; it grants no task mutation.
			mutation = r.Method != http.MethodGet && r.URL.Path != "/api/v2/auth/login"
		}
		if add {
			m.adds.Add(1)
		}
		if remove {
			m.removes.Add(1)
		}
		if mutation && !m.allow.Load() {
			m.denied.Add(1)
			http.Error(w, "recovery observation forbids mutation", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(m.server.Close)
	return m
}

// qBittorrent retains global tags after removing the final torrent. Remove only
// this run's unique tag, after every owned task has been observed absent.
func removeOrphanTag(t *testing.T, ctx context.Context, endpoint, tag string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/v2/torrents/deleteTags", strings.NewReader(url.Values{"tags": {tag}}.Encode()))
	orphanMust(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	orphanMust(t, err)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("owned tag cleanup failed")
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v2/torrents/tags", nil)
	orphanMust(t, err)
	response, err = client.Do(request)
	orphanMust(t, err)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatal("owned tag cleanup observation failed")
	}
	var tags []string
	orphanMust(t, json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tags))
	for _, actual := range tags {
		if actual == tag {
			t.Fatal("owned tag remains after task cleanup")
		}
	}
}

func orphanMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		// DB/client errors may contain connection strings; never print them.
		_, file, line, _ := runtime.Caller(1)
		t.Fatalf("external-orphan drill operation failed at %s:%d (details withheld)", filepath.Base(file), line)
	}
}
