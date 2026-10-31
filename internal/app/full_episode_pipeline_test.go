package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

type episodePipelineProbe struct {
	originalRecovery      bool
	parenthesizedAbsolute bool
	incompleteInventory   bool
	manualRevision        bool
}

func TestAppRunReviewedEpisodeFullPipeline(t *testing.T) {
	for _, c := range []struct {
		name   string
		mapped bool
		probe  episodePipelineProbe
	}{{name: "standard"}, {name: "mapped", mapped: true}, {name: "mapped-parenthesized", mapped: true, probe: episodePipelineProbe{parenthesizedAbsolute: true}}} {
		t.Run(c.name, func(t *testing.T) { runReviewedEpisodeFullPipeline(t, c.mapped, c.probe) })
	}
}
func TestAppRunOriginalEpisodeRecoveryAfterRestart(t *testing.T) {
	if os.Getenv("PULSE_RECOVERY_RUNTIME") != "1" {
		t.Skip("explicit bounded original lease-expiry/restart probe")
	}
	runReviewedEpisodeFullPipeline(t, true, episodePipelineProbe{originalRecovery: true})
}
func runReviewedEpisodeFullPipeline(t *testing.T, mapped bool, probe ...episodePipelineProbe) {
	f := organizationSetup(t)
	duration := 48 * time.Second
	var options episodePipelineProbe
	if len(probe) > 0 {
		options = probe[0]
	}
	var revisedTarget string
	recoverOriginal := options.originalRecovery
	if recoverOriginal || options.manualRevision {
		duration = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := f.pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("DELETE FROM download_task;DELETE FROM unit_reservation;DELETE FROM download_requirement;DELETE FROM release_candidate;DELETE FROM indexer;")
	exec("UPDATE library_root SET media_type='tv' WHERE id=$1", f.root)
	exec("UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root)
	exec("UPDATE media SET media_type='tv',title='Fixture',year=2020 WHERE id=$1", f.media)
	exec("UPDATE media_unit SET unit_key='S01E01',season_number=1,episode_number=1,air_date='2020-01-01',metadata_version=metadata_version+1 WHERE id=$1", f.unit)
	if _, e := f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: f.media, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: f.unit, ExternalID: 1001, TmdbSeriesID: 100}); e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("c", 40)
	payload := bytes.Repeat([]byte("TV full bytes."), 4)
	rawTorrent, torrentHash := ordinaryTestTorrent("Fixture.S01E01.mkv", len(payload))
	if !mapped {
		hash = torrentHash
	}
	releaseTitle := "Fixture.2020.S01E01.1080p.BluRay.x264"
	if mapped {
		releaseTitle = "[Team] Fixture - 25 [1080p BluRay x264]"
		if options.parenthesizedAbsolute {
			releaseTitle = "[Team] Fixture - 25 (1080p) [BluRay x264]"
		}
	}
	source := filepath.Join(f.target, releaseTitle+".mkv")
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
	var inventoryComplete atomic.Bool
	inventoryComplete.Store(!options.incompleteInventory)
	var adds, queries, inventories, notifications atomic.Int32
	var mu sync.Mutex
	save, tags := "", ""
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			if r.ParseMultipartForm(1<<20) != nil {
				w.WriteHeader(400)
				return
			}
			if !mapped {
				assertOrdinaryTorrentUpload(t, r, rawTorrent)
			}
			mu.Lock()
			save, tags = r.FormValue("savepath"), r.FormValue("tags")
			mu.Unlock()
			adds.Add(1)
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			mu.Lock()
			s, tag := save, tags
			mu.Unlock()
			if s == "" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "name": releaseTitle, "save_path": s, "content_path": source, "progress": 1, "size": len(payload), "downloaded": len(payload), "state": "uploading", "tags": tag}})
		case "/api/v2/torrents/files":
			inventories.Add(1)
			if r.URL.Query().Get("hash") != hash {
				w.WriteHeader(400)
				return
			}
			progress := json.RawMessage(`1`)
			if !inventoryComplete.Load() {
				progress = json.RawMessage(`0.999999999999999999999`)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"index": 0, "name": filepath.Base(source), "size": len(payload), "progress": progress}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer qb.Close()
	password, e := crypto.Encrypt(outboxTestKey, []byte("fixture-password"))
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE download_client SET url=$2,password_encrypted=$3,remote_path_map='{}' WHERE id=$1", f.client, qb.URL, password)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			_, _ = w.Write(rawTorrent)
			return
		}
		queries.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		link := "magnet:?xt=urn:btih:" + hash
		if !mapped {
			link = "http://" + r.Host + "/torrent"
		}
		_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>%s</title><guid>tv-pipeline</guid><enclosure url="%s" length="%d"/><torznab:attr name="infohash" value="%s"/></item></channel></rss>`, releaseTitle, link, len(payload), hash)
	}))
	defer feed.Close()
	idx := uuid.New()
	key, e := crypto.Encrypt(outboxTestKey, []byte("fixture-key"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: idx, Name: "TV pipeline", Url: feed.URL, ApiKeyEncrypted: key, Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	user := uuid.New()
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "tv-owner", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}
	eStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	sub, _, e := subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: f.root, ScopeType: "all", IndexerIDs: []uuid.UUID{idx}, DownloaderID: &f.client})
	if e != nil {
		t.Fatal(e)
	}
	search := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), f.gen, crypto.NewSecretProtector(outboxTestKey), 0)
	search.SetHealthRepository(postgres.NewIndexerHealthStore(f.pool, f.gen))
	search.RegisterClient(idx, torznab.NewClient(feed.URL, "fixture-key", nil))
	query, e := search.ExecuteSearch(ctx, idx, "Fixture", 0)
	if e != nil || query.State != "succeeded" {
		t.Fatal(query, e)
	}
	var candidate uuid.UUID
	if e = f.pool.QueryRow(ctx, "SELECT candidate_id FROM indexer_query_candidate WHERE query_id=$1", query.QueryID).Scan(&candidate); e != nil {
		t.Fatal(e)
	}
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.pool, f.gen)), f.gen)
	if mapped {
		svc := subscription.NewEpisodeMappingService(mapping, nil)
		draft := ports.EpisodeMappingDraft{Mode: "manual", Entries: []ports.ManualEpisodeMappingEntry{{SourceNumber: 25, UnitID: f.unit}}}
		preview, e := svc.Preview(ctx, actor, sub.ID, draft)
		if e != nil || !preview.CanPublish {
			t.Fatal(preview, e)
		}
		_, e = svc.Publish(ctx, actor, sub.ID, ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: preview.InputStamp, ExpectedSourceHash: preview.SourceHash, Confirmed: true}, "tv-mapped-publish")
		if e != nil {
			t.Fatal(e)
		}
	}

	observed, e := mapping.ReadEpisodeCandidate(ctx, actor, sub.ID, candidate)
	if e != nil {
		t.Fatal(e)
	}
	review, e := subscription.NewEpisodeCandidateReviewService(mapping).Review(ctx, actor, ports.EpisodeCandidateReviewCommand{SubscriptionID: sub.ID, CandidateID: candidate, ExpectedInputStamp: observed.InputStamp, Confirmed: true, Reason: "确认当前标准单集作品身份"}, "tv-review")
	if e != nil {
		t.Fatal(e)
	}
	acquired, e := postgres.NewEpisodeAcquisitionStore(f.pool, f.gen).AcquireReviewedEpisode(ctx, actor, ports.AcquireReviewedEpisodeCommand{SubscriptionID: sub.ID, ReviewID: review.ID, ExpectedInputStamp: review.InputStamp, Confirmed: true}, "tv-acquire")
	if e != nil {
		t.Fatal(e)
	}
	var refreshes, offlineRefreshes atomic.Int32
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Updates []struct {
				Path       string `json:"Path"`
				UpdateType string `json:"UpdateType"`
			} `json:"Updates"`
		}
		if r.Method != "POST" || r.URL.Path != "/Library/Media/Updated" || r.Header.Get("Authorization") != `MediaBrowser Token="private-server-api-key"` || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Updates) != 1 || !strings.HasPrefix(body.Updates[0].Path, "/tv/Fixture (2020)/Season 01/") || body.Updates[0].UpdateType != "Created" {
			t.Error("invalid TV refresh authority/path")
			w.WriteHeader(400)
			return
		}
		refreshes.Add(1)
		w.WriteHeader(204)
	}))
	defer refreshServer.Close()
	serverID := createOutboxServer(t, f, refreshServer.URL)
	serverMapping, _ := json.Marshal(map[string]string{f.target: "/tv"})
	exec("UPDATE media_server SET path_mapping=$2 WHERE id=$1", serverID, serverMapping)
	offlineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { offlineRefreshes.Add(1); w.WriteHeader(503) }))
	defer offlineServer.Close()
	createOutboxServer(t, f, offlineServer.URL)

	_, _, notifier := testOutboxServices(f)
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { notifications.Add(1); w.WriteHeader(200) }))
	defer notify.Close()
	createOutboxChannel(t, f, notifier, notify.URL)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	if recoverOriginal || options.manualRevision {
		exec("CREATE FUNCTION fail_restart_import() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='transfer.imported' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
		exec("CREATE TRIGGER fail_restart_import BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_restart_import()")
	}
	runtime, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runtime) }()
	joined := false
	defer func() {
		stop()
		if joined {
			return
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("TV runtime owner not joined")
		}
	}()
	if options.incompleteInventory {
		waitOutboxCondition(t, ctx, func() bool {
			task, err := f.q.GetDownloadTaskByOpID(ctx, acquired.OperationID)
			return err == nil && task.State == "tracking" && inventories.Load() > 0
		})
		manager := verifyIncompleteInventory(t, ctx, f, acquired.OperationID, source, payload)
		inventoryComplete.Store(true)
		if _, err := manager.ObserveTask(ctx, acquired.OperationID); err != nil {
			t.Fatal("complete inventory recovery", err)
		}
	}
	if recoverOriginal || options.manualRevision {
		var originalPlan, taskID uuid.UUID
		var deadline time.Time
		waitOutboxCondition(t, ctx, func() bool {
			return f.pool.QueryRow(ctx, "SELECT t.id,p.id,p.lease_until FROM download_task t JOIN transfer_plan p ON p.id=t.transfer_plan_id WHERE t.operation_id=$1 AND EXISTS(SELECT 1 FROM transfer_step s WHERE s.plan_id=p.id AND s.state='running') AND EXISTS(SELECT 1 FROM job j WHERE j.kind='download_organization' AND j.payload->>'task_id'=t.id::text AND j.error IS NOT NULL)", acquired.OperationID).Scan(&taskID, &originalPlan, &deadline) == nil
		})
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			joined = true
		case <-ctx.Done():
			t.Fatal("first runtime not joined")
		}
		f.pool, e = postgres.NewPool(ctx, cfg.Database.URL, postgres.DefaultPoolConfig())
		if e != nil {
			t.Fatal("restart database reconnect", e)
		}
		defer f.pool.Close()
		var noFacts int
		if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM library_file_unit").Scan(&noFacts); e != nil || noFacts != 0 || adds.Load() != 1 {
			t.Fatal("restart import failure leaked or duplicated Add", noFacts, adds.Load(), e)
		}
		exec("DROP TRIGGER fail_restart_import ON audit_log")
		exec("DROP FUNCTION fail_restart_import()")
		// Worker has exited and joined. Model its terminal review outcome, never approve the plan or rewrite its lease/evidence.
		exec("UPDATE download_task SET organization_state='review_required' WHERE id=$1", taskID)
		exec("UPDATE job SET state='dead',lease_owner=NULL,lease_until=NULL WHERE kind='download_organization' AND payload->>'task_id'=$1", taskID.String())
		repo := transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
		if options.manualRevision {
			f.path = source
			f.op = acquired.OperationID
			f.taskID = taskID
			revisedTarget = reviseHistoricalEpisodeTarget(t, ctx, f, repo, actor, originalPlan, mapped && os.Getenv("PULSE_REVISION_EPISODE_BROWSER") == "1")
		} else {
			p, e := repo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
			if e != nil || p.Recoverable || p.Reason != "execution_lease_active" {
				t.Fatal("active original lease could be recovered", p, e)
			}
			timer := time.NewTimer(time.Until(deadline) + 50*time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				t.Fatal("original lease did not expire within probe")
			}
			timer.Stop()
			p, e = repo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
			if e != nil || !p.Recoverable {
				t.Fatal("original postcondition not observed", p, e)
			}
			r, e := repo.CommitOriginalTransferRecovery(ctx, actor, ports.TransferRecoveryCommand{PlanID: originalPlan, ExpectedInputStamp: p.InputStamp, Confirmed: true, Reason: "核对原硬链接事实后恢复原冻结计划"}, "restart-original")
			if e != nil || r.PlanID != originalPlan {
				t.Fatal("restart recovery command", r, e)
			}
		}
		a, e = New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
		if e != nil {
			t.Fatal(e)
		}
		runtime, stop = context.WithCancel(ctx)
		defer stop()
		done = make(chan error, 1)
		joined = false
		go func() { done <- a.Run(runtime) }()
	}

	waitOutboxCondition(t, ctx, func() bool {
		var imported int
		if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND state='imported'", acquired.OperationID).Scan(&imported); e != nil {
			return false
		}
		var succeeded int
		if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE consumer=$1 AND state='delivered'", "mediaserver.refresh:"+serverID.String()).Scan(&succeeded); e != nil {
			return false
		}
		return imported == 1 && notifications.Load() > 0 && refreshes.Load() > 0 && succeeded == 1 && offlineRefreshes.Load() > 0
	})
	var refreshSucceeded int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE consumer=$1 AND state='delivered'", "mediaserver.refresh:"+serverID.String()).Scan(&refreshSucceeded); e != nil || refreshSucceeded != 1 || offlineRefreshes.Load() == 0 {
		t.Fatal("TV server delivery not independent/confirmed", refreshSucceeded, offlineRefreshes.Load(), e)
	}
	var tasks, files, manifests, active int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM download_file_inventory),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)").Scan(&tasks, &files, &manifests, &active); e != nil || tasks != 1 || files != 1 || manifests != 1 || active != 0 || adds.Load() != 1 || queries.Load() != 1 || inventories.Load() < 1 {
		t.Fatal("TV pipeline not unique physical acquisition", tasks, files, manifests, active, adds.Load(), queries.Load(), inventories.Load(), e)
	}
	var rel string
	if e = f.pool.QueryRow(ctx, "SELECT f.rel_path FROM library_file f JOIN library_file_unit m ON m.file_id=f.id WHERE f.retired_at IS NULL AND m.unit_id=$1 AND f.root_id=$2", f.unit, f.root).Scan(&rel); e != nil {
		t.Fatal(e)
	}
	if revisedTarget != "" && rel != revisedTarget {
		t.Fatal("wrong revised target", rel)
	}
	actual, e := os.ReadFile(filepath.Join(f.target, rel))
	if e != nil || !bytes.Equal(actual, payload) {
		t.Fatal("TV full bytes not imported", e)
	}
	original, e := os.Stat(source)
	target, e2 := os.Stat(filepath.Join(f.target, rel))
	if e != nil || e2 != nil || !os.SameFile(original, target) {
		t.Fatal("TV not actual hardlink", e, e2)
	}
}
