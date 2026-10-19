package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// These fixtures exercise pure validation only, not execution authorization.
// Integration fixtures below must obtain authority from the real finalizer.
func automaticTVOrganizationProofFixture(t *testing.T) (ports.AutomaticTVExecutionAuthority, automaticTVOrganizationProof, []ports.AutomaticTVRemoteFile) {
	t.Helper()
	target := ports.AutomaticTVRoundSnapshot{SchemaVersion: "automatic-tv-round-v1", IdentityRuleVersion: media.AutomaticTVIdentityRuleVersion, ParserVersion: media.EpisodeParserVersion, OwnerID: uuid.New(), SubscriptionID: uuid.New(), WantedID: uuid.New(), MediaID: uuid.New(), RootID: uuid.New(), ClientID: uuid.New(), UnitID: uuid.New(), UnitKey: "S02E03", UnitVersion: 1, MediaVersion: 1, Season: 2, Episode: 3, EpisodeID: 10203, TMDBID: 55101, Year: 2024, Title: "Trusted Native Series", MappingMode: "standard", Rules: json.RawMessage(`{}`)}
	a := ports.AutomaticTVExecutionAuthority{SchemaVersion: "automatic-tv-execution-v1", Kind: "automatic_standard_single_episode_v1", OperationID: uuid.New(), RoundID: uuid.New(), QueryID: uuid.New(), CandidateID: uuid.New(), IndexerID: uuid.New(), Target: target, RootPathDigest: automaticTVOrganizationDigest([]byte("/library/tv")), IssuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a.SendValidUntil = a.IssuedAt.Add(5 * time.Minute)
	raw, _ := json.Marshal(a)
	a.DecisionStamp = automaticTVOrganizationDigest(raw)
	files := []media.TVTorrentFile{{Path: "Trusted Native Series/Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.mkv", Size: 1234}, {Path: "Trusted Native Series/notes.nfo", Size: 17}, {Path: "Trusted Native Series/zero.txt", Size: 0}}
	video, e := media.ValidateAutomaticTVSingleEpisodeFiles(files, media.AutomaticTVTorrentTarget{Unit: media.NumberingUnit{ID: target.UnitID, EpisodeID: target.EpisodeID, Season: 2, Episode: 3, Key: target.UnitKey, Version: 1}, Title: target.Title, Year: target.Year, TMDBID: target.TMDBID})
	if e != nil {
		t.Fatal(e)
	}
	p := automaticTVOrganizationProof{Kind: "automatic_tv_prepared_v1", Authority: a, SavePath: "/downloads", InfoHash: "0123456789012345678901234567890123456789", SHA256: [32]byte{1}, TotalBytes: 1251, Files: files, Video: *video}
	inventory := []ports.AutomaticTVRemoteFile{{Index: 0, Path: files[0].Path, Size: 1234, CompletedBytes: 1234, Wanted: true}, {Index: 1, Path: files[1].Path, Size: 17, CompletedBytes: 17, Wanted: false}, {Index: 2, Path: files[2].Path, Size: 0, CompletedBytes: 0, Wanted: false}}
	return a, p, inventory
}

func TestAutomaticTVOrganizationOriginalCoverage(t *testing.T) {
	a, p, inventory := automaticTVOrganizationProofFixture(t)
	raw, _ := json.Marshal(a)
	// The first-send deadline expired months ago and is not an import deadline.
	if _, e := automaticTVOrganizationAuthority(raw, a.OperationID); e != nil {
		t.Fatal("old first-send expiry revoked ongoing proof", e)
	}
	proof, _ := json.Marshal(p)
	native, _ := json.Marshal(inventory)
	got, e := automaticTVOrganizationVideo(proof, a, native)
	if e != nil || got.Video.Size != 1234 || got.TotalBytes != 1251 {
		t.Fatal("video/attachment sizes lost", e)
	}
	source, e := automaticTVOrganizationSource(map[string]string{"/downloads": "/local/downloads"}, got)
	if e != nil || source != filepath.Join("/local/downloads", p.Video.Path) {
		t.Fatal("video must come from save_path and complete inventory", e)
	}
}

func TestAutomaticTVOrganizationRejectUnboundInventory(t *testing.T) {
	for _, scenario := range []string{"missing-unwanted", "missing-zero", "changed-unwanted", "duplicate-index", "partial-video", "extra-video", "mixed-authority", "wrong-unit", "wrong-total", "wrong-video-size", "escape"} {
		t.Run(scenario, func(t *testing.T) {
			a, p, files := automaticTVOrganizationProofFixture(t)
			switch scenario {
			case "missing-unwanted":
				files = append(files[:1], files[2:]...)
			case "missing-zero":
				files = files[:2]
			case "changed-unwanted":
				files[1].Path = "Trusted Native Series/different.nfo"
			case "duplicate-index":
				files[1].Index = 0
			case "partial-video":
				files[0].CompletedBytes--
			case "extra-video":
				p.Files = append(p.Files, media.TVTorrentFile{Path: "Trusted Native Series/Trusted.Native.Series.2024.S02E04.mkv", Size: 9})
			case "mixed-authority":
				p.Authority.OperationID = uuid.New()
			case "wrong-unit":
				p.Video.Unit.UnitID = uuid.New()
			case "wrong-total":
				p.TotalBytes--
			case "wrong-video-size":
				p.Video.Size = p.TotalBytes
			case "escape":
				p.Files[0].Path = "../outside.mkv"
			}
			raw, _ := json.Marshal(p)
			native, _ := json.Marshal(files)
			if _, e := automaticTVOrganizationVideo(raw, a, native); !errors.Is(e, ErrReviewRequired) {
				t.Fatal("unbound proof accepted", e)
			}
		})
	}
}

func TestAutomaticTVOrganizationRejectMixedTargetEvidence(t *testing.T) {
	a, p, _ := automaticTVOrganizationProofFixture(t)
	target := automaticTVOrganizationTarget(a, uuid.New(), automaticTVOrganizationDigest([]byte("inventory")), filepath.Join(p.SavePath, p.Video.Path), p.Video.Size)
	if !automaticTVOrganizationTargetValid(a, target) {
		t.Fatal("valid automatic evidence rejected")
	}
	for _, scenario := range []string{"manual", "complete", "file-action", "no-authority", "other-operation", "total-size", "old-parser"} {
		t.Run(scenario, func(t *testing.T) {
			f := target
			switch scenario {
			case "manual":
				f.EpisodeOperationID = a.OperationID
			case "complete":
				f.CompleteTVPlanID = uuid.New()
			case "file-action":
				f.FileAction = &fileActionEvidence{}
			case "no-authority":
				f.AutomaticTVOperationID = nil
			case "other-operation":
				op := uuid.New()
				f.AutomaticTVOperationID = &op
			case "total-size":
				f.InventorySizeBytes = 0
			case "old-parser":
				f.ParserVersion = "episode.v0"
			}
			if automaticTVOrganizationTargetValid(a, f) {
				t.Fatal("mixed/invalid automatic target accepted")
			}
		})
	}
}

func TestAutomaticTVOrganizationLegacyEvidenceJSON(t *testing.T) {
	legacy := transferTargetEvidence{UnitID: uuid.New(), UnitKey: "movie", UnitVersion: 1, MediaID: uuid.New(), MediaVersion: 1, ParserVersion: media.EpisodeParserVersion}
	raw, e := json.Marshal(legacy)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid evidence")
	}
	for _, name := range []string{"automatic_tv_operation_id", "automatic_tv_inventory_stamp"} {
		if _, ok := fields[name]; ok {
			t.Fatalf("legacy evidence gained %s", name)
		}
	}
}

type automaticTVOrganizationFixture struct {
	pool                                                              *pgxpool.Pool
	svc                                                               *Service
	q                                                                 *db.Queries
	manager                                                           *download.Manager
	dsn, rootPath, sourcePath, hash, video                            string
	root, owner, subscription, unit, indexer, client, operation, task uuid.UUID
	lease                                                             ports.JobLease
	adds                                                              atomic.Int32
	raw                                                               []byte
}

func automaticTVOrganizationExec(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, e := pool.Exec(t.Context(), statement, args...); e != nil {
		t.Fatal(e)
	}
}
func automaticTVOrganizationCount(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) int {
	t.Helper()
	var n int
	if e := pool.QueryRow(t.Context(), statement, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func automaticTVOrganizationClaim(t *testing.T, q *db.Queries, kind string) ports.JobLease {
	t.Helper()
	job, e := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{kind}, Column2: "automatic-tv-organization-test", Column3: 120000})
	if e != nil {
		t.Fatal("claim "+kind, e)
	}
	return ports.JobLease{ID: job.ID, Owner: "automatic-tv-organization-test", Token: job.LeaseToken}
}

// Only catalog and native HTTP transports are fixtures. Source proof, decision,
// preparation, Add, complete inventory and organization all use production code.
func newAutomaticTVOrganizationFixture(t *testing.T) *automaticTVOrganizationFixture {
	t.Helper()
	if postgres.CurrentMigrationVersion != 91 {
		t.Fatal("91 PG tests require Main's shared hooks and Current91 release")
	}
	f := &automaticTVOrganizationFixture{dsn: pgtest.GetCluster(t).NewTestDB(t), root: uuid.New(), owner: uuid.New(), subscription: uuid.New(), unit: uuid.New(), indexer: uuid.New(), client: uuid.New(), rootPath: t.TempDir(), video: "Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.H264.mkv"}
	if v, e := postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); e != nil || v != 91 {
		t.Fatal("migration91", v, e)
	}
	var e error
	f.pool, e = postgres.NewPool(t.Context(), f.dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(f.pool.Close)
	f.q = db.New(f.pool)
	var version string
	if e = f.pool.QueryRow(t.Context(), "SHOW server_version").Scan(&version); e != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("PG18.6 required", version, e)
	}
	if e = os.WriteFile(filepath.Join(f.rootPath, ".pulsemedia_root"), []byte("native-test-root"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: f.root, Path: f.rootPath, Name: "TV", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	// A real successful empty scan establishes the generation's first root inode.
	if _, e = library.NewScanner(idgen.New(), rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(t.Context(), f.root); e != nil {
		t.Fatal("real baseline scan", e)
	}
	parent := "Trusted Native Series"
	f.raw = []byte(fmt.Sprintf("d4:infod5:filesld6:lengthi1234e4:pathl%d:%seed6:lengthi17e4:pathl8:note.nfoeed6:lengthi0e4:pathl8:zero.txteee4:name%d:%s12:piece lengthi16384e6:pieces20:00000000000000000000ee", len(f.video), f.video, len(parent), parent))
	metainfo, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, f.raw)
	if e != nil {
		t.Fatal("actual metainfo", e)
	}
	f.hash = metainfo.InfoHash()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			if r.URL.Query().Get("t") != "tvsearch" || r.URL.Query().Get("season") != "2" || r.URL.Query().Get("ep") != "3" {
				t.Error("source lost canonical TV query")
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="1"/><item><title>Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.H264</title><guid>organization-source</guid><enclosure url="http://%s/download" length="1251"/><torznab:attr name="seeders" value="9"/></item></channel></rss>`, r.Host)
		case "/download":
			_, _ = w.Write(f.raw)
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "fixture", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			if f.adds.Load() == 0 {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"hash": f.hash, "save_path": f.rootPath, "content_path": filepath.Join(f.rootPath, parent), "name": parent, "tags": "pulsemedia,pulsemedia:op:" + f.operation.String(), "state": "uploading", "total_size": 1251, "completed": 1251, "progress": 1}})
		case "/api/v2/torrents/add":
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				t.Error(e)
				http.Error(w, "bad form", 400)
				return
			}
			defer func() {
				if r.MultipartForm != nil {
					_ = r.MultipartForm.RemoveAll()
				}
			}()
			torrentFile, _, e := r.FormFile("torrents")
			if e != nil {
				t.Error("original byte upload missing", e)
				return
			}
			raw, e := io.ReadAll(torrentFile)
			_ = torrentFile.Close()
			if e != nil || !bytes.Equal(raw, f.raw) || r.FormValue("urls") != "" || r.FormValue("savepath") != f.rootPath || r.FormValue("tags") != "pulsemedia,pulsemedia:op:"+f.operation.String() {
				t.Error("Add changed original bytes/path/operation")
			}
			f.adds.Add(1)
			_, _ = fmt.Fprintf(w, `{"added_torrent_ids":["%s"],"failure_count":0,"pending_count":0,"success_count":1}`, f.hash)
		case "/api/v2/torrents/files":
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"index": 0, "name": parent + "/" + f.video, "size": 1234, "priority": 1, "progress": 1}, map[string]any{"index": 1, "name": parent + "/note.nfo", "size": 17, "priority": 0, "progress": 1}, map[string]any{"index": 2, "name": parent + "/zero.txt", "size": 0, "priority": 0, "progress": 1}})
		default:
			t.Error("unexpected native route", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	secret, e := codec.EncryptSecret([]byte("fixture-api-key"))
	if e != nil {
		t.Fatal(e)
	}
	endpoint, e := codec.EncryptSecret([]byte(server.URL + "/api?apikey=fixture-api-key"))
	if e != nil {
		t.Fatal(e)
	}
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'organization-member','fixture','member',true)`, f.owner)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'organization-source',$2,$3,'[5000,5040]','online')`, f.indexer, server.URL, secret)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) VALUES($1,'torznab',$2,'explicit-v1',1)`, f.indexer, endpoint)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,true)`, f.indexer)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'organization-native','qbittorrent',$2,'fixture',$3,'{}')`, f.client, server.URL, secret)
	m := uuid.New()
	wanted := uuid.New()
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO media(id,title,media_type,year) VALUES($1,'Trusted Native Series','tv',2024)`, m)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','55101')`, uuid.New(), m)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number,air_date) VALUES($1,$2,'S02E03',2,3,'2020-03-04')`, f.unit, m)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',10203,55101)`, f.unit)
	bound, _ := json.Marshal([]uuid.UUID{f.indexer})
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision,indexer_ids,downloader_id,state,scope_type,upgrade_mode) SELECT $1,$2,$3,$4,'default',current_revision,$5,$6,'active','all','disabled' FROM policy WHERE id='default'`, f.subscription, f.owner, m, f.root, bound, f.client)
	automaticTVOrganizationExec(t, f.pool, `INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version,next_evaluate_at) VALUES($1,$2,$3,1,clock_timestamp())`, wanted, f.subscription, f.unit)
	rounds := postgres.NewAutomaticTVRoundStore(f.pool, idgen.New())
	round, e := rounds.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: wanted})
	if e != nil {
		t.Fatal("round87", e)
	}
	sourceLease := automaticTVOrganizationClaim(t, f.q, "automatic_tv_source")
	source := search.NewService(postgres.NewSearchExecutionStore(f.pool, codec), idgen.New(), codec, 0)
	source.SetAutomaticTVRoundRepository(rounds)
	source.RegisterClient(f.indexer, torznab.NewClient(server.URL+"/api?apikey=fixture-api-key", "fixture-api-key", &http.Client{Timeout: 5 * time.Second}))
	if e = source.ExecuteAutomaticTVSource(t.Context(), ports.AutomaticTVSourceTask{RoundID: round.ID, IndexerID: f.indexer, Lease: sourceLease}); e != nil {
		t.Fatal("native87 source", e)
	}
	automaticTVOrganizationExec(t, f.pool, `UPDATE job SET not_before=clock_timestamp() WHERE id=(SELECT finalize_job_id FROM automatic_tv_round WHERE id=$1)`, round.ID)
	finalizeLease := automaticTVOrganizationClaim(t, f.q, "automatic_tv_finalize")
	decision, e := postgres.NewAutomaticTVAcquisitionStore(f.pool, idgen.New(), acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, finalizeLease)
	if e != nil || decision == nil || decision.State != "reserved" {
		t.Fatal("authority88", decision, e)
	}
	f.operation = decision.OperationID
	sender := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), codec)
	f.manager = download.NewManager(f.pool, idgen.New(), bytes.Repeat([]byte{27}, 32))
	f.manager.SetClientFactory(func(kind, address, user, password string) (ports.DownloadClient, error) {
		if kind != "qbittorrent" || address != server.URL || user != "fixture" || password != "fixture-api-key" {
			return nil, ports.ErrBadResponse
		}
		return qbittorrent.NewClient(address, user, password, &http.Client{Timeout: 5 * time.Second})
	})
	f.manager.SetAutomaticTVServices(sender, torznab.NewAutomaticTVTorrentFetcher(&http.Client{Timeout: 5 * time.Second}), torrent.Inspector{})
	sendLease := automaticTVOrganizationClaim(t, f.q, "automatic_tv_download_submission")
	sent, e := f.manager.SendAutomaticTV(t.Context(), ports.AutomaticTVSendRequest{OperationID: f.operation, Lease: sendLease})
	if e != nil || sent == nil || sent.State != "tracking" {
		t.Fatal("original89 Add", sent, e)
	}
	task, e := f.q.GetDownloadTaskByOpID(t.Context(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	f.task = task.ID
	observer := postgres.NewAutomaticTVObservationStore(f.pool, idgen.New())
	n, e := observer.ScheduleAutomaticTVObservations(t.Context(), 10)
	if e != nil || n != 1 {
		t.Fatal("observation90 schedule", n, e)
	}
	observationLease := automaticTVOrganizationClaim(t, f.q, ports.AutomaticTVObservationJobKind)
	observed, e := f.manager.ObserveAutomaticTV(t.Context(), observer, ports.AutomaticTVObservationRequest{OperationID: f.operation, TaskID: f.task, Lease: observationLease})
	if e != nil || observed == nil || !observed.Complete || observed.ReviewRequired {
		t.Fatal("native90 complete inventory", observed, e)
	}
	sourceDir := filepath.Join(f.rootPath, parent)
	if e = os.Mkdir(sourceDir, 0700); e != nil {
		t.Fatal(e)
	}
	f.sourcePath = filepath.Join(sourceDir, f.video)
	for path, raw := range map[string][]byte{f.sourcePath: bytes.Repeat([]byte("v"), 1234), filepath.Join(sourceDir, "note.nfo"): bytes.Repeat([]byte("n"), 17), filepath.Join(sourceDir, "zero.txt"): nil} {
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	f.lease = automaticTVOrganizationClaim(t, f.q, "download_organization")
	f.svc = NewService(f.pool, idgen.New(), outbox.New(f.pool, idgen.New(), clock.New()), rootfs.NewFilesystem())
	return f
}

func (f *automaticTVOrganizationFixture) assertUnpublished(t *testing.T) {
	t.Helper()
	for _, statement := range []string{`SELECT count(*) FROM unit_fact WHERE stage='imported'`, `SELECT count(*) FROM library_file_unit`, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, `SELECT count(*) FROM automatic_tv_organization_completion`, `SELECT count(*) FROM download_task WHERE organization_state='completed'`, `SELECT count(*) FROM job WHERE kind='download_organization' AND state='succeeded'`} {
		if n := automaticTVOrganizationCount(t, f.pool, statement); n != 0 {
			t.Fatal("partial publication", statement, n)
		}
	}
	for _, statement := range []string{`SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL`, `SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state='dispatched'`, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND state='pending'`} {
		if n := automaticTVOrganizationCount(t, f.pool, statement, f.operation); n != 1 {
			t.Fatal("protection lost", statement, n)
		}
	}
	if f.adds.Load() != 1 {
		t.Fatal("Add replayed", f.adds.Load())
	}
}

func (f *automaticTVOrganizationFixture) assertImported(t *testing.T) {
	t.Helper()
	for _, statement := range []string{`SELECT count(*) FROM library_file`, `SELECT count(*) FROM library_file_unit`, `SELECT count(*) FROM unit_fact WHERE stage='imported' AND revoked_at IS NULL`, `SELECT count(*) FROM outbox_event WHERE event_type='media.imported'`, `SELECT count(*) FROM automatic_tv_organization_completion`, `SELECT count(*) FROM transfer_plan WHERE state='completed'`, `SELECT count(*) FROM transfer_step WHERE state='completed'`, `SELECT count(*) FROM download_task WHERE organization_state='completed'`, `SELECT count(*) FROM job WHERE kind='download_organization' AND state='succeeded'`, `SELECT count(*) FROM unit_reservation WHERE state='consumed'`, `SELECT count(*) FROM acquisition_reservation WHERE released_at IS NOT NULL`, `SELECT count(*) FROM download_requirement WHERE state='imported'`} {
		if n := automaticTVOrganizationCount(t, f.pool, statement); n != 1 {
			t.Fatal("single atomic import missing", statement, n)
		}
	}
	plan, e := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	steps, e := f.q.ListStepsForPlan(t.Context(), plan.ID)
	if e != nil || len(steps) != 1 {
		t.Fatal("single video step", e)
	}
	source, e := os.Stat(f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.Stat(steps[0].TargetPath)
	if e != nil || !os.SameFile(source, target) || target.Size() != 1234 {
		t.Fatal("hardlink inode/video bytes", e)
	}
	for path, want := range map[string]int64{"note.nfo": 17, "zero.txt": 0} {
		raw, e := os.ReadFile(filepath.Join(filepath.Dir(f.sourcePath), path))
		if e != nil || int64(len(raw)) != want {
			t.Fatal("source attachment changed", path, e)
		}
	}
	if f.adds.Load() != 1 {
		t.Fatal("Add replayed")
	}
}

func TestAutomaticTVOrganizationNativeAtomicPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	if e := f.svc.OrganizeDownload(t.Context(), f.lease); e != nil {
		t.Fatal("production organization", e)
	}
	f.assertImported(t)
	plan, e := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		handled, e := f.svc.confirmAutomaticTVOrganization(t.Context(), f.lease, plan)
		if e != nil || !handled {
			t.Fatal("read-only stable terminal confirmation", handled, e)
		}
	}
	f.assertImported(t)
}

func TestAutomaticTVOrganizationCurrentQualificationPG(t *testing.T) {
	for _, scenario := range []string{"pause", "skip", "account", "source", "client", "policy", "unit", "root", "mapping"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			switch scenario {
			case "pause":
				automaticTVOrganizationExec(t, f.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, f.subscription)
			case "skip":
				automaticTVOrganizationExec(t, f.pool, `UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE subscription_id=$1`, f.subscription)
			case "account":
				automaticTVOrganizationExec(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, f.owner)
			case "source":
				automaticTVOrganizationExec(t, f.pool, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, f.indexer)
			case "client":
				automaticTVOrganizationExec(t, f.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client)
			case "policy":
				automaticTVOrganizationExec(t, f.pool, `INSERT INTO policy_revision(id,policy_id,revision,rules) SELECT gen_random_uuid(),s.quality_policy_id,s.quality_policy_revision+1,p.rules FROM subscription s JOIN policy_revision p ON p.policy_id=s.quality_policy_id AND p.revision=s.quality_policy_revision WHERE s.id=$1`, f.subscription)
				automaticTVOrganizationExec(t, f.pool, `UPDATE policy SET current_revision=current_revision+1 WHERE id=(SELECT quality_policy_id FROM subscription WHERE id=$1)`, f.subscription)
			case "unit":
				automaticTVOrganizationExec(t, f.pool, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, f.unit)
			case "root":
				automaticTVOrganizationExec(t, f.pool, `UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1`, f.root)
			case "mapping":
				automaticTVOrganizationExec(t, f.pool, `INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,1,s.media_id,m.metadata_version,s.intent_version+1,'standard',repeat('a',64),repeat('b',64),'{}','[]' FROM subscription s JOIN media m ON m.id=s.media_id WHERE s.id=$1`, f.subscription)
				automaticTVOrganizationExec(t, f.pool, `UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id=$1`, f.subscription)
				automaticTVOrganizationExec(t, f.pool, `UPDATE wanted_item SET intent_version=(SELECT intent_version FROM subscription WHERE id=$1) WHERE subscription_id=$1`, f.subscription)
			}
			if e := f.svc.OrganizeDownload(t.Context(), f.lease); e == nil {
				t.Fatal("changed current permission accepted")
			}
			f.assertUnpublished(t)
		})
	}
}

type automaticTVOrganizationFilesystem struct {
	ports.TransferFilesystem
	after func()
}

func (f *automaticTVOrganizationFilesystem) Hardlink(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, expected ports.FileIdentity) error {
	if e := f.TransferFilesystem.Hardlink(ctx, source, sourceRel, target, targetRel, expected); e != nil {
		return e
	}
	if f.after != nil {
		f.after()
	}
	return nil
}

func TestAutomaticTVOrganizationRollbackAndSamePlanRetryPG(t *testing.T) {
	for _, stage := range []string{"after-hardlink", "imported-event", "finish-job", "deferred", "deferred-lease-expiry", "lease-tail", "freshness-lock-wait"} {
		t.Run(stage, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			if stage == "after-hardlink" {
				f.svc.files = &automaticTVOrganizationFilesystem{TransferFilesystem: rootfs.NewFilesystem(), after: func() {
					automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fault after hardlink'; END $$`)
					automaticTVOrganizationExec(t, f.pool, `CREATE TRIGGER org_fault BEFORE INSERT ON library_file FOR EACH ROW EXECUTE FUNCTION org_fault()`)
				}}
			}
			switch stage {
			case "imported-event":
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='media.imported' THEN RAISE EXCEPTION 'imported fault'; END IF; RETURN NEW; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE TRIGGER org_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION org_fault()`)
			case "finish-job":
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='download_organization' AND NEW.state='succeeded' THEN RAISE EXCEPTION 'finish fault'; END IF; RETURN NEW; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE TRIGGER org_fault BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION org_fault()`)
			case "deferred":
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'deferred fault'; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE CONSTRAINT TRIGGER org_fault AFTER INSERT ON automatic_tv_organization_completion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION org_fault()`)
			case "deferred-lease-expiry":
				f.svc.files = &automaticTVOrganizationFilesystem{TransferFilesystem: rootfs.NewFilesystem(), after: func() {
					automaticTVOrganizationExec(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '2 seconds' WHERE id=$1`, f.lease.ID)
				}}
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2.1); RETURN NEW; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE CONSTRAINT TRIGGER org_fault AFTER INSERT ON automatic_tv_organization_completion DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION org_fault()`)
			case "lease-tail":
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='download_organization' AND NEW.state='succeeded' THEN PERFORM set_config('pulse.org_clock','expired',true); END IF; RETURN NEW; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE TRIGGER org_fault AFTER UPDATE ON job FOR EACH ROW EXECUTE FUNCTION org_fault()`)
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+CASE WHEN current_setting('pulse.org_clock',true)='expired' THEN interval '1 day' ELSE interval '0' END`)
				automaticTVOrganizationExec(t, f.pool, `ALTER DATABASE `+pgxpoolIdentifier(f.dsn)+` SET search_path='public','pg_catalog'`)
				f.pool.Reset()
			case "freshness-lock-wait":
				automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION org_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(5.05); RETURN NEW; END $$`)
				automaticTVOrganizationExec(t, f.pool, `CREATE TRIGGER org_fault BEFORE INSERT ON unit_fact FOR EACH ROW EXECUTE FUNCTION org_fault()`)
			}
			started := time.Now()
			if e := f.svc.OrganizeDownload(t.Context(), f.lease); e == nil {
				t.Fatal("fault published import")
			} else if stage == "deferred-lease-expiry" {
				var databaseGuard interface{ SQLState() string }
				rejected := errors.Is(e, ErrExecutionRejected) || (errors.As(e, &databaseGuard) && databaseGuard.SQLState() == "P0001")
				if !rejected || time.Since(started) < 2*time.Second {
					t.Fatal("expected final guard rejection after deferred sleep expired the lease", e)
				}
				if automaticTVOrganizationCount(t, f.pool, `SELECT count(*) FROM job WHERE id=$1 AND lease_until<clock_timestamp()`, f.lease.ID) != 1 {
					t.Fatal("deferred constraint did not cross the actual database lease deadline")
				}
			}
			f.assertUnpublished(t)
			plan, e := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
			if e != nil {
				t.Fatal("original plan lost", e)
			}
			steps, e := f.q.ListStepsForPlan(t.Context(), plan.ID)
			if e != nil || len(steps) != 1 {
				t.Fatal(e)
			}
			source, e := os.Stat(f.sourcePath)
			if e != nil {
				t.Fatal(e)
			}
			target, e := os.Stat(steps[0].TargetPath)
			if e != nil || !os.SameFile(source, target) {
				t.Fatal("rollback removed or replaced physical hardlink", e)
			}
			tables := map[string]string{"after-hardlink": "library_file", "imported-event": "outbox_event", "finish-job": "job", "deferred": "automatic_tv_organization_completion", "deferred-lease-expiry": "automatic_tv_organization_completion", "lease-tail": "job", "freshness-lock-wait": "unit_fact"}
			automaticTVOrganizationExec(t, f.pool, `DROP TRIGGER org_fault ON `+tables[stage])
			automaticTVOrganizationExec(t, f.pool, `DROP FUNCTION org_fault()`)
			// Simulate the persisted old worker expiring; reclaim the SAME job and plan.
			automaticTVOrganizationExec(t, f.pool, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, plan.ID)
			automaticTVOrganizationExec(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.lease.ID)
			automaticTVOrganizationExec(t, f.pool, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, f.lease.ID)
			f.lease = automaticTVOrganizationClaim(t, f.q, "download_organization")
			f.svc = NewService(f.pool, idgen.New(), outbox.New(f.pool, idgen.New(), clock.New()), rootfs.NewFilesystem())
			if e = f.svc.OrganizeDownload(t.Context(), f.lease); e != nil {
				t.Fatal("same-plan restart retry", e)
			}
			f.assertImported(t)
			current, e := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
			if e != nil || current.ID != plan.ID {
				t.Fatal("retry replaced original plan", e)
			}
			resumed, e := os.Stat(steps[0].TargetPath)
			if e != nil || !os.SameFile(target, resumed) {
				t.Fatal("retry overwrote original hardlink", e)
			}
		})
	}
}

func pgxpoolIdentifier(dsn string) string {
	config, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		panic(e)
	}
	return `"` + strings.ReplaceAll(config.ConnConfig.Database, `"`, `""`) + `"`
}

func TestAutomaticTVOrganizationNamespaceReplacementPG(t *testing.T) {
	for _, scenario := range []string{"source-directory", "source-directory-symlink", "target-root", "marker", "source-tamper", "target-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			switch scenario {
			case "source-tamper":
				f.svc.files = &automaticTVOrganizationFilesystem{TransferFilesystem: rootfs.NewFilesystem(), after: func() {
					if e := os.WriteFile(f.sourcePath, bytes.Repeat([]byte("x"), 1234), 0600); e != nil {
						t.Error(e)
					}
				}}
			case "target-conflict":
				// The canonical destination is calculated by the production plan builder.
				plan, e := f.svc.createAutomaticTVOrganizationPlan(t.Context(), f.lease, f.operation)
				if e != nil {
					t.Fatal(e)
				}
				steps, e := f.q.ListStepsForPlan(t.Context(), plan.ID)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.MkdirAll(filepath.Dir(steps[0].TargetPath), 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(steps[0].TargetPath, []byte("protected-conflict"), 0600); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() {
					raw, e := os.ReadFile(steps[0].TargetPath)
					if e != nil || string(raw) != "protected-conflict" {
						t.Error("organization overwrote conflicting target", e)
					}
				})
			default:
				f.svc.files = &automaticTVOrganizationFilesystem{TransferFilesystem: rootfs.NewFilesystem(), after: func() {
					switch scenario {
					case "source-directory", "source-directory-symlink":
						dir := filepath.Dir(f.sourcePath)
						old := dir + "-old"
						if e := os.Rename(dir, old); e != nil {
							t.Error(e)
							return
						}
						if scenario == "source-directory-symlink" {
							if e := os.Symlink(old, dir); e != nil {
								t.Error(e)
							}
							return
						}
						if e := os.Mkdir(dir, 0700); e != nil {
							t.Error(e)
							return
						}
						if e := os.Link(filepath.Join(old, f.video), f.sourcePath); e != nil {
							t.Error(e)
						}
					case "target-root":
						old := f.rootPath + "-old"
						if e := os.Rename(f.rootPath, old); e != nil {
							t.Error(e)
							return
						}
						t.Cleanup(func() { _ = os.RemoveAll(old) })
						if e := os.Mkdir(f.rootPath, 0700); e != nil {
							t.Error(e)
						}
						if e := os.WriteFile(filepath.Join(f.rootPath, ".pulsemedia_root"), []byte("native-test-root"), 0600); e != nil {
							t.Error(e)
						}
					case "marker":
						if e := os.Remove(filepath.Join(f.rootPath, ".pulsemedia_root")); e != nil {
							t.Error(e)
						}
						if e := os.WriteFile(filepath.Join(f.rootPath, ".pulsemedia_root"), []byte("replaced-marker"), 0600); e != nil {
							t.Error(e)
						}
					}
				}}
			}
			if e := f.svc.OrganizeDownload(t.Context(), f.lease); e == nil {
				t.Fatal("changed physical namespace accepted")
			}
			f.assertUnpublished(t)
		})
	}
}

func TestAutomaticTVOrganizationExpiredFirstSendStillImportsPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	automaticTVOrganizationExec(t, f.pool, `UPDATE job SET lease_until=pg_catalog.clock_timestamp()+interval '1 day' WHERE id=$1`, f.lease.ID)
	automaticTVOrganizationExec(t, f.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+interval '6 minutes'`)
	automaticTVOrganizationExec(t, f.pool, `ALTER DATABASE `+pgxpoolIdentifier(f.dsn)+` SET search_path='public','pg_catalog'`)
	f.pool.Reset()
	if e := f.svc.OrganizeDownload(t.Context(), f.lease); e != nil {
		t.Fatal("first-send TTL incorrectly limited ongoing organization", e)
	}
	f.assertImported(t)
}

func TestAutomaticTVOrganizationAfterPhysicalCurrentChangePG(t *testing.T) {
	for _, scenario := range []string{"pause", "owner", "client"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			f.svc.files = &automaticTVOrganizationFilesystem{TransferFilesystem: rootfs.NewFilesystem(), after: func() {
				switch scenario {
				case "pause":
					automaticTVOrganizationExec(t, f.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, f.subscription)
				case "owner":
					automaticTVOrganizationExec(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, f.owner)
				case "client":
					automaticTVOrganizationExec(t, f.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client)
				}
			}}
			if e := f.svc.OrganizeDownload(t.Context(), f.lease); e == nil {
				t.Fatal("post-hardlink current permission change accepted")
			}
			f.assertUnpublished(t)
			plan, e := f.q.GetTransferPlanByOpID(t.Context(), f.operation)
			if e != nil {
				t.Fatal(e)
			}
			steps, e := f.q.ListStepsForPlan(t.Context(), plan.ID)
			if e != nil || len(steps) != 1 {
				t.Fatal(e)
			}
			source, e := os.Stat(f.sourcePath)
			if e != nil {
				t.Fatal(e)
			}
			target, e := os.Stat(steps[0].TargetPath)
			if e != nil || !os.SameFile(source, target) {
				t.Fatal("physical link lost after current change", e)
			}
		})
	}
}

func TestAutomaticTVOrganizationDirectCompletionRejectedPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	for _, statement := range []string{`UPDATE download_task SET organization_state='completed' WHERE id=$1`, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE id=$1`} {
		id := f.task
		if strings.Contains(statement, "UPDATE job") {
			id = f.lease.ID
		}
		if _, e := f.pool.Exec(t.Context(), statement, id); e == nil {
			t.Fatal("generic completion bypassed original plan proof")
		}
	}
	f.assertUnpublished(t)
	plan, e := f.svc.createAutomaticTVOrganizationPlan(t.Context(), f.lease, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE transfer_plan SET state='completed' WHERE id=$1`, plan.ID); e == nil {
		t.Fatal("plan completion bypassed atomic proof")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE transfer_step SET state='completed' WHERE plan_id=$1`, plan.ID); e == nil {
		t.Fatal("step completion bypassed atomic proof")
	}
	f.assertUnpublished(t)
}

func TestAutomaticTVOrganizationSameGenerationReplacementPG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	old := f.rootPath + "-original"
	if e := os.Rename(f.rootPath, old); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := os.RemoveAll(old); e != nil {
			t.Error(e)
		}
	})
	if e := os.MkdirAll(filepath.Dir(f.sourcePath), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(filepath.Join(old, "Trusted Native Series", f.video), f.sourcePath); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(f.rootPath, ".pulsemedia_root"), []byte("native-test-root"), 0600); e != nil {
		t.Fatal(e)
	}
	// The configured path, marker name, generation and baseline flag all match;
	// only the physical root differs from its FIRST successful scan.
	if e := f.svc.OrganizeDownload(t.Context(), f.lease); e == nil {
		t.Fatal("replacement root inherited old generation")
	}
	f.assertUnpublished(t)
}

func TestAutomaticTVOrganizationConcurrentDowngradePG(t *testing.T) {
	f := newAutomaticTVOrganizationFixture(t)
	if _, e := f.svc.createAutomaticTVOrganizationPlan(t.Context(), f.lease, f.operation); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	holder, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = holder.Exec(ctx, `SELECT plan_id FROM automatic_tv_organization_plan FOR UPDATE`); e != nil {
		_ = holder.Rollback(context.WithoutCancel(ctx))
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, err := postgres.Rollback(ctx, f.dsn, "../../../migrations"); done <- err }()
	defer func() {
		_ = holder.Rollback(context.WithoutCancel(ctx))
		select {
		case <-done:
		default:
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("downgrade process did not join")
			}
		}
	}()
	// A real table lock makes down wait behind the original history writer.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	waiting := false
	for !waiting {
		select {
		case <-deadline.C:
			t.Fatal("downgrade did not wait on history lock")
		case <-tick.C:
			var n int
			e = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid()`).Scan(&n)
			if e != nil {
				t.Fatal(e)
			}
			waiting = n > 0
		}
	}
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		done <- e
		if e == nil {
			t.Fatal("concurrent downgrade discarded active plan history")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if e = postgres.CheckSchemaVersion(t.Context(), f.pool, 91); e != nil {
		t.Fatal(e)
	}
	f.assertUnpublished(t)
}

func TestAutomaticTVOrganizationMigration91PG(t *testing.T) {
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 91 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(t.Context(), dsn, "../../../migrations"); e != nil || v != 90 {
		t.Fatal("empty down", v, e)
	}
	if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 91 {
		t.Fatal("empty up again", v, e)
	}
	f := newAutomaticTVOrganizationFixture(t)
	if e := f.svc.OrganizeDownload(t.Context(), f.lease); e != nil {
		t.Fatal(e)
	}
	if _, e := postgres.Rollback(t.Context(), f.dsn, "../../../migrations"); e == nil {
		t.Fatal("91 history discarded by downgrade")
	}
	for _, statement := range []string{`DELETE FROM automatic_tv_organization_plan`, `UPDATE automatic_tv_organization_completion SET lease_token=lease_token+1`, `TRUNCATE automatic_tv_organization_observation`} {
		if _, e := f.pool.Exec(t.Context(), statement); e == nil {
			t.Fatal("immutable organization evidence changed", statement)
		}
	}
	f.assertImported(t)
}
