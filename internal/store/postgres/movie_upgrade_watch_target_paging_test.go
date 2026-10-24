package postgres_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type moviePagingRemote struct {
	raw              []byte
	hash, path, tags string
	adds             int
}
type moviePagingNative struct {
	mu    sync.Mutex
	tasks map[string]*moviePagingRemote
}

func (s *moviePagingNative) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v2/auth/login" {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "paging-test", Path: "/"})
		_, _ = io.WriteString(w, "Ok.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/api/v2/torrents/add" {
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "bad multipart", 400)
			return
		}
		var raw []byte
		fields := make(map[string]string)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad part", 400)
				return
			}
			body, e := io.ReadAll(io.LimitReader(part, 1<<20))
			_ = part.Close()
			if e != nil {
				http.Error(w, "bad body", 400)
				return
			}
			if part.FormName() == "torrents" {
				raw = body
			} else {
				fields[part.FormName()] = string(body)
			}
		}
		metainfo, err := (torrent.Inspector{}).InspectTorrent(raw)
		if err != nil {
			http.Error(w, "bad metainfo", 400)
			return
		}
		task := s.tasks[metainfo.InfoHash]
		if task == nil {
			task = &moviePagingRemote{raw: raw, hash: metainfo.InfoHash, path: fields["savepath"], tags: fields["tags"]}
			s.tasks[task.hash] = task
		}
		task.adds++
		_, _ = io.WriteString(w, "Ok.")
		return
	}
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		hash = r.URL.Query().Get("hashes")
	}
	if r.URL.Path == "/api/v2/torrents/info" {
		var out []map[string]any
		for key, task := range s.tasks {
			if tag := r.URL.Query().Get("tag"); tag != "" {
				matched := false
				for _, actual := range strings.Split(task.tags, ",") {
					if strings.TrimSpace(actual) == tag {
						matched = true
					}
				}
				if !matched {
					continue
				}
			}
			if hash != "" && hash != key {
				continue
			}
			out = append(out, map[string]any{"hash": task.hash, "tags": task.tags, "save_path": task.path, "size": 4, "downloaded": 2, "progress": 0.5, "state": "downloading"})
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	task := s.tasks[hash]
	if task == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/v2/torrents/export":
		_, _ = w.Write(task.raw)
	case "/api/v2/torrents/files":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"index": 0, "name": "Fight.Club.1999.2160p.Bluray.x265.mkv", "size": 4, "progress": 1}})
	default:
		http.NotFound(w, r)
	}
}

// Only catalog facts are constructed here; original production transactions
// issue each independent reservation, preparation and execution authorization.
func moviePagingPrepare(t *testing.T, base *movieUpgradeWatchFixture, ordinal int) *preparedUpgradeFixture {
	t.Helper()
	ctx := t.Context()
	q := db.New(base.p)
	f := &frozenUpgradeFixture{p: base.p, dsn: base.dsn, store: base.store, actor: base.actor, root: base.root, source: base.frozenUpgradeFixture.source, client: base.client, sub: uuid.New(), unit: uuid.New(), file: uuid.New(), fact: uuid.New(), candidate: uuid.New()}
	medium := uuid.New()
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: medium, Title: "Fight Club", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE media SET year=1999 WHERE id=$1`, medium)
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: f.unit, MediaID: medium, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("%d", 550000+ordinal)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie',$3)`, uuid.New(), medium, identity)
	sources, _ := json.Marshal([]uuid.UUID{f.source})
	if _, err := q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: f.sub, UserID: uuid.MustParse(f.actor.ID), MediaID: medium, RootID: f.root, QualityPolicyID: "frozen-upgrade", QualityPolicyRevision: 1, IndexerIds: sources, ScopeType: "all"}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE subscription SET downloader_id=$2 WHERE id=$1`, f.sub, f.client)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,100,1)`, f.file, f.root, fmt.Sprintf("old-paging-%03d.mkv", ordinal))
	if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: f.unit}); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(media.Recognize("Fight.Club.1999.1080p.Bluray.x264").Quality)
	if _, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: f.fact, UnitID: f.unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertWantedItem(ctx, db.UpsertWantedItemParams{ID: uuid.New(), SubscriptionID: f.sub, UnitID: f.unit, IntentVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetUpgradeMode(ctx, f.actor, ports.SetUpgradeModeCommand{SubscriptionID: f.sub, ExpectedIntent: 1, Mode: ports.UpgradeStopAtCutoff, Confirmed: true}, fmt.Sprintf("paging-enable-%03d", ordinal)); err != nil {
		t.Fatal("enable", err)
	}
	f.project(t)
	m, err := q.GetMediaByID(ctx, medium)
	if err != nil {
		t.Fatal(err)
	}
	units, err := q.ListUpgradeUnits(ctx, medium)
	if err != nil || len(units) != 1 {
		t.Fatal("one target unit", err)
	}
	target := media.MovieUpgradeTarget{MediaID: medium, UnitID: f.unit, MediaVersion: m.MetadataVersion, UnitVersion: units[0].MetadataVersion, MediaType: media.TypeMovie, UnitKey: "movie", OriginalTitle: m.Title, Year: 1999, Identities: []media.Identity{{Namespace: "tmdb.movie", ExternalID: identity}}, ParserVersion: media.EpisodeParserVersion}
	name := "Fight.Club.1999.2160p.Bluray.x265.mkv"
	raw := []byte(fmt.Sprintf("d4:infod6:lengthi4e4:name%d:%s12:piece lengthi4e6:pieces20:%020dee", len(name), name, ordinal))
	proof, err := torrent.InspectMovieUpgradeMetainfo(raw, target)
	if err != nil {
		t.Fatal("proof", err)
	}
	profile, _ := json.Marshal(media.Recognize(strings.TrimSuffix(name, ".mkv")).Quality)
	if _, err := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: f.candidate, IndexerID: f.source, Guid: fmt.Sprintf("paging-%03d", ordinal), Title: strings.TrimSuffix(name, ".mkv"), LinkEncrypted: []byte("never-decrypt"), SizeBytes: 4, QualityProfile: profile, SourceConfigVersion: 1}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE release_candidate SET info_hash=$2 WHERE id=$1`, f.candidate, proof.InfoHash())
	query := uuid.New()
	f.exec(t, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())`, query, f.source)
	f.exec(t, `INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)`, query, f.candidate)
	reserved, err := f.store.ReserveUpgrade(ctx, f.actor, f.command(t), fmt.Sprintf("paging-reserve-%03d", ordinal))
	if err != nil {
		t.Fatal("reserve", err)
	}
	out := &preparedUpgradeFixture{frozenUpgradeFixture: f, preparation: base.preparation, proof: proof, operation: reserved.OperationID}
	out.lease, err = out.preparation.ClaimMovieUpgradePreparation(ctx, f.actor, ports.ClaimMovieUpgradePreparation{OperationID: out.operation, Owner: uuid.New()})
	if err != nil {
		t.Fatal("preparation lease", err)
	}
	if _, err = out.preparation.PrepareMovieUpgrade(ctx, f.actor, out.prepare(), fmt.Sprintf("paging-prepare-%03d", ordinal)); err != nil {
		t.Fatal("prepare", err)
	}
	return out
}

func TestMovieUpgradeWatchIndependentTargetPaging65PG(t *testing.T) {
	remote := &moviePagingNative{tasks: make(map[string]*moviePagingRemote)}
	server := httptest.NewServer(http.HandlerFunc(remote.serve))
	t.Cleanup(server.Close)
	base := newMovieUpgradeWatchFixture(t, func(f *frozenUpgradeFixture) {
		f.exec(t, `UPDATE download_client SET url=$2 WHERE id=$1`, f.client, server.URL)
	})
	expected := map[uuid.UUID]ports.MovieUpgradeTrackingHintRequest{base.capture.MovieUpgradeTrackingHintRequest().TaskID: base.capture.MovieUpgradeTrackingHintRequest()}
	command, err := postgres.NewMovieUpgradeSendCommandRepository(base.preparation)
	if err != nil {
		t.Fatal(err)
	}
	issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, err := download.NewMovieUpgradeNativeSendDriver(base.manager, issuer, nativeUpgradeClock{})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := postgres.NewMovieUpgradeNativeJobSendRepository(base.preparation, driver, issuer)
	if err != nil {
		t.Fatal(err)
	}
	operations := make(map[uuid.UUID]uuid.UUID)
	for i := 1; i < 65; i++ {
		prepared := moviePagingPrepare(t, base, i)
		var task uuid.UUID
		if err = base.p.QueryRow(t.Context(), `SELECT task_id FROM movie_upgrade_preparation WHERE operation_id=$1`, prepared.operation).Scan(&task); err != nil {
			t.Fatal(err)
		}
		queued, err := command.QueueMovieUpgradeSend(t.Context(), base.actor, ports.QueueMovieUpgradeSend{OperationID: prepared.operation, TaskID: task, Confirmed: true}, fmt.Sprintf("paging-send-%03d", i))
		if err != nil {
			t.Fatal("queue send", err)
		}
		n := &nativeUpgradeFixture{preparedUpgradeFixture: prepared, manager: base.manager}
		result, err := sender.SendMovieUpgradeJob(t.Context(), base.actor, claimSendJob(t, n, queued), prepared.proof)
		if err != nil || result.Status != "confirmed" {
			t.Fatal("native send", err)
		}
		operations[prepared.operation] = task
	}
	// Only real tracking transactions update the predecessor; a due original
	// operation may obtain a newer partial before the event manifest is frozen.
	total := 0
	for {
		count, err := base.runtime.ScheduleMovieUpgradeTracking(t.Context())
		if err != nil {
			t.Fatal("schedule observations", err)
		}
		if count == 0 {
			break
		}
		for range count {
			req := base.claim(t, 2*time.Minute)
			result, err := base.jobRepo.TrackMovieUpgradeJob(t.Context(), base.actor, req)
			if err != nil || result == nil || result.Outcome != "tracking" {
				if result != nil {
					t.Fatalf("real partial outcome=%s reason=%s observed=%d err=%v", result.Outcome, result.Reason, total, err)
				}
				t.Fatal("real partial missing result", err)
			}
			task, ok := operations[req.OperationID]
			if !ok {
				task = base.capture.MovieUpgradeTrackingHintRequest().TaskID
				if req.OperationID != base.operation {
					t.Fatal("unknown operation")
				}
			}
			expected[task] = ports.MovieUpgradeTrackingHintRequest{OperationID: req.OperationID, TaskID: task, PriorObservationID: result.ObservationID}
			total++
		}
		if len(expected) == 65 {
			break
		}
		if total > 130 {
			t.Fatal("initial observations did not reach all operations")
		}
	}
	clock := &automaticTVPagingClock{now: time.Now(), wall: os.Getenv("PULSE_M8_WATCH_WALL_CLOCK") == "1"}
	watch, err := postgres.NewMovieUpgradeWatchStore(base.runtime, clock)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 256})
	if err != nil || !sources.Complete || len(sources.Sources) != 1 {
		t.Fatal("shared source", err)
	}
	source := sources.Sources[0]
	clock.now = time.Now()
	hint := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Fight.Club.1999.2160p.Bluray.x265.mkv", Metadata: ports.RootChangeMetadata{EventID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, ReceivedAt: clock.Now(), ManifestAt: source.ManifestAt, SourceIdentity: source.Binding.RootIdentity}}
	before := base.snapshot(t)
	seen := make(map[uuid.UUID]bool)
	cursor := ""
	for i := 0; i < 2; i++ {
		page, err := watch.ResolveDownloadWatchTargets(t.Context(), source.Binding, hint, ports.DownloadWatchTargetQuery{Cursor: cursor})
		want := 64
		if i == 1 {
			want = 1
		}
		if err != nil || len(page.Targets) != want || page.Complete != (i == 1) {
			t.Fatalf("page=%d targets=%d complete=%t error=%v", i, len(page.Targets), page.Complete, err)
		}
		for _, target := range page.Targets {
			req, ok := expected[target.TaskID]
			if !ok || seen[target.TaskID] || target.OperationID != req.OperationID || target.MovieUpgradeHintCapture == nil {
				t.Fatal("duplicate or wrong target")
			}
			captured := target.MovieUpgradeHintCapture.MovieUpgradeTrackingHintRequest()
			if captured.PriorObservationID != req.PriorObservationID || captured.EventID != hint.Metadata.EventID || captured.InstanceID != hint.Metadata.InstanceID || !captured.ReceivedAt.Equal(hint.Metadata.ReceivedAt) {
				t.Fatal("rebound predecessor/event")
			}
			seen[target.TaskID] = true
		}
		cursor = page.NextCursor
		if (i == 0) == (cursor == "") {
			t.Fatal("cursor boundary")
		}
	}
	if len(seen) != 65 || before != base.snapshot(t) {
		t.Fatal("omission or read changed protected graph")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.tasks) != 65 {
		t.Fatal("native hash count", len(remote.tasks))
	}
	for _, task := range remote.tasks {
		if task.adds != 1 {
			t.Fatal("Add repeated")
		}
	}
	if clock.wall {
		t.Logf("M8_WATCH_TARGET_WALL_CLOCK_PASS family=upgrade targets=65 pages=64+1 elapsed_ms=%d read_only=true predecessor_unchanged=true native_Add_each=1", time.Since(hint.Metadata.ReceivedAt).Milliseconds())
		return
	}
	t.Log("M8_UPGRADE_TARGET_PAGING_PASS targets=65 pages=64+1 independent_operations=true native_Add_each=1 native_partial=true predecessor_frozen=true read_only=true paging_clock=INJECTED thirty_second_gate_unchanged=true wall_clock_65_performance=NOT_MEASURED")
}
