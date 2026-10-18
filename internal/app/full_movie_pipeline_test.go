package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
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
)

func TestAppRunFullMoviePipelineUsesOnePhysicalAcquisition(t *testing.T) {
	for _, selected := range []bool{false, true} {
		name := "automatic"
		if selected {
			name = "explicit-selection"
		}
		t.Run(name, func(t *testing.T) { runFullMoviePipeline(t, selected) })
	}
}

type pipelineSelectedMetadata struct{ ports.MetadataProvider }

func (*pipelineSelectedMetadata) GetMovieDetails(context.Context, int64, string) (*ports.MovieMetadata, error) {
	return &ports.MovieMetadata{TMDBID: 1, MediaType: "movie", Title: "Movie", Year: 2020}, nil
}
func runFullMoviePipeline(t *testing.T, selected bool) {
	runFullMoviePipelineWithMapping(t, selected, "")
}
func runFullMoviePipelineWithMapping(t *testing.T, selected bool, mappingMode string) {
	runFullMoviePipelineSource(t, selected, mappingMode, "")
}
func runFullMoviePipelineSource(t *testing.T, selected bool, mappingMode, directoryMode string, beforeStart ...func(*organizationFixture, *ports.Actor, uuid.UUID, *App) func()) {
	var afterProbes []func()
	var selectedOp uuid.UUID
	f := organizationSetup(t)
	limit := 48 * time.Second
	if directoryMode == "browser" || directoryMode == "unified" {
		limit = 110 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if _, e := f.pool.Exec(ctx, "DELETE FROM download_task;DELETE FROM unit_reservation;DELETE FROM download_requirement;DELETE FROM release_candidate;DELETE FROM indexer;"); e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("a", 40)
	payload := []byte("complete movie bytes for real hardlink validation")
	useHTTPSource := !selected && directoryMode == ""
	rawTorrent, torrentHash := ordinaryTestTorrent("Movie.2020.mkv", len(payload))
	if useHTTPSource {
		hash = torrentHash
	}
	source := filepath.Join(f.target, "source.mkv")
	if directoryMode != "" {
		source = filepath.Join(f.target, "release", "source.mkv")
		if e := os.MkdirAll(filepath.Dir(source), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
	protectedPath := source
	contentPath := source
	totalBytes := len(payload)
	inventoryFiles := []map[string]any{}
	if directoryMode != "" {
		contentPath = filepath.Dir(source)
		sidecar := []byte("subtitle bytes")
		if e := os.WriteFile(filepath.Join(contentPath, "subtitle.srt"), sidecar, 0600); e != nil {
			t.Fatal(e)
		}
		totalBytes += len(sidecar)
		inventoryFiles = []map[string]any{{"index": 0, "name": "release/source.mkv", "size": len(payload), "progress": 1}, {"index": 1, "name": "release/subtitle.srt", "size": len(sidecar), "progress": 1}}
	}
	mapping := map[string]string{}
	switch mappingMode {
	case "root":
		mapping["/"] = filepath.Join(f.target, "remote-root")
		mappedSource := filepath.Join(mapping["/"], strings.TrimPrefix(source, "/"))
		if err := os.MkdirAll(filepath.Dir(mappedSource), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		source = mappedSource
		if err := os.WriteFile(source, payload, 0600); err != nil {
			t.Fatal(err)
		}
		if directoryMode != "" {
			originalSidecar := filepath.Join(contentPath, "subtitle.srt")
			bytes, e := os.ReadFile(originalSidecar)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(filepath.Dir(source), "subtitle.srt"), bytes, 0600); e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(originalSidecar); e != nil {
				t.Fatal(e)
			}
		}
	case "conflict":
		mapping[f.target] = filepath.Join(f.target, "other")
		mapping[f.target+"/"] = f.target
	case "invalid":
		mapping["relative"] = f.target
	case "symlink":
		outside := t.TempDir()
		protectedPath = filepath.Join(outside, "source.mkv")
		if err := os.WriteFile(protectedPath, payload, 0600); err != nil {
			t.Fatal(err)
		}
		allowed := t.TempDir()
		if err := os.Symlink(protectedPath, filepath.Join(allowed, "source.mkv")); err != nil {
			t.Fatal(err)
		}
		mapping[f.target] = allowed
	}
	mappingRaw, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	var adds, queries atomic.Int32
	var mu sync.Mutex
	save, tags := "", ""
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				w.WriteHeader(400)
				return
			}
			if useHTTPSource {
				assertOrdinaryTorrentUpload(t, r, rawTorrent)
			}
			mu.Lock()
			save, tags = r.FormValue("savepath"), r.FormValue("tags")
			mu.Unlock()
			adds.Add(1)
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			mu.Lock()
			currentSave, currentTags := save, tags
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if currentSave == "" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "name": "Movie.2020.1080p.BluRay", "save_path": currentSave, "content_path": contentPath, "progress": 1, "size": totalBytes, "downloaded": totalBytes, "state": "uploading", "tags": currentTags}})
		case "/api/v2/torrents/files":
			_ = json.NewEncoder(w).Encode(inventoryFiles)
		default:
			w.WriteHeader(404)
		}
	}))
	defer qb.Close()
	password, e := crypto.Encrypt(outboxTestKey, []byte("fixture-private-password"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_client SET url=$2,password_encrypted=$3,remote_path_map='{}' WHERE id=$1", f.client, qb.URL, password); e != nil {
		t.Fatal(e)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE download_client SET remote_path_map=$2 WHERE id=$1", f.client, mappingRaw); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			_, _ = w.Write(rawTorrent)
			return
		}
		if r.URL.Query().Get("apikey") != "fixture-search-key" {
			w.WriteHeader(403)
			return
		}
		queries.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		link := "magnet:?xt=urn:btih:" + hash
		if useHTTPSource {
			link = "http://" + r.Host + "/torrent"
		}
		_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Movie.2020.1080p.BluRay</title><guid>pipeline</guid><enclosure url="%s" length="%d"/><torznab:attr name="infohash" value="%s"/></item></channel></rss>`, link, totalBytes, hash)
	}))
	defer server.Close()
	indexer := uuid.New()
	key, e := crypto.Encrypt(outboxTestKey, []byte("fixture-search-key"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexer, Name: "Full pipeline", Url: server.URL, ApiKeyEncrypted: key, Categories: []byte(`[]`)}); e != nil {
		t.Fatal(e)
	}
	user := uuid.New()
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "pipeline-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	eStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	sub, _, e := subscription.NewService(eStore, eStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), eStore).CreateSubscription(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}, subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: f.root, ScopeType: "all", IndexerIDs: []uuid.UUID{indexer}, DownloaderID: &f.client})
	if e != nil {
		t.Fatal(e)
	}
	_, _, notifier := testOutboxServices(f)
	var notifications atomic.Int32
	notifyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { notifications.Add(1); w.WriteHeader(200) }))
	defer notifyServer.Close()
	createOutboxChannel(t, f, notifier, notifyServer.URL)
	if selected {
		if _, e = f.pool.Exec(ctx, "UPDATE subscription SET state='paused' WHERE id=$1", sub.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: f.media, Namespace: "tmdb.movie", ExternalID: "1"}); e != nil {
			t.Fatal(e)
		}
		if directoryMode != "unified" {
			search := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), f.gen, crypto.NewSecretProtector(outboxTestKey), 0)
			search.SetHealthRepository(postgres.NewIndexerHealthStore(f.pool, f.gen))
			search.RegisterClient(indexer, torznab.NewClient(server.URL, "fixture-search-key", nil))
			query, e := search.ExecuteSearch(ctx, indexer, "Movie", 0)
			if e != nil || query.State != "succeeded" {
				t.Fatal("selected HTTP source failed", query, e)
			}
			var candidateID uuid.UUID
			if e = f.pool.QueryRow(ctx, "SELECT candidate_id FROM indexer_query_candidate WHERE query_id=$1", query.QueryID).Scan(&candidateID); e != nil {
				t.Fatal("selected persisted query evidence", e)
			}
			svc := acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(f.pool, f.gen, clock.New(), acquisition.SelectMovieRound), &pipelineSelectedMetadata{}, clock.New())
			actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}
			choice := ports.MovieSelection{TMDBID: 1, RootID: f.root, CandidateID: candidateID, DownloaderID: f.client, PolicyID: "default"}
			preview, e := svc.Preview(ctx, actor, choice)
			if e != nil || preview.Decision != "accept" {
				t.Fatal("selected preview", preview, e)
			}
			command := ports.MovieSelectionCommand{Selection: choice, ExpectedInputStamp: preview.InputStamp, Confirmed: true}
			first, e := svc.Commit(ctx, actor, command, "selected-pipeline")
			if e != nil {
				t.Fatal(e)
			}
			again, e := svc.Commit(ctx, actor, command, "selected-pipeline")
			if e != nil || *again != *first {
				t.Fatal("selected pipeline replay", again, first, e)
			}
			selectedOp = first.OperationID
			if _, e = f.pool.Exec(ctx, "UPDATE subscription SET state='active' WHERE id=$1", sub.ID); e != nil {
				t.Fatal(e)
			}
		}
	}

	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = f.pool.Config().ConnConfig.ConnString()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	a, e := New(cfg, WithDBPool(f.pool), WithLogger(logging.NewLogger(io.Discard, "error")))
	if e != nil {
		t.Fatal(e)
	}
	for _, probe := range beforeStart {
		afterProbes = append(afterProbes, probe(f, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}, selectedOp, a))
	}
	runtime, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runtime) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(50 * time.Second):
			t.Error("pipeline owner not joined")
		}
	}()
	defer func() {
		if t.Failed() {
			var raw []byte
			_ = f.pool.QueryRow(context.Background(), "SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(jsonb_build_object('kind',kind,'state',state,'error',error))FROM job),'tasks',(SELECT jsonb_agg(jsonb_build_object('state',state,'organization',organization_state,'reason',organization_reason)) FROM download_task))").Scan(&raw)
			t.Logf("pipeline adds=%d queries=%d diagnostics=%s", adds.Load(), queries.Load(), raw)
			if tasks, e := f.q.ListRequirementsForOperation(context.Background(), f.op); e == nil {
				_ = tasks
			}
			var id uuid.UUID
			if e := f.pool.QueryRow(context.Background(), "SELECT id FROM download_task LIMIT 1").Scan(&id); e == nil {
				inputs, e := f.q.GetDownloadOrganizationInputs(context.Background(), id)
				for _, in := range inputs {
					t.Logf("orginput healthy=%s baseline=%t matched=%t frozen=%t source=%t versions=%t rawsize=%d valid=%t", in.Health, in.BaselineCompleted, in.MatchedUnitID.Valid, in.FrozenCoverage, in.AcquisitionSourceStamp != "", in.DemandVersionsCurrent, len(in.LinkEncrypted), in.FrozenCoverage && in.DemandVersionsCurrent)
				}
				t.Logf("orginputquery error=%v", e)
			}
		}
	}()
	if mappingMode != "" && mappingMode != "root" {
		waitOutboxCondition(t, ctx, func() bool {
			var n int
			err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_task WHERE organization_state='review_required'`).Scan(&n)
			return err == nil && n == 1
		})
		var facts, files, plans, active, imported int
		err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_fact WHERE revoked_at IS NULL),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM transfer_plan),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM download_requirement WHERE state='imported')`).Scan(&facts, &files, &plans, &active, &imported)
		if err != nil || facts != 0 || files != 0 || plans != 0 || active != 1 || imported != 0 || adds.Load() != 1 {
			t.Fatal("unsafe mapping advanced organization", facts, files, plans, active, imported, adds.Load(), err)
		}
		original, err := os.ReadFile(source)
		if err != nil || string(original) != string(payload) {
			t.Fatal("source changed", err)
		}
		protected, err := os.ReadFile(protectedPath)
		if err != nil || string(protected) != string(payload) {
			t.Fatal("protected external bytes changed", err)
		}
		return
	}
	if directoryMode == "unified" {
		for _, probe := range afterProbes {
			probe()
		}
		afterProbes = nil
	}
	waitOutboxCondition(t, ctx, func() bool {
		var review int
		if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE organization_state='review_required'").Scan(&review); e == nil && review > 0 {
			t.Fatal("pipeline organization explicitly requires review")
		}
		var imported int
		e := f.pool.QueryRow(ctx, "SELECT count(*) FROM download_requirement WHERE root_id=$1 AND state='imported'", f.root).Scan(&imported)
		return e == nil && imported == 1 && notifications.Load() > 0
	})
	var tasks, physical, files, active int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM library_file_unit),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)").Scan(&tasks, &physical, &files, &active); e != nil || tasks != 1 || physical != 1 || files != 1 || active != 0 || adds.Load() != 1 || queries.Load() < 1 {
		t.Fatal("pipeline not one physical operation", tasks, physical, files, active, adds.Load(), queries.Load(), e)
	}
	report, e := subscription.NewStatusService(postgres.NewSubscriptionStatusStore(f.pool)).Read(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}, sub.ID)
	if e != nil || len(report.Items) != 1 || !report.Items[0].IsSatisfied {
		t.Fatal("pipeline imported not current owned", report, e)
	}
	var relative string
	if e = f.pool.QueryRow(ctx, "SELECT f.rel_path FROM library_file f JOIN library_file_unit u ON u.file_id=f.id WHERE f.retired_at IS NULL").Scan(&relative); e != nil {
		t.Fatal(e)
	}
	actual, e := os.ReadFile(filepath.Join(f.target, relative))
	if e != nil || string(actual) != string(payload) {
		t.Fatal("artifact not complete real bytes", e)
	}
	original, e := os.Stat(source)
	target, e2 := os.Stat(filepath.Join(f.target, relative))
	if e != nil || e2 != nil || !os.SameFile(original, target) {
		t.Fatal("expected real hardlink outcome", e, e2)
	}
	if directoryMode != "" {
		var content string
		var total, video int64
		var count int
		e = f.pool.QueryRow(ctx, `SELECT t.content_path,t.total_bytes,s.source_size,jsonb_array_length(i.files) FROM download_task t JOIN download_file_inventory i ON i.task_id=t.id JOIN transfer_step s ON s.plan_id=t.transfer_plan_id`).Scan(&content, &total, &video, &count)
		if e != nil || content != filepath.Join(contentPath, "source.mkv") || total != int64(totalBytes) || video != int64(len(payload)) || count != 2 {
			t.Fatal("directory evidence not frozen", content, total, video, count, e)
		}
		bytes, e := os.ReadFile(filepath.Join(filepath.Dir(source), "subtitle.srt"))
		if e != nil || string(bytes) != "subtitle bytes" {
			t.Fatal("sidecar changed", e)
		}
		if directoryMode == "browser" {
			var originalOp uuid.UUID
			if e = f.pool.QueryRow(ctx, "SELECT operation_id FROM download_task LIMIT 1").Scan(&originalOp); e != nil {
				t.Fatal(e)
			}
			runMovieDirectoryActivityBrowser(t, ctx, a, f, user, originalOp)
		}
	}
	for _, probe := range afterProbes {
		if probe != nil {
			probe()
		}
	}
}
