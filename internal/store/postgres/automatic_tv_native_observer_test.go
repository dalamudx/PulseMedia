package postgres_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type automaticTVNativeObservationHarness struct {
	*automaticTVSenderHarness
	observer            *postgres.AutomaticTVObservationStore
	request             ports.AutomaticTVObservationRequest
	kind, mode, video   string
	reads, lists, files atomic.Int32
	readHook            func()
}

// Use the real source proof, atomic finalizer, first-send preparation and native
// Add. Only catalog setup and HTTP transport are fixtures; no authority is forged.
func newAutomaticTVNativeObservationHarness(t *testing.T, kind string) *automaticTVNativeObservationHarness {
	t.Helper()
	f := newAutomaticTVFixture(t)
	if kind != "qbittorrent" {
		execIntent(t, f.pool, `UPDATE download_client SET client_type=$2 WHERE id=$1`, f.client, kind)
	}
	video := "Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.mkv"
	rootName := "Trusted.Native.Series.2024"
	raw := []byte(fmt.Sprintf("d4:infod5:filesld6:lengthi1234e4:pathl%d:%seed6:lengthi0e4:pathl8:note.nfoeee4:name%d:%s12:piece lengthi16384e6:pieces20:00000000000000000000ee", len(video), video, len(rootName), rootName))
	proof, e := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if e != nil {
		t.Fatal("multi-file original metainfo", e)
	}
	round := f.start(t)
	candidate := tvExecutionCandidate(t, f, "native-send", "1080p", 9)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	tvExecutionPage(t, f, round, candidate)
	decision, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, tvExecutionLease(t, f, round))
	if e != nil || decision == nil || decision.State != "reserved" {
		t.Fatalf("real finalization %+v %v", decision, e)
	}
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "native-tv-sender", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	send := ports.AutomaticTVSendRequest{OperationID: decision.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "native-tv-sender", Token: job.LeaseToken}}
	repo := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)))
	work, e := repo.LoadAutomaticTVSendWork(t.Context(), send)
	if e != nil {
		t.Fatal(e)
	}
	base := &automaticTVSenderHarness{fixture: f, request: send, repository: repo, raw: raw, hash: hash, save: work.SavePath}
	h := &automaticTVNativeObservationHarness{automaticTVSenderHarness: base, kind: kind, video: "Trusted.Native.Series.2024/" + video, observer: postgres.NewAutomaticTVObservationStore(f.pool, idgen.New())}
	base.protected = base.protection(t)
	base.source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base.fetches.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/download/native-send" {
			t.Error("wrong original source request")
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(base.source.Close)
	base.client = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serve(t, w, r) }))
	t.Cleanup(base.client.Close)
	return h
}

func (h *automaticTVNativeObservationHarness) manager(t *testing.T) *download.Manager {
	t.Helper()
	target, e := url.Parse(h.client.URL)
	if e != nil {
		t.Fatal(e)
	}
	source, e := url.Parse(h.source.URL)
	if e != nil {
		t.Fatal(e)
	}
	m := download.NewManager(h.fixture.pool, idgen.New(), bytes.Repeat([]byte{27}, 32))
	m.SetClientFactory(func(kind, address, user, password string) (ports.DownloadClient, error) {
		if kind != h.kind || address != "https://downloader.test" || user != "fixture" || password != "fixture-api-key" {
			t.Error("factory lost actual bound configuration")
			return nil, ports.ErrBadResponse
		}
		httpClient := &http.Client{Transport: automaticTVSenderTransport{target: target, ignoreCancellation: h.ignoreCancellation}, Timeout: 10 * time.Second}
		if kind == "transmission" {
			return transmission.NewClient(address, user, password, httpClient)
		}
		return qbittorrent.NewClient(address, user, password, httpClient)
	})
	m.SetAutomaticTVServices(h.repository, torznab.NewAutomaticTVTorrentFetcher(&http.Client{Transport: automaticTVNativeTransport{target: source}, Timeout: 5 * time.Second}), torrent.Inspector{})
	return m
}

func (h *automaticTVNativeObservationHarness) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if h.kind == "transmission" {
		var rpc struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&rpc) != nil {
			t.Error("bad native RPC")
			w.WriteHeader(400)
			return
		}
		if rpc.Method == "torrent-add" {
			var metainfo, save string
			var labels []string
			_ = json.Unmarshal(rpc.Arguments["metainfo"], &metainfo)
			_ = json.Unmarshal(rpc.Arguments["download-dir"], &save)
			_ = json.Unmarshal(rpc.Arguments["labels"], &labels)
			raw, e := base64.StdEncoding.DecodeString(metainfo)
			if e != nil || !bytes.Equal(raw, h.raw) || save != h.save || strings.Join(labels, ",") != "pulsemedia,pulsemedia:op:"+h.automaticTVSenderHarness.request.OperationID.String() {
				t.Error("original Add binding changed")
			}
			h.adds.Add(1)
			if h.addHook != nil {
				h.addHook()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrent-added": map[string]any{"hashString": h.hash, "name": "Trusted.Native.Series.2024"}}})
			return
		}
		if rpc.Method != "torrent-get" {
			t.Error("observation used mutation/export")
			w.WriteHeader(400)
			return
		}
		_, fileRead := rpc.Arguments["ids"]
		h.reads.Add(1)
		if fileRead {
			h.files.Add(1)
		} else {
			h.lists.Add(1)
		}
		if h.readHook != nil {
			h.readHook()
		}
		task := map[string]any{"hashString": h.hash, "downloadDir": h.save, "labels": []string{"pulsemedia", "pulsemedia:op:" + h.request.OperationID.String()}, "status": 6, "totalSize": 1234, "haveValid": 1234, "error": 0}
		if h.mode == "incomplete" {
			task["status"] = 4
			task["haveValid"] = 600
		}
		if h.mode == "wrong_path" {
			task["downloadDir"] = h.save + "/other"
		}
		if h.mode == "wrong_hash" {
			task["hashString"] = strings.Repeat("f", 40)
		}
		if h.mode == "wrong_label" {
			task["labels"] = []string{"other"}
		}
		if h.mode == "changing" && h.lists.Load() > 1 {
			task["downloadDir"] = h.save + "/other"
		}
		if h.mode == "bytes" {
			task["haveValid"] = 1233
		}
		if fileRead {
			task["files"] = []any{map[string]any{"name": h.video, "length": 1234, "bytesCompleted": 1234}, map[string]any{"name": "Trusted.Native.Series.2024/note.nfo", "length": 0, "bytesCompleted": 0}}
			task["fileStats"] = []any{map[string]any{"wanted": true, "bytesCompleted": 1234}, map[string]any{"wanted": false, "bytesCompleted": 0}}
		}
		tasks := []any{task}
		if h.mode == "absent" {
			tasks = []any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": tasks}})
		return
	}
	switch r.URL.Path {
	case "/api/v2/auth/login":
		h.logins.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "synthetic-session", Path: "/"})
		_, _ = io.WriteString(w, "Ok.")
	case "/api/v2/torrents/add":
		if e := r.ParseMultipartForm(1 << 20); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		file, _, e := r.FormFile("torrents")
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		raw, e := io.ReadAll(file)
		_ = file.Close()
		if e != nil || !bytes.Equal(raw, h.raw) || r.FormValue("savepath") != h.save || r.FormValue("tags") != "pulsemedia,pulsemedia:op:"+h.automaticTVSenderHarness.request.OperationID.String() {
			t.Error("original Add binding changed")
		}
		h.adds.Add(1)
		if h.addHook != nil {
			h.addHook()
		}
		_, _ = fmt.Fprintf(w, `{"added_torrent_ids":["%s"],"failure_count":0,"pending_count":0,"success_count":1}`, h.hash)
	case "/api/v2/torrents/info":
		h.reads.Add(1)
		if r.URL.Query().Get("tag") != "" {
			h.lists.Add(1)
		}
		if h.readHook != nil {
			h.readHook()
		}
		task := map[string]any{"hash": h.hash, "save_path": h.save, "content_path": h.save + "/Trusted.Native.Series.2024", "name": "Trusted.Native.Series.2024", "tags": "pulsemedia,pulsemedia:op:" + h.request.OperationID.String(), "state": "uploading", "total_size": 1234, "completed": 1234, "progress": 1}
		if h.mode == "incomplete" {
			task["state"] = "downloading"
			task["completed"] = 600
			task["progress"] = 0.4
		}
		if h.mode == "wrong_path" {
			task["save_path"] = h.save + "/other"
			task["content_path"] = h.save + "/other/Trusted.Native.Series.2024"
		}
		if h.mode == "wrong_hash" {
			task["hash"] = strings.Repeat("f", 40)
		}
		if h.mode == "wrong_label" {
			task["tags"] = "other"
		}
		if h.mode == "changing" && h.lists.Load() > 1 {
			task["content_path"] = h.save + "/other"
		}
		if h.mode == "bytes" {
			task["completed"] = 1233
		}
		if h.mode == "nil" {
			_, _ = io.WriteString(w, "null")
			return
		}
		tasks := []any{task}
		if h.mode == "absent" {
			tasks = []any{}
		}
		if h.mode == "ambiguous" {
			tasks = append(tasks, task)
		}
		_ = json.NewEncoder(w).Encode(tasks)
	case "/api/v2/torrents/files":
		h.reads.Add(1)
		h.files.Add(1)
		if h.mode == "empty_files" {
			_, _ = io.WriteString(w, "[]")
			return
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"index": 0, "name": h.video, "size": 1234, "priority": 1, "progress": 1}, map[string]any{"index": 1, "name": "Trusted.Native.Series.2024/note.nfo", "size": 0, "priority": 0, "progress": 1}})
	default:
		t.Error("unexpected native request", r.URL.Path)
		w.WriteHeader(400)
	}
}

func (h *automaticTVNativeObservationHarness) send(t *testing.T) {
	t.Helper()
	out, e := h.manager(t).SendAutomaticTV(t.Context(), h.automaticTVSenderHarness.request)
	if e != nil || out == nil || out.State != "tracking" {
		t.Fatalf("physical first send %+v %v", out, e)
	}
	h.automaticTVSenderHarness.check(t, "tracking", 1)
}
func (h *automaticTVNativeObservationHarness) claim(t *testing.T) {
	t.Helper()
	n, e := h.observer.ScheduleAutomaticTVObservations(t.Context(), 100)
	if e != nil || n != 1 {
		t.Fatalf("fresh schedule %d %v", n, e)
	}
	j, e := db.New(h.fixture.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.AutomaticTVObservationJobKind}, Column2: "native-tv-observer", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	task, e := db.New(h.fixture.pool).GetDownloadTaskByOpID(t.Context(), h.automaticTVSenderHarness.request.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	h.request = ports.AutomaticTVObservationRequest{OperationID: task.OperationID, TaskID: task.ID, Lease: ports.JobLease{ID: j.ID, Owner: "native-tv-observer", Token: j.LeaseToken}}
}
func (h *automaticTVNativeObservationHarness) check(t *testing.T, results, organizations int) {
	t.Helper()
	if h.adds.Load() != 1 || h.protection(t) != h.protected {
		t.Fatal("observation replayed Add or changed protection")
	}
	for _, c := range []struct {
		query string
		want  int
	}{{`SELECT count(*) FROM automatic_tv_observation_result`, results}, {`SELECT count(*) FROM job WHERE kind='download_organization'`, organizations}, {`SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NOT NULL`, 1}, {`SELECT count(*) FROM automatic_tv_observation_permit WHERE finished_at IS NULL`, 0}, {`SELECT count(*) FROM unit_fact`, 0}} {
		if n := intentCount(t, h.fixture.pool, c.query); n != c.want {
			t.Fatalf("%s = %d want %d", c.query, n, c.want)
		}
	}
}

func TestAutomaticTVNativeObservationCompletePG(t *testing.T) {
	for _, scenario := range []string{"qbittorrent", "transmission", "expired_first_send", "paused"} {
		t.Run(scenario, func(t *testing.T) {
			kind := scenario
			if kind != "transmission" {
				kind = "qbittorrent"
			}
			h := newAutomaticTVNativeObservationHarness(t, kind)
			h.send(t)
			if scenario == "expired_first_send" {
				execIntent(t, h.fixture.pool, `CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.clock_timestamp()+interval '6 minutes'`)
				execIntent(t, h.fixture.pool, `CREATE FUNCTION public.statement_timestamp() RETURNS timestamptz LANGUAGE SQL VOLATILE RETURN pg_catalog.statement_timestamp()+interval '6 minutes'`)
				cfg := h.fixture.pool.Config()
				cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
				p, e := pgxpool.NewWithConfig(t.Context(), cfg)
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(p.Close)
				h.fixture.pool = p
				h.observer = postgres.NewAutomaticTVObservationStore(p, idgen.New())
				if n := intentCount(t, p, `SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1 AND (snapshot->>'send_valid_until')::timestamptz<clock_timestamp()`, h.automaticTVSenderHarness.request.OperationID); n != 1 {
					t.Fatal("original five-minute first-send window did not expire")
				}
			}
			if scenario == "paused" {
				execIntent(t, h.fixture.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, h.fixture.sub)
			}
			h.claim(t)
			out, e := h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, h.request)
			if e != nil || out == nil || !out.Complete || out.State != "completed" || out.ReviewRequired != (scenario == "paused") {
				t.Fatalf("complete %+v %v", out, e)
			}
			organizations := 1
			if scenario == "paused" {
				organizations = 0
			}
			h.check(t, 1, organizations)
			if h.lists.Load() != 2 || h.files.Load() != 1 {
				t.Fatal("missing outer A/files/B", h.lists.Load(), h.files.Load())
			}
			if n := intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_observation_result WHERE jsonb_array_length(inventory)=2 AND inventory->1->>'CompletedBytes'='0' AND inventory->1->>'Wanted'='false'`); n != 1 {
				t.Fatal("lost unwanted zero-byte sidecar", n)
			}
			if n := intentCount(t, h.fixture.pool, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded'`, h.request.Lease.ID); n != 1 {
				t.Fatal("fresh observation job not done")
			}
			if _, e = h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, h.request); e == nil {
				t.Fatal("completed job replayed")
			}
			h.check(t, 1, organizations)
		})
	}
}
func TestAutomaticTVNativeObservationTrackingPG(t *testing.T) {
	for _, kind := range []string{"qbittorrent", "transmission"} {
		t.Run(kind, func(t *testing.T) {
			h := newAutomaticTVNativeObservationHarness(t, kind)
			h.send(t)
			h.claim(t)
			h.mode = "incomplete"
			out, e := h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, h.request)
			if e != nil || out == nil || out.State != "tracking" || out.Complete || out.ReviewRequired {
				t.Fatalf("tracking %+v %v", out, e)
			}
			if h.files.Load() != 0 || h.lists.Load() != 2 {
				t.Fatal("incomplete task read files or skipped B")
			}
			h.check(t, 1, 0)
		})
	}
}
func TestAutomaticTVNativeObservationContradictionsPG(t *testing.T) {
	for _, mode := range []string{"absent", "ambiguous", "wrong_label", "wrong_hash", "wrong_path", "bytes", "changing", "nil", "empty_files"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutomaticTVNativeObservationHarness(t, "qbittorrent")
			h.send(t)
			h.claim(t)
			h.mode = mode
			out, e := h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, h.request)
			results := 0
			if e == nil {
				if out == nil || out.Complete {
					t.Fatalf("unsafe completion %+v", out)
				}
				if mode == "bytes" {
					if out.State != "tracking" || out.ReviewRequired || h.files.Load() != 0 || h.lists.Load() != 2 {
						t.Fatalf("one missing byte must retain tracking without inventory: %+v", out)
					}
				} else if !out.ReviewRequired || out.State != "unknown" {
					t.Fatalf("unsafe classification %+v", out)
				}
				results = 1
			} else if !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("unsanitized error", e)
			}
			if (mode == "absent" || mode == "wrong_hash" || mode == "wrong_path") && (h.files.Load() != 0 || h.lists.Load() != 2) {
				t.Fatal("contradictory A did not skip files and retain B")
			}
			h.check(t, results, 0)
		})
	}
}

type automaticTVNativeObservationRepository struct {
	ports.AutomaticTVObservationRepository
	afterLoad func(*ports.AutomaticTVObservationSnapshot)
}

func (r automaticTVNativeObservationRepository) LoadAutomaticTVObservation(ctx context.Context, request ports.AutomaticTVObservationRequest) (*ports.AutomaticTVObservationSnapshot, error) {
	s, e := r.AutomaticTVObservationRepository.LoadAutomaticTVObservation(ctx, request)
	if e == nil && r.afterLoad != nil {
		r.afterLoad(s)
	}
	return s, e
}

// Embedding just the generic interface deliberately hides the native marker.
type automaticTVNativeObservationUnsupported struct{ ports.DownloadClient }

func TestAutomaticTVNativeObservationAuthorityPG(t *testing.T) {
	for _, scenario := range []string{"unsupported", "old_token", "client_stamp", "snapshot_task", "unit_version", "late_token"} {
		t.Run(scenario, func(t *testing.T) {
			h := newAutomaticTVNativeObservationHarness(t, "qbittorrent")
			h.send(t)
			h.claim(t)
			m := h.manager(t)
			var repo ports.AutomaticTVObservationRepository = h.observer
			switch scenario {
			case "unsupported":
				m.SetClientFactory(func(_, address, user, password string) (ports.DownloadClient, error) {
					c, e := qbittorrent.NewClient(address, user, password, h.client.Client())
					return automaticTVNativeObservationUnsupported{c}, e
				})
			case "old_token":
				h.request.Lease.Token++
			case "client_stamp":
				repo = automaticTVNativeObservationRepository{h.observer, func(_ *ports.AutomaticTVObservationSnapshot) {
					execIntent(t, h.fixture.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, h.fixture.client)
				}}
			case "snapshot_task":
				repo = automaticTVNativeObservationRepository{h.observer, func(s *ports.AutomaticTVObservationSnapshot) { s.Request.TaskID = uuid.New() }}
			case "late_token":
				var once sync.Once
				h.readHook = func() {
					once.Do(func() {
						execIntent(t, h.fixture.pool, `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`, h.request.Lease.ID)
					})
				}
			case "unit_version":
				var once sync.Once
				h.readHook = func() {
					once.Do(func() {
						execIntent(t, h.fixture.pool, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, h.fixture.unit)
					})
				}
			}
			out, e := m.ObserveAutomaticTV(t.Context(), repo, h.request)
			if scenario == "unit_version" {
				if e != nil || out == nil || !out.Complete || !out.ReviewRequired {
					t.Fatalf("physical fact/current eligibility %+v %v", out, e)
				}
				h.check(t, 1, 0)
				return
			}
			if e == nil || out != nil {
				t.Fatalf("stale authority accepted %+v %v", out, e)
			}
			if scenario == "late_token" {
				if !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
					t.Fatal("stale callback token", e)
				}
			} else if h.reads.Load() != 0 {
				t.Fatal("unavailable authority touched native network")
			}
			h.check(t, 0, 0)
		})
	}
}

func TestAutomaticTVNativeObservationActiveSenderPG(t *testing.T) {
	h := newAutomaticTVNativeObservationHarness(t, "qbittorrent")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.addHook = func() { close(entered); <-release }
	m := h.manager(t)
	go func() { _, e := m.SendAutomaticTV(t.Context(), h.automaticTVSenderHarness.request); done <- e }()
	joined := false
	defer func() {
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("sender did not return")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no Add entry")
	}
	var killed bool
	if e := h.fixture.pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(backend_pid) FROM automatic_tv_physical_attempt WHERE operation_id=$1 AND finished_at IS NULL`, h.automaticTVSenderHarness.request.OperationID).Scan(&killed); e != nil || !killed {
		t.Fatal("kill sender session", e)
	}
	if n, e := h.observer.ScheduleAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
		t.Fatal("active sender was scheduled", n, e)
	}
	task, e := db.New(h.fixture.pool).GetDownloadTaskByOpID(t.Context(), h.automaticTVSenderHarness.request.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	request := ports.AutomaticTVObservationRequest{OperationID: task.OperationID, TaskID: task.ID, Lease: h.automaticTVSenderHarness.request.Lease}
	if out, e := h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, request); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) || out != nil {
		t.Fatal("dead PID released sender", out, e)
	}
	if h.reads.Load() != 0 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_observation_permit`) != 0 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NULL`) != 1 {
		t.Fatal("observer ended sender or entered native I/O")
	}
	if _, e := h.manager(t).SendAutomaticTV(t.Context(), h.automaticTVSenderHarness.request); !errors.Is(e, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatal("second Add", e)
	}
	unblock()
	select {
	case <-done:
		joined = true
	case <-time.After(15 * time.Second):
		t.Fatal("sender stuck")
	}
	if h.adds.Load() != 1 {
		t.Fatal("replayed Add")
	}
}

func TestAutomaticTVNativeObservationPhysicalHoldPG(t *testing.T) {
	for _, scenario := range []string{"cancel", "deadline", "lost_session", "expired_lease"} {
		t.Run(scenario, func(t *testing.T) {
			h := newAutomaticTVNativeObservationHarness(t, "qbittorrent")
			h.send(t)
			h.claim(t)
			h.ignoreCancellation = true
			ctx, cancel := context.WithCancel(t.Context())
			if scenario == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var blockOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			h.readHook = func() { blockOnce.Do(func() { close(entered); <-release }) }
			joined := false
			defer func() {
				unblock()
				if !joined {
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						t.Error("observation did not await physical return")
					}
				}
			}()
			m := h.manager(t)
			go func() {
				out, e := m.ObserveAutomaticTV(ctx, h.observer, h.request)
				if out != nil {
					done <- fmt.Errorf("late receipt")
					return
				}
				done <- e
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native List did not start")
			}
			if scenario == "lost_session" {
				var killed bool
				if e := h.fixture.pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(backend_pid) FROM automatic_tv_observation_permit WHERE operation_id=$1 AND finished_at IS NULL`, h.request.OperationID).Scan(&killed); e != nil || !killed {
					t.Fatal("terminate observation backend", e)
				}
			}
			switch scenario {
			case "expired_lease":
				execIntent(t, h.fixture.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, h.request.Lease.ID)
			case "deadline":
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("deadline did not expire")
				}
			case "cancel":
				cancel()
			}
			select {
			case e := <-done:
				joined = true
				t.Fatal("returned before actual I/O", e)
			default:
			}
			before := h.reads.Load()
			if out, e := h.manager(t).ObserveAutomaticTV(t.Context(), h.observer, h.request); e == nil || out != nil {
				t.Fatal("second Manager entered", out, e)
			}
			if h.reads.Load() != before || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_observation_permit WHERE finished_at IS NULL`) != 1 {
				t.Fatal("lost persistent physical hold")
			}
			if n, e := h.observer.RecoverAutomaticTVObservations(t.Context(), 100); e != nil || n != 0 {
				t.Fatal("recovered outstanding physical I/O", n, e)
			}
			unblock()
			select {
			case e := <-done:
				joined = true
				if scenario == "expired_lease" {
					if !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
						t.Fatal("expired callback", e)
					}
				} else {
					want := context.Canceled
					if scenario == "deadline" {
						want = context.DeadlineExceeded
					}
					if scenario == "lost_session" {
						want = ports.ErrUnavailable
					}
					if !errors.Is(e, want) {
						t.Fatal("late physical return", e)
					}
				}

			case <-time.After(15 * time.Second):
				t.Fatal("physical return not joined")
			}
			h.check(t, 0, 0)
		})
	}
}

func TestAutomaticTVNativeObservationRequestValidation(t *testing.T) {
	valid := ports.AutomaticTVObservationRequest{TaskID: uuid.New(), OperationID: uuid.New(), Lease: ports.JobLease{ID: uuid.New(), Owner: "owner", Token: 1}}
	for _, mutate := range []func(*ports.AutomaticTVObservationRequest){func(r *ports.AutomaticTVObservationRequest) { r.TaskID = uuid.Nil }, func(r *ports.AutomaticTVObservationRequest) { r.OperationID = uuid.Nil }, func(r *ports.AutomaticTVObservationRequest) { r.Lease.ID = uuid.Nil }, func(r *ports.AutomaticTVObservationRequest) { r.Lease.Owner = "" }, func(r *ports.AutomaticTVObservationRequest) { r.Lease.Token = 0 }, func(r *ports.AutomaticTVObservationRequest) { r.Lease.Token = -1 }, func(r *ports.AutomaticTVObservationRequest) { r.PhysicalPermitID = uuid.New() }} {
		r := valid
		mutate(&r)
		var m *download.Manager
		if out, e := m.ObserveAutomaticTV(t.Context(), nil, r); out != nil || !errors.Is(e, ports.ErrAutomaticTVObservationStale) {
			t.Fatal("invalid request accepted", out, e)
		}
	}
}
