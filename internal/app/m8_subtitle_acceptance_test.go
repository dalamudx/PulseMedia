package app

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Inventory/facts are SQL_PREPARED. Roots, first scan identities, queue claims,
// hashing, Shooter protocol, publication and completion are REAL App.Run work.
// The owned HTTP server proves the native protocol, not live Shooter availability
// or recognition by a running Jellyfin player. Naming is checked byte for byte.
func TestM8SubtitleRuntimeAcceptancePG(t *testing.T) {
	for _, tc := range []struct{ kind, scenario string }{
		{"movie", "publish"}, {"tv", "publish"}, {"movie", "preserve_user"},
		{"movie", "not_found"}, {"movie", "network"}, {"movie", "invalid_srt"},
		{"movie", "completion_rollback"}, {"movie", "lost_lease"},
		{"movie", "source_replaced"}, {"movie", "root_replaced"},
	} {
		t.Run("SQL_PREPARED/"+tc.kind+"/"+tc.scenario, func(t *testing.T) { m8SubtitleRuntime(t, tc.kind, tc.scenario) })
	}
	if !t.Failed() {
		t.Log("M8_SUBTITLE_RUNTIME_PASS scenarios=10 inventory=SQL_PREPARED root_scan=REAL network=OWNED_HTTP player=exact_basename_only live_shooter=false")
	}
}

type m8SubtitleRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r m8SubtitleRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	// Reject every destination except the canonical native Shooter origin. The
	// provider still sees the original URL when applying its same-origin gate.
	if req.URL.Scheme != "https" || req.URL.Host != "www.shooter.cn" || (req.URL.Path != "/api/subapi.php" && req.URL.Path != "/owned/subtitle.srt") || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		return nil, errors.New("unexpected subtitle fixture destination")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL = &u
	clone.Host = r.target.Host
	return r.transport.RoundTrip(clone)
}

type m8SubtitleRuntimeFixture struct {
	app    *App
	pool   *pgxpool.Pool
	cfg    *config.Config
	client *http.Client
	cancel context.CancelFunc
	done   chan error
}

func (f *m8SubtitleRuntimeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *m8SubtitleRuntimeFixture) start(t *testing.T) {
	t.Helper()
	runtimePool, err := pgxpool.New(t.Context(), f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(f.cfg, WithDBPool(runtimePool), WithMetadataHTTPClient(f.client), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		runtimePool.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	f.app = a
	ctx, cancel := context.WithCancel(t.Context())
	f.cancel = cancel
	f.done = make(chan error, 1)
	go func() { f.done <- a.Run(ctx) }()
}
func (f *m8SubtitleRuntimeFixture) stop(t *testing.T) {
	t.Helper()
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case err := <-f.done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("App.Run did not drain")
	}
	if err := f.app.Close(); err != nil {
		t.Error(err)
	}
	f.cancel = nil
}
func m8SubtitleWait(t *testing.T, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("subtitle runtime deadline")
		case <-ticker.C:
		}
	}
}

type m8SubtitlePlaybackCheck struct {
	video  []byte
	verify func(*testing.T, string, string, string)
}

func m8SubtitleRuntime(t *testing.T, kind, scenario string, playback ...m8SubtitlePlaybackCheck) {
	if !strings.HasPrefix(filepath.Clean(os.TempDir()), "/data/temp/") {
		t.Fatal("owned TMPDIR under /data/temp required")
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(t.Context(), dsn, "../../migrations")
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatalf("current migration: %d %v", version, err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var pgVersion int
	if err = pool.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::int`).Scan(&pgVersion); err != nil || pgVersion < 180006 || pgVersion >= 190000 {
		t.Fatalf("PG18.6+ required: %d %v", pgVersion, err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	f := &m8SubtitleRuntimeFixture{pool: pool, cfg: cfg}
	var jobID uuid.UUID
	var source, relative, rootPath, subtitlePath string
	var postCalls, getCalls atomic.Int32
	video := make([]byte, 65537)
	for i := range video {
		video[i] = byte((i*37 + i/251 + i/4096*19) % 256)
	}
	if len(playback) > 1 {
		t.Fatal("one native player check expected")
	}
	if len(playback) == 1 {
		if len(playback[0].video) < 16384 || playback[0].verify == nil {
			t.Fatal("playable video and player verifier required")
		}
		video = playback[0].video
	}
	// Independent expected hash from fixture bytes; do not call production hashing.
	var blocks []string
	for _, offset := range []int{4096, (len(video) / 3) * 2, len(video) / 3, len(video) - 8192} {
		blocks = append(blocks, fmt.Sprintf("%x", md5.Sum(video[offset:offset+4096])))
	}
	expectedHash := strings.Join(blocks, ";")
	rawSRT := []byte("\xef\xbb\xbf7\r\n00:00:01,000 --> 00:00:03,000\r\n你好，世界。\r\n\r\n")
	normalized := []byte("1\n00:00:01,000 --> 00:00:03,000\n你好，世界。\n\n")
	var leaseObserved atomic.Bool
	fixtureReady := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-fixtureReady
		switch r.URL.Path {
		case "/api/subapi.php":
			postCalls.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil || len(r.PostForm) != 4 || r.PostForm.Get("filehash") != expectedHash || r.PostForm.Get("format") != "json" || r.PostForm.Get("lang") != "chn" || r.PostForm.Get("pathinfo") != "" {
				t.Error("native Shooter form/hash/path privacy mismatch")
				w.WriteHeader(400)
				return
			}
			var valid bool
			err := pool.QueryRow(r.Context(), `SELECT state='running' AND attempts=1 AND max_attempts=1 AND lease_owner IS NOT NULL AND lease_token>0 AND lease_until>clock_timestamp() FROM job WHERE id=$1`, jobID).Scan(&valid)
			if err != nil || !valid {
				t.Errorf("native POST without valid claimed lease: %v", err)
				w.WriteHeader(400)
				return
			}
			leaseObserved.Store(true)
			if scenario == "network" {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = conn.Close()
				return
			}
			if scenario == "not_found" {
				_, _ = w.Write([]byte{0xff})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"Files":[{"Ext":"srt","Link":"https://www.shooter.cn/owned/subtitle.srt"}]}]`)
		case "/owned/subtitle.srt":
			getCalls.Add(1)
			if r.Method != http.MethodGet {
				t.Error("subtitle fetch is not GET")
				w.WriteHeader(405)
				return
			}
			switch scenario {
			case "lost_lease":
				_, e := pool.Exec(r.Context(), `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, jobID)
				if e != nil {
					t.Error(e)
				}
			case "source_replaced":
				// Replace the directory entry, retaining the original download inode.
				target := filepath.Join(rootPath, relative)
				if e := os.Remove(target); e != nil {
					t.Error(e)
				}
				if e := os.WriteFile(target, video, 0600); e != nil {
					t.Error(e)
				}
			case "root_replaced":
				if e := os.Rename(rootPath, rootPath+".displaced"); e != nil {
					t.Error(e)
				}
				if e := os.Mkdir(rootPath, 0700); e != nil {
					t.Error(e)
				}
				if e := os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("replacement marker"), 0600); e != nil {
					t.Error(e)
				}
			}
			if scenario == "invalid_srt" {
				_, _ = io.WriteString(w, "<html>验证码</html>")
				return
			}
			_, _ = w.Write(rawSRT)
		default:
			t.Error("unexpected owned fixture request")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	f.client = &http.Client{Transport: m8SubtitleRoute{target, transport}, Timeout: 3 * time.Second}
	f.start(t)
	t.Cleanup(func() { f.stop(t) })
	// Start with real empty roots and await native baseline finalization before
	// preparing inventory; no fabricated root marker, inode, scan token or run.
	var rootID uuid.UUID
	m8SubtitleWait(t, func() bool {
		return pool.QueryRow(t.Context(), `SELECT r.id,r.path FROM library_root r WHERE r.media_type=$1 AND r.health='online' AND r.baseline_completed AND EXISTS(SELECT 1 FROM scan_run sr JOIN scan_finalization sf ON sf.scan_run_id=sr.id WHERE sr.root_id=r.id AND sr.generation=r.generation AND sr.state='succeeded' AND sf.completed_at IS NOT NULL) AND NOT EXISTS(SELECT 1 FROM library_scan_job l JOIN job j ON j.id=l.job_id WHERE l.root_id=r.id AND j.state IN ('queued','running'))`, kind).Scan(&rootID, &rootPath) == nil
	})
	cap, err := rootfs.NewFilesystem().OpenRoot(t.Context(), rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	rootIdentity, err := cap.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var device, inode int64
	if err = pool.QueryRow(t.Context(), `SELECT sf.root_device,sf.root_inode FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id JOIN library_root r ON r.id=sf.root_id WHERE r.id=$1 AND sr.generation=r.generation AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1`, rootID).Scan(&device, &inode); err != nil || device != rootIdentity.Device || inode != rootIdentity.Inode {
		t.Fatalf("first scan root identity mismatch: %v", err)
	}
	if _, err = cap.Inspect(t.Context(), ".pulsemedia_root"); err != nil {
		t.Fatal(err)
	}
	relative = filepath.Join("Subtitle (2024)", "Subtitle.2024.1080p.part1.mkv")
	if kind == "tv" {
		relative = filepath.Join("Subtitle (2024)", "Season 01", "Subtitle.S01E03.1080p.part1.mkv")
	}
	if err = os.MkdirAll(filepath.Join(rootPath, filepath.Dir(relative)), 0700); err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(t.TempDir(), "download.mkv")
	if err = os.WriteFile(source, video, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(source, filepath.Join(rootPath, relative)); err != nil {
		t.Fatal(err)
	}
	identity, err := cap.Inspect(t.Context(), relative)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	subtitleName := strings.TrimSuffix(filepath.Base(relative), ".mkv") + ".zh-cn.default.srt"
	subtitlePath = filepath.Join(rootPath, filepath.Dir(relative), subtitleName)
	userBytes := []byte("user subtitle must remain byte-identical\n")
	var userInfo os.FileInfo
	if scenario == "preserve_user" {
		if err = os.WriteFile(subtitlePath, userBytes, 0600); err != nil {
			t.Fatal(err)
		}
		userInfo, err = os.Stat(subtitlePath)
		if err != nil {
			t.Fatal(err)
		}
	}
	mediaID, unitID, fileID := uuid.New(), uuid.New(), uuid.New()
	unitKey := "movie"
	var season, episode any
	if kind == "tv" {
		unitKey = "S01E03"
		season = int32(1)
		episode = int32(3)
	}
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Subtitle','Subtitle',2024,$2)`, mediaID, kind)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,'6211')`, uuid.New(), mediaID, "tmdb."+kind)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,$5)`, unitID, mediaID, unitKey, season, episode)
	if scenario == "completion_rollback" {
		f.exec(t, `CREATE FUNCTION m8_reject_subtitle_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='media.subtitle.completed' THEN RAISE EXCEPTION 'owned completion rollback'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER m8_reject_subtitle_completion AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION m8_reject_subtitle_completion()`)
	}
	// Production entry point within a real importing transaction. Prove both
	// facts and event/job are invisible outside that transaction before commit.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	execTx := func(sql string, args ...any) {
		t.Helper()
		if _, e := tx.Exec(t.Context(), sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	execTx(`INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,$4,$5,$6)`, fileID, rootID, relative, identity.Size, identity.Mtime, fmt.Sprintf("stat.v1:%d:%d:%d", identity.Inode, identity.Size, identity.Mtime))
	execTx(`INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, fileID, unitID)
	execTx(`INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), unitID, rootID, fileID)
	event, err := outbox.New(pool, f.app.IDGen(), f.app.Clock()).PublishImported(t.Context(), db.New(tx), unitID.String(), map[string]any{"file_id": fileID, "unit_id": unitID})
	if err != nil {
		t.Fatal(err)
	}
	var enqueued bool
	if err = tx.QueryRow(t.Context(), `SELECT id,state='queued' AND max_attempts=1 AND attempts=0 AND payload=jsonb_build_object('version',1,'source_event_id',$1::uuid,'file_id',$2::uuid,'unit_id',$3::uuid) AND dedupe_key='media-subtitle:'||$2::uuid::text||':'||(SELECT mapping_revision::text FROM library_file WHERE id=$2::uuid) FROM job WHERE kind='media_subtitle' AND payload->>'source_event_id'=$1::uuid::text`, event.ID, fileID, unitID).Scan(&jobID, &enqueued); err != nil || !enqueued {
		t.Fatalf("transaction-bound subtitle enqueue: %v", err)
	}
	var invisible bool
	if err = pool.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM job WHERE id=$1) AND NOT EXISTS(SELECT 1 FROM outbox_event WHERE id=$2) AND NOT EXISTS(SELECT 1 FROM library_file WHERE id=$3)`, jobID, event.ID, fileID).Scan(&invisible); err != nil || !invisible {
		t.Fatalf("enqueue escaped importing TX: %v", err)
	}
	close(fixtureReady)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	protected := m8SubtitleIngestionGraph(t, pool)
	var state string
	var result []byte
	m8SubtitleWait(t, func() bool {
		err := pool.QueryRow(t.Context(), `SELECT state,result FROM job WHERE id=$1`, jobID).Scan(&state, &result)
		return err == nil && (state == "succeeded" || state == "dead" || state == "failed")
	})
	successful := scenario == "publish" || scenario == "preserve_user" || scenario == "not_found"
	if state != map[bool]string{true: "succeeded", false: "dead"}[successful] {
		t.Fatalf("terminal job=%s result=%s", state, result)
	}
	var terminal bool
	if err = pool.QueryRow(t.Context(), `SELECT attempts=1 AND max_attempts=1 AND lease_token>0 AND lease_owner IS NULL AND lease_until IS NULL AND (NOT $2 OR finished_at IS NOT NULL) AND ($2 OR error LIKE '%review_required%') FROM job WHERE id=$1`, jobID, successful).Scan(&terminal); err != nil || !terminal {
		t.Fatalf("terminal lease/review receipt: %v", err)
	}
	var audits int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action='media.subtitle.completed' AND target_type='job' AND target_id=$1`, jobID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if successful {
		var completion ports.MediaSubtitleCompletion
		if err = json.Unmarshal(result, &completion); err != nil {
			t.Fatal(err)
		}
		var exact bool
		if err = pool.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(actor_type='system' AND actor_id='media_subtitle' AND payload=$2::jsonb) FROM audit_log WHERE action='media.subtitle.completed' AND target_type='job' AND target_id=$1`, jobID.String(), result).Scan(&exact); err != nil || !exact {
			t.Fatalf("exact atomic audit/result: %v", err)
		}
		want := ports.MediaSubtitleCompletion{Outcome: "not_found", VideoHash: ports.SubtitleVideoHash{Hash: expectedHash, SizeBytes: int64(len(video))}}
		if scenario != "not_found" {
			gotID, e := cap.Inspect(t.Context(), filepath.Join(filepath.Dir(relative), subtitleName))
			if e != nil {
				t.Fatal(e)
			}
			disposition := ports.SidecarCreated
			published := true
			hash := want.VideoHash
			if scenario == "preserve_user" {
				disposition = ports.SidecarPreservedRegular
				published = false
				hash = ports.SubtitleVideoHash{}
			}
			want = ports.MediaSubtitleCompletion{Outcome: "observed", VideoHash: hash, Name: filepath.Join(filepath.Dir(relative), subtitleName), Publication: ports.SidecarResult{Disposition: disposition, Published: published, Identity: gotID}}
		}
		if completion != want {
			t.Fatalf("exact native completion: got=%+v want=%+v", completion, want)
		}
	} else if audits != 0 || len(result) != 0 {
		t.Fatalf("failure falsely completed: audits=%d result=%s", audits, result)
	}
	published := scenario == "publish" || scenario == "completion_rollback"
	if published {
		if !bytes.Equal(m7ReadRegular(t, subtitlePath, 0644), normalized) {
			t.Fatal("normalized Chinese SRT bytes mismatch")
		}
	}
	if scenario == "preserve_user" {
		now, e := os.Stat(subtitlePath)
		if e != nil || !os.SameFile(userInfo, now) || !bytes.Equal(m7ReadRegular(t, subtitlePath, 0600), userBytes) {
			t.Fatal("user subtitle inode/mode/bytes changed")
		}
	} else if !published {
		if _, e := os.Lstat(subtitlePath); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("failure/no-result published subtitle: %v", e)
		}
	}
	wantPosts, wantGets := int32(1), int32(1)
	if scenario == "preserve_user" {
		wantPosts, wantGets = 0, 0
	}
	if scenario == "not_found" || scenario == "network" {
		wantGets = 0
	}
	if postCalls.Load() != wantPosts || getCalls.Load() != wantGets || leaseObserved.Load() != (wantPosts > 0) {
		t.Fatalf("native request counts POST=%d GET=%d validLease=%t", postCalls.Load(), getCalls.Load(), leaseObserved.Load())
	}
	if protected != m8SubtitleIngestionGraph(t, pool) {
		t.Fatal("subtitle changed SQL_PREPARED ingestion graph")
	}
	if scenario == "completion_rollback" {
		before, err := os.Stat(subtitlePath)
		if err != nil {
			t.Fatal(err)
		}
		f.stop(t)
		f.exec(t, `DROP TRIGGER m8_reject_subtitle_completion ON audit_log; DROP FUNCTION m8_reject_subtitle_completion()`)
		// PublishImported must also preserve historical R3 protection: another
		// imported event for this mapping cannot enqueue an automatic retry.
		republish, e := pool.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = republish.Rollback(context.WithoutCancel(t.Context())) }()
		if _, e = outbox.New(pool, f.app.IDGen(), f.app.Clock()).PublishImported(t.Context(), db.New(republish), unitID.String(), map[string]any{"file_id": fileID, "unit_id": unitID}); e != nil {
			t.Fatal(e)
		}
		if e = republish.Commit(t.Context()); e != nil {
			t.Fatal(e)
		}
		var oneOriginal bool
		if e = pool.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(id=$1) FROM job WHERE kind='media_subtitle'`, jobID).Scan(&oneOriginal); e != nil || !oneOriginal {
			t.Fatalf("PublishImported replayed historical R3 job: %v", e)
		}
		// Restart the actual App, then run a committed operator observation job
		// to prove the worker advanced beyond recovery without automatic replay.
		f.start(t)
		barrier := uuid.New()
		f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts) SELECT $1,kind,'queued',payload,1 FROM job WHERE id=$2`, barrier, jobID)
		m8SubtitleWait(t, func() bool {
			var done bool
			return pool.QueryRow(t.Context(), `SELECT state='succeeded' FROM job WHERE id=$1`, barrier).Scan(&done) == nil && done
		})
		var safe bool
		if err = pool.QueryRow(t.Context(), `SELECT state='dead' AND attempts=1 AND result IS NULL AND NOT EXISTS(SELECT 1 FROM audit_log WHERE action='media.subtitle.completed' AND target_id=$1::uuid::text) FROM job WHERE id=$1`, jobID).Scan(&safe); err != nil || !safe {
			t.Fatalf("R3 original job replayed: %v", err)
		}
		var observed ports.MediaSubtitleCompletion
		var receipt []byte
		if err = pool.QueryRow(t.Context(), `SELECT result FROM job WHERE id=$1`, barrier).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(receipt, &observed); err != nil || observed.Publication.Disposition != ports.SidecarPreservedRegular || observed.Publication.Published {
			t.Fatalf("operator observation claimed prior publication: %+v %v", observed, err)
		}
		now, e := os.Stat(subtitlePath)
		if e != nil || !os.SameFile(before, now) || !bytes.Equal(m7ReadRegular(t, subtitlePath, 0644), normalized) || postCalls.Load() != 1 || getCalls.Load() != 1 {
			t.Fatal("restart replayed network or replaced uncertain published file")
		}
	}
	// Restore deliberately replaced entries only after the worker has finished.
	if scenario == "root_replaced" {
		if _, e := os.Lstat(filepath.Join(rootPath+".displaced", filepath.Dir(relative), subtitleName)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("replacement fence published into displaced root: %v", e)
		}
		if err = os.Remove(filepath.Join(rootPath, ".pulsemedia_root")); err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(rootPath); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(rootPath+".displaced", rootPath); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "source_replaced" {
		if err = os.Remove(filepath.Join(rootPath, relative)); err != nil {
			t.Fatal(err)
		}
		if err = os.Link(source, filepath.Join(rootPath, relative)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{source, filepath.Join(rootPath, relative)} {
		raw := m7ReadRegular(t, path, 0600)
		info, e := os.Stat(path)
		if e != nil || !os.SameFile(originalInfo, info) || sha256.Sum256(raw) != sha256.Sum256(video) {
			t.Fatal("source bytes/hash/inode changed")
		}
	}
	actual, err := cap.Inspect(t.Context(), relative)
	if err != nil || actual != identity {
		t.Fatalf("source identity changed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(subtitlePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pulsemedia-sidecar-") {
			t.Fatal("subtitle staging file leaked")
		}
	}
	for _, player := range playback {
		player.verify(t, rootPath, relative, subtitlePath)
	}
	t.Logf("M8_SUBTITLE_CASE_PASS kind=%s scenario=%s state=%s inventory=SQL_PREPARED video=REAL", kind, scenario, state)
}

// Exclude background scheduling clocks and attachment jobs; preserve exact input
// and ingestion facts, including any acquisition/requirement/operation protection.
func m8SubtitleIngestionGraph(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var graph strings.Builder
	for _, table := range []string{"media", "media_identity", "media_unit", "library_file", "library_file_unit", "unit_fact", "outbox_event", "outbox_delivery", "unit_reservation", "acquisition_reservation", "download_requirement", "download_task", "idempotency_record"} {
		var value string
		if err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{table}.Sanitize()+` t) s`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		graph.WriteString(table)
		graph.WriteString(value)
	}
	return graph.String()
}
