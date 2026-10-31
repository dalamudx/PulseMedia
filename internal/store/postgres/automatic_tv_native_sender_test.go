package postgres_test

import (
	"bytes"
	"context"
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
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

type automaticTVSenderTransport struct {
	target             *url.URL
	ignoreCancellation bool
}

func (r automaticTVSenderTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "downloader.test" {
		return nil, ports.ErrBadResponse
	}
	ctx := request.Context()
	if r.ignoreCancellation {
		ctx = context.WithoutCancel(ctx)
	}
	clone := request.Clone(ctx)
	address := *request.URL
	address.Scheme, address.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &address, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

type automaticTVSenderHarness struct {
	fixture                         automaticTVFixture
	request                         ports.AutomaticTVSendRequest
	repository                      *postgres.AutomaticTVSubmissionStore
	raw                             []byte
	hash, save, protected           string
	source, client                  *httptest.Server
	adds, logins, fetches           atomic.Int32
	ack                             string
	fetchHook                       func() error
	addHook                         func()
	sourceFails, ignoreCancellation bool
}

func newAutomaticTVSenderHarness(t *testing.T) *automaticTVSenderHarness {
	t.Helper()
	f := newAutomaticTVFixture(t)
	name := "Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.mkv"
	// Synthetic zero-content metainfo, kept only in memory; no content transfer.
	raw := []byte(fmt.Sprintf("d4:infod6:lengthi1234e4:name%d:%s12:piece lengthi16384e6:pieces20:00000000000000000000ee", len(name), name))
	proof, err := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if err != nil {
		t.Fatal("fixture metainfo", err)
	}
	round := f.start(t)
	if round.Snapshot.MappingMode != "standard" || round.Snapshot.MappingRevision != 0 {
		t.Fatal("implicit standard mapping changed")
	}
	candidate := tvExecutionCandidate(t, f, "native-send", "1080p", 9)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	tvExecutionPage(t, f, round, candidate)
	result, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, tvExecutionLease(t, f, round))
	if err != nil || result == nil || result.State != "reserved" {
		t.Fatalf("real finalization: %+v %v", result, err)
	}
	job, err := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "native-tv-sender", Column3: 60000})
	if err != nil {
		t.Fatal("original submission job", err)
	}
	request := ports.AutomaticTVSendRequest{OperationID: result.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "native-tv-sender", Token: job.LeaseToken}}
	repository := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)))
	work, err := repository.LoadAutomaticTVSendWork(t.Context(), request)
	if err != nil || work == nil {
		t.Fatal("authorized send work", err)
	}
	root, err := db.New(f.pool).GetLibraryRootByID(t.Context(), f.tv)
	if err != nil {
		t.Fatal("read configured target root", err)
	}
	if work.SourceEndpointURL != "https://tv-source.test/api?apikey=private-query-key" || work.Authority.Target.ClientID != f.client || work.Authority.Target.MappingRevision != 0 || work.RootPath != root.Path || work.SavePath != root.Path || work.ClientURL != "https://downloader.test" {
		t.Fatal("full source/client/root binding changed")
	}
	h := &automaticTVSenderHarness{fixture: f, request: request, repository: repository, raw: raw, hash: hash, save: work.SavePath, ack: "json"}
	h.protected = h.protection(t)
	h.source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fetches.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/download/native-send" {
			t.Error("original encrypted download URL changed")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if h.fetchHook != nil {
			if e := h.fetchHook(); e != nil {
				t.Error("fetch mutation", e)
				http.Error(w, "fixture error", http.StatusInternalServerError)
				return
			}
		}
		if h.sourceFails {
			http.Error(w, "private provider message", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(h.raw)
	}))
	t.Cleanup(h.source.Close)
	h.client = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			h.logins.Add(1)
			if e := r.ParseForm(); e != nil {
				t.Error(e)
			}
			if r.Form.Get("username") != "fixture" || r.Form.Get("password") != "fixture-api-key" {
				t.Error("bound credentials changed")
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "synthetic-session", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			h.adds.Add(1)
			if r.Method != http.MethodPost {
				t.Error("wrong Add method")
			}
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				t.Error(e)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			defer func() {
				if r.MultipartForm != nil {
					_ = r.MultipartForm.RemoveAll()
				}
			}()
			file, _, e := r.FormFile("torrents")
			if e != nil {
				t.Error("missing original bytes", e)
				http.Error(w, "no bytes", http.StatusBadRequest)
				return
			}
			got, e := io.ReadAll(file)
			_ = file.Close()
			if e != nil || !bytes.Equal(got, h.raw) || r.FormValue("urls") != "" || r.FormValue("savepath") != h.save || r.FormValue("tags") != "pulsemedia,pulsemedia:op:"+h.request.OperationID.String() {
				t.Error("Add did not preserve original bytes/path/tags")
			}
			if h.addHook != nil {
				h.addHook()
			}
			switch h.ack {
			case "json":
				_, _ = fmt.Fprintf(w, `{"added_torrent_ids":["%s"],"failure_count":0,"pending_count":0,"success_count":1}`, h.hash)
			case "legacy":
				_, _ = io.WriteString(w, "Ok.")
			case "lost":
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("HTTP fixture cannot lose acknowledgement")
					return
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			default:
				_, _ = io.WriteString(w, `{"success_count":1,"private":"must-not-persist"}`)
			}
		default:
			t.Error("unexpected native client request")
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(h.client.Close)
	return h
}

func (h *automaticTVSenderHarness) manager(t *testing.T) *download.Manager {
	t.Helper()
	source, err := url.Parse(h.source.URL)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(h.client.URL)
	if err != nil {
		t.Fatal(err)
	}
	m := download.NewManager(h.fixture.pool, idgen.New(), bytes.Repeat([]byte{27}, 32))
	m.SetClientFactory(func(kind, address, user, password string) (ports.DownloadClient, error) {
		if kind != "qbittorrent" || address != "https://downloader.test" || user != "fixture" || password != "fixture-api-key" {
			t.Error("fresh factory did not receive exact current bound config")
			return nil, ports.ErrBadResponse
		}
		return qbittorrent.NewClient(address, user, password, &http.Client{Transport: automaticTVSenderTransport{target: target, ignoreCancellation: h.ignoreCancellation}, Timeout: 10 * time.Second})
	})
	m.SetAutomaticTVServices(h.repository, torznab.NewAutomaticTVTorrentFetcher(&http.Client{Transport: automaticTVNativeTransport{target: source}, Timeout: 5 * time.Second}), torrent.Inspector{})
	return m
}

func (h *automaticTVSenderHarness) protection(t *testing.T) string {
	t.Helper()
	var out string
	err := h.fixture.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(jsonb_build_array(id,operation_id,unit_id,candidate_id,source_root_id,source_generation,source_stamp) ORDER BY id) FROM acquisition_reservation WHERE operation_id=$1),
 (SELECT jsonb_agg(jsonb_build_array(id,operation_id,unit_id,root_id,candidate_id,purpose) ORDER BY id) FROM unit_reservation WHERE operation_id=$1),
 (SELECT jsonb_agg(jsonb_build_array(id,operation_id,unit_id,root_id,submission_fence) ORDER BY id) FROM download_requirement WHERE operation_id=$1))::text`, h.request.OperationID).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *automaticTVSenderHarness) check(t *testing.T, state string, adds int32) {
	t.Helper()
	if h.adds.Load() != adds || h.protection(t) != h.protected {
		t.Fatal("original protections or physical Add count changed")
	}
	tvExecutionCounts(t, h.fixture, 1)
	if intentCount(t, h.fixture.pool, `SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL`) != 1 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM download_requirement WHERE state='pending'`) != 1 {
		t.Fatal("physical protection released")
	}
	if state == "tracking" || state == "unknown" {
		if intentCount(t, h.fixture.pool, `SELECT count(*) FROM audit_log WHERE action IN ('automatic_tv.submission_prepared','automatic_tv.submission_observed')`) != 2 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM outbox_event WHERE event_type IN ('automatic_tv.submission_prepared','automatic_tv.submission_observed')`) != 2 {
			t.Fatal("prepare/result audit and event not committed together")
		}
		var valid bool
		expected := state
		if state == "unknown" {
			expected = "submission_unknown"
		}
		if err := h.fixture.pool.QueryRow(t.Context(), `SELECT state='succeeded' AND result->>'state'=$2 FROM job WHERE id=$1`, h.request.Lease.ID, expected).Scan(&valid); err != nil || !valid {
			t.Fatal("original job/result receipt missing", err)
		}
		if intentCount(t, h.fixture.pool, `SELECT count(*) FROM audit_log WHERE payload::text LIKE '%must-not-persist%'`) != 0 {
			t.Fatal("raw native error leaked into audit")
		}
	}
	if state == "" {
		if intentCount(t, h.fixture.pool, `SELECT count(*) FROM download_task`) != 0 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_submission_preparation`) != 0 {
			t.Fatal("failed fetch/prepare created task or preparation")
		}
		return
	}
	task, err := db.New(h.fixture.pool).GetDownloadTaskByOpID(t.Context(), h.request.OperationID)
	if err != nil || task.State != state || task.SavePath != h.save || task.InfoHash != h.hash || task.ClientID != h.fixture.client {
		t.Fatalf("persistent task state=%s want %s: %v", task.State, state, err)
	}
}

func (h *automaticTVSenderHarness) forbidReplay(t *testing.T, m *download.Manager) {
	t.Helper()
	before := h.adds.Load()
	if _, err := m.SendAutomaticTV(t.Context(), h.request); !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) && !errors.Is(err, ports.ErrAutomaticTVResultStale) {
		t.Fatalf("repeat did not require observation: %v", err)
	}
	execIntent(t, h.fixture.pool, `UPDATE job SET lease_owner='replacement-sender',lease_token=lease_token+1,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, h.request.Lease.ID)
	next := h.request
	next.Lease.Owner = "replacement-sender"
	next.Lease.Token++
	if _, err := h.manager(t).SendAutomaticTV(t.Context(), next); !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) && !errors.Is(err, ports.ErrAutomaticTVResultStale) {
		t.Fatalf("replacement job did not require observation: %v", err)
	}
	if h.adds.Load() != before {
		t.Fatal("original or replacement lease replayed Add")
	}
}

func TestAutomaticTVNativeSenderAcknowledgedPG(t *testing.T) {
	for _, ack := range []string{"json", "legacy"} {
		t.Run(ack, func(t *testing.T) {
			h := newAutomaticTVSenderHarness(t)
			h.ack = ack
			m := h.manager(t)
			out, err := m.SendAutomaticTV(t.Context(), h.request)
			if err != nil || out == nil || out.State != "tracking" {
				t.Fatalf("first send: %+v %v", out, err)
			}
			h.check(t, "tracking", 1)
			h.genericIsolation(t, m)
			if h.fetches.Load() != 1 || h.logins.Load() != 1 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_submission_result`) != 1 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NOT NULL`) != 1 {
				t.Fatal("native fetch/login/result/physical completion missing")
			}
			h.forbidReplay(t, m)
		})
	}
}

func TestAutomaticTVNativeSenderUnknownNeverReplaysPG(t *testing.T) {
	for _, ack := range []string{"invalid", "lost"} {
		t.Run(ack, func(t *testing.T) {
			h := newAutomaticTVSenderHarness(t)
			h.ack = ack
			m := h.manager(t)
			out, err := m.SendAutomaticTV(t.Context(), h.request)
			if !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) || out == nil || out.State != "submission_unknown" {
				t.Fatalf("uncertain Add: %+v %v", out, err)
			}
			h.check(t, "unknown", 1)
			if intentCount(t, h.fixture.pool, `SELECT count(*) FROM unit_reservation WHERE state='dispatched'`) != 1 {
				t.Fatal("unknown Add released its protection")
			}
			h.forbidReplay(t, m)
		})
	}
}

func TestAutomaticTVNativeSenderBeforePreparePG(t *testing.T) {
	for _, kind := range []string{"fetch-failed", "unauthorized"} {
		t.Run(kind, func(t *testing.T) {
			h := newAutomaticTVSenderHarness(t)
			m := h.manager(t)
			if kind == "fetch-failed" {
				h.sourceFails = true
			} else {
				execIntent(t, h.fixture.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, h.fixture.sub)
			}
			if _, err := m.SendAutomaticTV(t.Context(), h.request); err == nil {
				t.Fatal("invalid first send succeeded")
			}
			h.check(t, "", 0)
			if kind == "unauthorized" && (h.fetches.Load() != 0 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_physical_attempt`) != 0) {
				t.Fatal("unauthorized work changed physical history or touched network")
			}
		})
	}
}

func TestAutomaticTVNativeSenderCurrentFactsFencePG(t *testing.T) {
	for _, kind := range []string{"client", "source", "root"} {
		t.Run(kind, func(t *testing.T) {
			h := newAutomaticTVSenderHarness(t)
			h.fetchHook = func() error {
				switch kind {
				case "client":
					_, err := h.fixture.pool.Exec(t.Context(), `UPDATE download_client SET url='https://changed-downloader.test' WHERE id=$1`, h.fixture.client)
					return err
				case "source":
					_, err := h.fixture.pool.Exec(t.Context(), `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, h.fixture.indexer)
					return err
				default:
					_, err := h.fixture.pool.Exec(t.Context(), `UPDATE library_root SET path=path||'/replacement' WHERE id=$1`, h.fixture.tv)
					return err
				}
			}
			if _, err := h.manager(t).SendAutomaticTV(t.Context(), h.request); err == nil {
				t.Fatal("changed current facts authorized Add")
			}
			h.check(t, "", 0)
			if h.fetches.Load() != 1 {
				t.Fatal("test did not change facts between actual fetch and prepare")
			}
		})
	}
}

func TestAutomaticTVNativeSenderWaitsForPhysicalReturnPG(t *testing.T) {
	for _, kind := range []string{"cancel-only", "cancel-and-revoke", "kill-and-revoke", "kill-only"} {
		t.Run(kind, func(t *testing.T) {
			h := newAutomaticTVSenderHarness(t)
			h.ignoreCancellation = true
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			h.addHook = func() { close(entered); <-release }
			m := h.manager(t)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := m.SendAutomaticTV(ctx, h.request); done <- err }()
			joined := false
			t.Cleanup(func() {
				cancel()
				unblock()
				if !joined {
					select {
					case <-done:
					case <-time.After(12 * time.Second):
						t.Error("sender did not join actual HTTP return")
					}
				}
			})
			select {
			case <-entered:
			case <-time.After(8 * time.Second):
				t.Fatal("native Add not entered")
			}
			if strings.HasPrefix(kind, "kill") {
				var killed bool
				if err := h.fixture.pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(backend_pid) FROM automatic_tv_physical_attempt WHERE operation_id=$1 AND finished_at IS NULL`, h.request.OperationID).Scan(&killed); err != nil || !killed {
					t.Fatal("dedicated session not terminated", err)
				}
			}
			if kind == "cancel-only" {
				cancel()
			}
			if strings.HasSuffix(kind, "revoke") {
				cancel()
				execIntent(t, h.fixture.pool, `UPDATE job SET lease_token=lease_token+1,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, h.request.Lease.ID)
			}
			select {
			case err := <-done:
				joined = true
				t.Fatalf("sender returned before actual Add: %v", err)
			case <-time.After(120 * time.Millisecond):
			}
			if _, err := h.manager(t).SendAutomaticTV(t.Context(), h.request); err == nil {
				t.Fatal("second manager passed active physical attempt")
			}
			if h.adds.Load() != 1 || intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NULL`) != 1 {
				t.Fatal("lost session or cancellation released live physical protection")
			}
			unblock()
			select {
			case err := <-done:
				joined = true
				if kind == "kill-only" {
					if err != nil {
						t.Fatal("physical observation rejected only because session died", err)
					}
				} else if kind == "cancel-only" {
					if !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) {
						t.Fatalf("cancelled physical Add must remain unknown: %v", err)
					}
				} else if !errors.Is(err, ports.ErrAutomaticTVResultStale) {
					t.Fatalf("expired original lease result=%v", err)
				}
			case <-time.After(12 * time.Second):
				t.Fatal("sender failed bounded result write after actual return")
			}
			state := "submitting"
			if kind == "cancel-only" {
				state = "unknown"
			}
			if kind == "kill-only" {
				state = "tracking"
			}
			h.check(t, state, 1)
			if intentCount(t, h.fixture.pool, `SELECT count(*) FROM automatic_tv_physical_attempt WHERE finished_at IS NULL`) != 0 {
				t.Fatal("physical completion nonce not marked after return")
			}
			if kind == "kill-only" || kind == "cancel-only" {
				h.forbidReplay(t, m)
			}
		})
	}
}

func (h *automaticTVSenderHarness) genericIsolation(t *testing.T, m *download.Manager) {
	t.Helper()
	ledger := func() string {
		var value string
		if err := h.fixture.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM download_task t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM automatic_tv_submission_preparation t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM automatic_tv_submission_result t),
 (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM automatic_tv_physical_attempt t))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return tvExecutionState(t, h.fixture) + value
	}
	before := ledger()
	adds, fetches, logins := h.adds.Load(), h.fetches.Load(), h.logins.Load()
	task, err := db.New(h.fixture.pool).GetDownloadTaskByOpID(t.Context(), h.request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.PrepareDownload(t.Context(), h.request.OperationID, h.fixture.client, task.CandidateID, h.save, h.request.Lease); !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatalf("generic SubmitDownload escaped isolation: %v", err)
	}
	if _, err = m.ReconcileTask(t.Context(), h.request.OperationID); !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatalf("generic ReconcileTask escaped isolation: %v", err)
	}
	if _, err = m.ObserveTask(t.Context(), h.request.OperationID); !errors.Is(err, ports.ErrAutomaticTVNeedsObservation) {
		t.Fatalf("generic ObserveTask escaped isolation: %v", err)
	}
	if h.adds.Load() != adds || h.fetches.Load() != fetches || h.logins.Load() != logins || ledger() != before {
		t.Fatal("generic entry changed native network count or automatic ledger")
	}
}
