package postgres_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func transmissionMovieFixture(t *testing.T, lost bool, beforeFreeze ...func(*frozenUpgradeFixture)) *trackingFixture {
	t.Helper()
	f := &trackingFixture{}
	f.complete.Store(true)
	var save string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" || r.Method != "POST" {
			t.Error("wrong native endpoint")
			w.WriteHeader(400)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "native-admin" || password != "native-test-password" {
			t.Error("wrong frozen credentials")
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "movie-session")
			w.WriteHeader(409)
			return
		}
		var req struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		switch req.Method {
		case "torrent-add":
			f.adds.Add(1)
			var raw, directory string
			var labels []string
			_ = json.Unmarshal(req.Arguments["metainfo"], &raw)
			_ = json.Unmarshal(req.Arguments["download-dir"], &directory)
			_ = json.Unmarshal(req.Arguments["labels"], &labels)
			if len(req.Arguments) != 4 || raw != base64.StdEncoding.EncodeToString(f.proof.Bytes()) || directory != save || strings.Join(labels, ",") != "pulsemedia,pulsemedia:op:"+f.operation.String() {
				t.Error("original upgrade bytes/path/operation changed")
			}
			if lost {
				c, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = c.Close()
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrent-added": map[string]any{"hashString": f.proof.InfoHash(), "name": "Movie", "id": 7}}, "tag": 1})
		case "torrent-get":
			_, files := req.Arguments["ids"]
			if files {
				f.files.Add(1)
			} else {
				f.infos.Add(1)
			}
			if hook := f.onInfo.Load(); hook != nil {
				(*hook)()
			}
			hash := f.proof.InfoHash()
			if f.wrongHash.Load() {
				hash = strings.Repeat("f", 40)
			}
			size := f.proof.TotalBytes()
			done, status := size, 6
			if !f.complete.Load() {
				done, status = size/2, 4
			}
			row := map[string]any{"hashString": hash, "downloadDir": save, "labels": []string{"pulsemedia", "pulsemedia:op:" + f.operation.String()}, "status": status, "totalSize": size, "haveValid": done, "error": 0, "rateDownload": 123}
			if files {
				var ids []string
				_ = json.Unmarshal(req.Arguments["ids"], &ids)
				if len(ids) != 1 || ids[0] != f.proof.InfoHash() {
					t.Error("wrong original file query")
				}
				listing, stats := []any{}, []any{}
				for _, file := range f.proof.Files() {
					name := file.Path
					if f.wrongFile.Load() {
						name = "Other.Movie.2024.mkv"
					}
					listing = append(listing, map[string]any{"name": name, "length": file.Size, "bytesCompleted": file.Size})
					stats = append(stats, map[string]any{"bytesCompleted": file.Size, "wanted": false})
				}
				row["files"], row["fileStats"] = listing, stats
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []any{row}}, "tag": 1})
		default:
			t.Error("unexpected native mutation", req.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	f.nativeUpgradeFixture = nativeUpgrade(t, "Ok.", false, func(b *frozenUpgradeFixture) {
		b.exec(t, `UPDATE download_client SET client_type='transmission',url=$2 WHERE id=$1`, b.client, server.URL)
		// App startup must decrypt and migrate real encrypted configuration before
		// opening workers; the legacy fixture placeholder is not a credential.
		apiKey, err := crypto.Encrypt(bytes.Repeat([]byte{37}, 32), []byte("transmission-indexer-key"))
		if err != nil {
			t.Fatal(err)
		}
		b.exec(t, `UPDATE indexer SET api_key_encrypted=$2 WHERE id=$1`, b.source, apiKey)
		b.exec(t, `UPDATE release_candidate SET source_config_version=(SELECT config_version FROM indexer WHERE id=indexer_id) WHERE id=$1`, b.candidate)
		b.exec(t, `UPDATE indexer_query SET config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`)
		for _, hook := range beforeFreeze {
			hook(b)
		}
	})
	f.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		if kind != "transmission" {
			return nil, ports.ErrUnavailable
		}
		return transmission.NewClient(url, user, password, nil)
	})
	if e := f.p.QueryRow(t.Context(), `SELECT evidence->>'save_path' FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&save); e != nil {
		t.Fatal(e)
	}
	_, command, sender, request := sendJobFixtureFromNative(t, f.nativeUpgradeFixture)
	queued, e := command.QueueMovieUpgradeSend(t.Context(), f.actor, request, "transmission-explicit-send")
	if e != nil {
		t.Fatal(e)
	}
	result, e := sender.SendMovieUpgradeJob(t.Context(), f.actor, claimSendJob(t, f.nativeUpgradeFixture, queued), f.proof)
	want := "confirmed"
	if lost {
		want = "unknown"
	}
	if e != nil || result == nil || result.Status != want {
		t.Fatal(result, e)
	}
	if v, e := postgres.Migrate(t.Context(), f.dsn, tracking72MigrationDir(t)); e != nil || v != 72 {
		t.Fatal(v, e)
	}
	f.issuer = postgres.NewMovieUpgradeTrackingQueryVerifier()
	f.driver, e = download.NewMovieUpgradeNativeTrackingDriver(f.manager, f.issuer, torrent.Inspector{}, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	f.repo, e = postgres.NewMovieUpgradeNativeTrackingRepository(f.preparation, f.driver, f.issuer)
	if e != nil {
		t.Fatal(e)
	}
	return f
}

func TestTransmissionMovieUpgradeNativePG(t *testing.T) {
	for _, mode := range []string{"completed", "tracking", "wrong_hash", "wrong_file", "late_lease", "changed_config", "lost_ack"} {
		t.Run(mode, func(t *testing.T) {
			f := transmissionMovieFixture(t, mode == "lost_ack")
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			switch mode {
			case "lost_ack":
				issuer := postgres.NewMovieUpgradeNativeQueryVerifier()
				driver, e := download.NewMovieUpgradeNativeObservationDriver(f.manager, issuer, nativeUpgradeClock{})
				if e != nil {
					t.Fatal(e)
				}
				observer, e := postgres.NewMovieUpgradeNativeObservationRepository(f.preparation, driver, issuer)
				if e != nil {
					t.Fatal(e)
				}
				out, e := observer.ObserveMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "transmission-observe", "transmission-observed")
				if e != nil || out == nil || out.Outcome != "exact_match" || out.Disposition != "manual_review" {
					t.Fatal(out, e)
				}
				if out, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "unknown-track", "unknown-result"); e == nil || out != nil {
					t.Fatal("unknown send tracked as confirmed", out, e)
				}
			case "late_lease":
				if v, e := postgres.Migrate(t.Context(), f.dsn, tracking73MigrationDir(t)); e != nil || v != 73 {
					t.Fatal(v, e)
				}
				job := &trackingJobFixture{trackingFixture: f}
				var e error
				job.runtime, e = postgres.NewMovieUpgradeTrackingRuntimeStore(f.preparation)
				if e != nil {
					t.Fatal(e)
				}
				job.jobRepo, e = postgres.NewMovieUpgradeNativeJobTrackingRepository(f.preparation, f.driver, f.issuer)
				if e != nil {
					t.Fatal(e)
				}
				job.queue = jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
				if n, e := job.runtime.ScheduleMovieUpgradeTracking(t.Context()); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				request := job.claim(t, time.Second)
				hook := func() { time.Sleep(1100 * time.Millisecond) }
				f.onInfo.Store(&hook)
				out, e := job.jobRepo.TrackMovieUpgradeJob(t.Context(), f.actor, request)
				if e == nil || out != nil || f.infos.Load() == 0 {
					t.Fatal("expired native job committed", out, e, f.infos.Load())
				}
			default:
				f.complete.Store(mode != "tracking")
				f.wrongHash.Store(mode == "wrong_hash")
				f.wrongFile.Store(mode == "wrong_file")
				if mode == "changed_config" {
					hook := func() { f.exec(t, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client) }
					f.onInfo.Store(&hook)
				}
				out, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "transmission-track", "transmission-result")
				if mode == "changed_config" {
					if e == nil || out != nil {
						t.Fatal("late native result committed", out, e)
					}
				} else {
					want := mode
					if strings.HasPrefix(mode, "wrong_") {
						want = "manual_review"
					}
					if e != nil || out == nil || out.Outcome != want {
						t.Fatal(out, e, want)
					}
					gets := f.infos.Load()
					again, e := f.repo.TrackMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), "transmission-track", "transmission-result")
					if e != nil || again == nil || *again != *out || f.infos.Load() != gets {
						t.Fatal("recovery repeated native I/O", again, e)
					}
				}
			}
			if mode != "changed_config" && before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("native observation changed original protected graph")
			}
			if f.adds.Load() != 1 || f.exports.Load() != 0 {
				t.Fatal("replay/fabricated export", f.adds.Load(), f.exports.Load())
			}
			if _, e := f.nativeUpgradeFixture.repo.SendMovieUpgrade(t.Context(), f.actor, f.operation, uuid.New(), f.proof, "transmission-replay"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
				t.Fatal("repeated send", e)
			}
		})
	}
}

// Build the complete original Transmission operation before any downstream
// authority. Real files and PG guards are used by the existing dedicated stores.
func transmissionMoviePhysicalFixture(t *testing.T) *organizationJobFixture {
	t.Helper()
	ctx := t.Context()
	base := t.TempDir()
	root, source := filepath.Join(base, "library"), filepath.Join(base, "downloads")
	for _, p := range []string{root, source} {
		if e := os.Mkdir(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("root"), 0600); e != nil {
		t.Fatal(e)
	}
	oldPath := filepath.Join(root, "original.mkv")
	if e := os.WriteFile(oldPath, bytes.Repeat([]byte("o"), 100), 0600); e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(oldPath)
	if e != nil {
		t.Fatal(e)
	}
	inode := stat.Sys().(*syscall.Stat_t).Ino
	native := transmissionMovieFixture(t, false, func(b *frozenUpgradeFixture) {
		b.exec(t, `UPDATE library_root SET path=$2 WHERE id=$1`, b.root, root)
		b.exec(t, `UPDATE library_root SET baseline_completed=true,health='online',marker_filename='.pulsemedia_root' WHERE id=$1`, b.root)
		mapping, _ := json.Marshal(map[string]string{root: source})
		b.exec(t, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, b.client, mapping)
		b.exec(t, `UPDATE library_file SET size_bytes=$2,mtime_ns=$3,file_fingerprint=$4 WHERE id=$1`, b.file, stat.Size(), stat.ModTime().UnixNano(), fmt.Sprintf("stat.v1:%d:%d:%d", inode, stat.Size(), stat.ModTime().UnixNano()))
		b.exec(t, `UPDATE unit_fact SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1`, b.fact)
		var fresh uuid.UUID
		if e := b.p.QueryRow(ctx, `INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) SELECT gen_random_uuid(),unit_id,'imported',source_file_id,root_id,quality_profile FROM unit_fact WHERE id=$1 RETURNING id`, b.fact).Scan(&fresh); e != nil {
			t.Fatal(e)
		}
		b.fact = fresh
		b.project(t)
	})
	if v, e := postgres.Migrate(ctx, native.dsn, organization74MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
	tracking := &trackingJobFixture{trackingFixture: native}
	tracking.runtime, e = postgres.NewMovieUpgradeTrackingRuntimeStore(native.preparation)
	if e != nil {
		t.Fatal(e)
	}
	tracking.jobRepo, e = postgres.NewMovieUpgradeNativeJobTrackingRepository(native.preparation, native.driver, native.issuer)
	if e != nil {
		t.Fatal(e)
	}
	tracking.queue = jobs.NewQueue(native.p, idgen.New(), nativeUpgradeClock{})
	if n, e := tracking.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	completed, e := tracking.jobRepo.TrackMovieUpgradeJob(ctx, native.actor, tracking.claim(t, 2*time.Minute))
	if e != nil || completed == nil || completed.Outcome != "completed" {
		t.Fatal(completed, e)
	}
	organization, e := postgres.NewMovieUpgradeOrganizationRepository(native.preparation)
	if e != nil {
		t.Fatal(e)
	}
	org := &organizationFixture{trackingJobFixture: tracking, organization: organization, result: completed}
	if v, e := postgres.Migrate(ctx, native.dsn, organization75MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
	auth, e := organization.AuthorizeMovieUpgradeOrganization(ctx, native.actor, org.command(t), "transmission-organization-authority")
	if e != nil {
		t.Fatal(e)
	}
	review, e := organization.ReadMovieUpgradeOrganization(ctx, native.actor, native.operation)
	if e != nil {
		t.Fatal(e)
	}
	physical := &physicalOrganizationFixture{organizationFixture: org, rootPath: root, sourceRoot: source, sourcePath: filepath.Join(source, review.SourceRelativePath), command: ports.PrepareMovieUpgradeOrganization{OperationID: native.operation, AuthorizationID: auth.ID, Confirmed: true}}
	if e := os.MkdirAll(filepath.Dir(physical.sourcePath), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(physical.sourcePath, bytes.Repeat([]byte("v"), int(review.SourceSize)), 0600); e != nil {
		t.Fatal(e)
	}
	physical.preparation, e = postgres.NewMovieUpgradeOrganizationPreparationRepository(native.preparation, rootfs.NewFilesystem())
	if e != nil {
		t.Fatal(e)
	}
	plan, e := physical.preparation.PrepareMovieUpgradeOrganization(ctx, native.actor, physical.command, "transmission-organization-prepare")
	if e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(ctx, native.dsn, organization76MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
	command, e := postgres.NewMovieUpgradeOrganizationJobRepository(native.preparation)
	if e != nil {
		t.Fatal(e)
	}
	return &organizationJobFixture{physicalOrganizationFixture: physical, repo: command, command: ports.QueueMovieUpgradeOrganization{OperationID: native.operation, PreparationID: plan.ID, Confirmed: true}, queue: tracking.queue}
}

func TestTransmissionMovieUpgradeOrganizationIngestionAndRetirementPG(t *testing.T) {
	f := transmissionMoviePhysicalFixture(t)
	ctx := t.Context()
	if v, e := postgres.Migrate(ctx, f.dsn, "../../../migrations"); e != nil || v < 80 {
		t.Fatal(v, e)
	}
	f.queued(t)
	lease := f.lease(t, 2*time.Minute)
	if r, e := f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); e != nil || r == nil {
		t.Fatal(r, e)
	}
	files := &nativeLinkFS{TransferFilesystem: rootfs.NewFilesystem()}
	executor, e := postgres.NewMovieUpgradeOrganizationExecutionRepository(f.trackingFixture.preparation, files)
	if e != nil {
		t.Fatal(e)
	}
	returned, e := executor.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
	if e != nil || returned == nil || returned.Status != "linked_pending_ingestion" {
		t.Fatal(returned, e)
	}
	ingestion, e := postgres.NewMovieUpgradeOrganizationIngestionRepository(f.trackingFixture.preparation, files, outbox.New(f.p, idgen.New(), nativeUpgradeClock{}))
	if e != nil {
		t.Fatal(e)
	}
	imported, e := ingestion.IngestMovieUpgradeOrganization(ctx, f.actor, lease.ID)
	if e != nil || imported == nil || imported.Status != "imported_pending_retirement" {
		t.Fatal(imported, e)
	}
	before, e := os.Stat(f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	linked, e := os.Stat(executionTarget(t, f))
	if e != nil || !os.SameFile(before, linked) {
		t.Fatal("new hardlink", e)
	}
	recycle := &retirementFS79{RecycleFilesystem: rootfs.NewFilesystem()}
	retirement, e := postgres.NewMovieUpgradeRetirementStore(f.trackingFixture.preparation, recycle)
	if e != nil {
		t.Fatal(e)
	}
	_, receipt := confirmRetirement79(t, f, retirement, f.sub)
	if e := retirement.ExecuteMovieUpgradeRetirement(ctx, f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if retirementStatus79(t, f) != "retired" || recycle.calls.Load() != 1 || files.calls.Load() != 1 || f.adds.Load() != 1 || f.exports.Load() != 0 {
		t.Fatal("wrong single-operation effects", recycle.calls.Load(), files.calls.Load(), f.adds.Load(), f.exports.Load())
	}
	again, e := retirement.ReadMovieUpgradeRetirementReceipt(ctx, f.actor, f.sub, f.operation, "retire-79")
	if e != nil || again == nil || *again != *receipt {
		t.Fatal("original retirement receipt", again, e)
	}
	if e := retirement.ExecuteMovieUpgradeRetirement(ctx, f.actor, f.operation); e != nil || recycle.calls.Load() != 1 {
		t.Fatal("retirement replay", e)
	}
	var valid bool
	e = f.p.QueryRow(ctx, `SELECT old.retired_at IS NOT NULL AND oldfact.revoked_at IS NOT NULL AND new.retired_at IS NULL AND newfact.revoked_at IS NULL AND r.retained_until=r.recorded_at+interval '7 days' AND lane.released_at=r.recorded_at AND reservation.state='consumed' AND requirement.state='imported' FROM movie_upgrade_authority a JOIN movie_upgrade_organization_ingestion i USING(operation_id) JOIN movie_upgrade_retirement_result r USING(operation_id) JOIN library_file old ON old.id=a.old_file_id JOIN unit_fact oldfact ON oldfact.id=a.old_fact_id JOIN library_file new ON new.id=i.file_id JOIN unit_fact newfact ON newfact.id=i.fact_id JOIN acquisition_reservation lane USING(operation_id) JOIN unit_reservation reservation USING(operation_id) JOIN download_requirement requirement USING(operation_id) WHERE a.operation_id=$1`, f.operation).Scan(&valid)
	if e != nil || !valid {
		t.Fatal("original identity/retention/protection", valid, e)
	}
}
