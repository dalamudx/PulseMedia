package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The downstream endpoints are the owned real services. HTTP intercepts only
// create the failure boundary; they never synthesize a downloader success.
func TestM4RealDownloadR3Faults(t *testing.T) {
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	if root == "" {
		t.Skip("explicit owned Podman M4 services required")
	}
	clean, err := filepath.EvalSymlinks(root)
	if err != nil || clean != root || filepath.Dir(root) != "/data/temp" || !strings.HasPrefix(filepath.Base(root), "pulse-m4-services.") {
		t.Fatal("canonical owned M4 service root required")
	}
	for _, kind := range []string{"qbittorrent", "transmission"} {
		label, err := exec.Command("podman", "inspect", "--format", "{{index .Config.Labels \"io.pulsemedia.state\"}}", "pulsemedia-m4-"+kind).Output()
		if err != nil || strings.TrimSpace(string(label)) != root {
			t.Fatal("refusing downloader outside owned service root")
		}
		for _, stage := range []string{"started-before-action", "action-before-reply", "validated-before-writeback", "result-before-commit", "deferred-commit-failure", "committed-before-restart", "expired-callback-blocked"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
				defer cancel()
				f := newM4R3Fixture(t, ctx, root, kind, stage)
				if stage == "expired-callback-blocked" {
					oldCtx, stop := context.WithCancel(ctx)
					defer stop()
					done := make(chan struct{})
					go func() { defer close(done); _, _ = handleDownloadJob(oldCtx, f.q, f.manager(), f.job, f.clientID) }()
					defer func() { f.gate.unblock(); <-done }()
					select {
					case <-f.gate.reached:
					case <-ctx.Done():
						t.Fatal("real Add did not reach withheld reply")
					}
					stop()
					select {
					case <-done:
						t.Fatal("cancellation released admitted unreturned I/O")
					case <-time.After(80 * time.Millisecond):
					}
					if _, err := f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", f.job.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := handleDownloadJob(ctx, f.q, f.manager(), f.job, f.clientID); err != nil {
						t.Fatal(err)
					}
					f.gate.unblock()
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("old physical callback did not drain")
					}
				} else {
					if _, err := handleDownloadJob(ctx, f.q, f.manager(), f.job, f.clientID); err != nil {
						t.Fatal(err)
					}
					task, err := f.q.GetDownloadTaskByOpID(ctx, f.operation)
					expected := "submitting"
					if stage == "started-before-action" || stage == "action-before-reply" {
						expected = "unknown"
					}
					if stage == "committed-before-restart" {
						expected = "tracking"
					}
					if err != nil || task.State != expected {
						t.Fatalf("persisted boundary state=%s expected=%s err=%v", task.State, expected, err)
					}
				}
				atomicFailure := stage == "validated-before-writeback" || stage == "result-before-commit" || stage == "deferred-commit-failure" || stage == "expired-callback-blocked"
				wantResults := 1
				if atomicFailure {
					wantResults = 0
				}
				f.assertResults(t, ctx, wantResults)
				f.dropFault(t, ctx)
				original, err := f.q.GetDownloadTaskByOpID(ctx, f.operation)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					// A reconstructed Manager has no memory of the original call or client.
					if _, err := handleDownloadJob(ctx, f.q, f.manager(), f.job, f.clientID); err != nil {
						t.Fatal(err)
					}
				}
				task, err := f.q.GetDownloadTaskByOpID(ctx, f.operation)
				expected := "tracking"
				accepted := int32(1)
				if stage == "started-before-action" {
					expected, accepted = "unknown", 0
				}
				if err != nil || task.State != expected || task.ID != original.ID || task.InfoHash != f.hash || task.SavePath != f.savePath {
					t.Fatal("restart changed original task identity/path or did not observe original result", err)
				}
				if f.gate.attempts.Load() != 1 || f.gate.accepted.Load() != accepted {
					t.Fatalf("unexpected Add: requests=%d downstream_accepted=%d", f.gate.attempts.Load(), f.gate.accepted.Load())
				}
				var protected int
				if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched')", f.operation).Scan(&protected); err != nil || protected != 1 {
					t.Fatal("uncertain or in-progress operation lost protection", err)
				}
				f.assertResults(t, ctx, wantResults)
				if accepted == 1 {
					observed, err := f.direct.GetTorrent(ctx, f.hash)
					if err != nil || observed.InfoHash != f.hash || observed.SavePath != f.savePath || !m4HasTag(observed.Tags, "pulsemedia:op:"+f.operation.String()) {
						t.Fatal("real operation identity/tag/path mismatch", err)
					}
				}
				t.Logf("M4_REAL_R3_PASSED client=%s boundary=%s original_task=true add_requests=1 downstream_accepted=%d restart_observed=true protection_retained=true submission_results=%d", kind, stage, accepted, wantResults)
			})
		}
	}
}

type m4R3Fixture struct {
	pool                  *pgxpool.Pool
	q                     *db.Queries
	operation, clientID   uuid.UUID
	job                   *db.Job
	hash, savePath, stage string
	direct                ports.DownloadClient
	manager               func() *download.Manager
	gate                  *m4R3Gate
}

func newM4R3Fixture(t *testing.T, ctx context.Context, root, kind, stage string) *m4R3Fixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q, gen := postgres.Queries(pool), idgen.New()
	probe, err := os.MkdirTemp(filepath.Join(root, "downloads"), "m4-r3.")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probe, 0755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(probe, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	name := "PulseMedia-R3-" + filepath.Base(probe) + ".mkv"
	payload := bytes.Repeat([]byte(name), 25)
	piece := sha1.Sum(payload)
	info := []byte(fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi65536e6:pieces20:", len(payload), len(name), name))
	info = append(info, piece[:]...)
	info = append(info, []byte("7:privatei1ee")...)
	digest := sha1.Sum(info)
	hash := hex.EncodeToString(digest[:])
	raw := append([]byte("d4:info"), info...)
	raw = append(raw, 'e')
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	t.Cleanup(source.Close)
	base := "http://127.0.0.1:18085"
	if kind == "transmission" {
		base = "http://127.0.0.1:19091"
	}
	direct, err := m4R3Client(kind, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := direct.GetTorrent(cleanup, hash)
		if err == nil {
			if err := direct.RemoveTorrent(cleanup, hash, false); err != nil {
				t.Error("owned R3 task cleanup failed", err)
				return
			}
			if !m4WaitTorrent(cleanup, func() bool { _, err := direct.GetTorrent(cleanup, hash); return errors.Is(err, ports.ErrNotFound) }) {
				t.Error("owned R3 task removal unconfirmed")
				return
			}
		} else if !errors.Is(err, ports.ErrNotFound) {
			t.Error("owned R3 task cleanup observation failed", err)
			return
		}
		if err := os.RemoveAll(probe); err != nil {
			t.Error(err)
		}
	})
	gate := &m4R3Gate{stage: stage, target: base, reached: make(chan struct{}), release: make(chan struct{}), pool: pool, t: t}
	t.Cleanup(gate.unblock)
	proxy := httptest.NewServer(gate)
	t.Cleanup(proxy.Close)
	gate.transport = &http.Transport{DisableKeepAlives: true}
	t.Cleanup(gate.transport.CloseIdleConnections)
	key := []byte("12345678901234567890123456789012")
	credential, err := crypto.Encrypt(key, []byte(""))
	if err != nil {
		t.Fatal(err)
	}
	indexerID, candidate, clientID, library, media, unit := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexerID, Name: "M4 owned R3 source", Url: source.URL, ApiKeyEncrypted: credential, Categories: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	link, err := crypto.Encrypt(key, []byte(source.URL+"/self-generated.torrent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: candidate, IndexerID: indexerID, SourceConfigVersion: 1, Guid: hash, Title: "Test.2020.1080p.Bluray", LinkEncrypted: link, InfoHash: pgtype.Text{String: hash, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: clientID, Name: "M4 real " + kind, ClientType: kind, Url: proxy.URL, PasswordEncrypted: credential, RemotePathMap: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	savePath := path.Join("/downloads", filepath.Base(probe))
	if _, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: library, Name: "M4 R3", MediaType: "movie", Path: savePath}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: media, Title: "Test", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit, MediaID: media, UnitKey: "movie"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", library); err != nil {
		t.Fatal(err)
	}
	actor := pgtest.CreateUserActor(t, q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	receipt, err := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, clock.New(), acquisition.SelectMovieRound)).AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{RootID: library, UnitIDs: []uuid.UUID{unit}, CandidateID: candidate, DownloaderID: &clientID})
	if err != nil {
		t.Fatal(err)
	}
	gate.operation = receipt.OperationID
	job, err := jobs.NewQueue(pool, gen, clock.New()).Claim(ctx, []string{"download_submission"}, "m4-real-r3", 3*time.Minute)
	if err != nil || job == nil {
		t.Fatal("missing R3 submission job", err)
	}
	f := &m4R3Fixture{pool: pool, q: q, operation: receipt.OperationID, clientID: clientID, job: job, hash: hash, savePath: savePath, stage: stage, direct: direct, gate: gate}
	f.manager = func() *download.Manager {
		repository := downloadstore.NewRepository(pool, gen, key)
		repository.SetTorrentSource(ordinaryTestSource, torrent.Inspector{})
		manager := download.NewManager(repository)
		manager.SetSubmissionRepository(postgres.NewDownloadSubmissionStore(pool, gen))
		manager.SetClientFactory(func(_ string, endpoint, _, _ string) (ports.DownloadClient, error) {
			client, err := m4R3Client(kind, endpoint, &http.Client{Transport: m4R3Transport{ignoreCancel: stage == "expired-callback-blocked"}, Timeout: 30 * time.Second})
			if err == nil {
				err = client.Test(ctx)
			}
			return client, err
		})
		return manager
	}
	sql := ""
	switch stage {
	case "validated-before-writeback":
		sql = `CREATE FUNCTION m4_reject_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'm4 intentional result rollback'; END $$; CREATE TRIGGER m4_result_fault BEFORE UPDATE OF state ON download_task FOR EACH ROW WHEN (NEW.state IN ('tracking','unknown')) EXECUTE FUNCTION m4_reject_result()`
	case "result-before-commit":
		sql = `CREATE FUNCTION m4_reject_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'm4 intentional tail rollback'; END $$; CREATE TRIGGER m4_result_fault BEFORE INSERT ON outbox_event FOR EACH ROW WHEN (NEW.event_type='download.submission_observed') EXECUTE FUNCTION m4_reject_result()`
	case "deferred-commit-failure":
		sql = `CREATE FUNCTION m4_reject_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'm4 intentional commit rollback'; END $$; CREATE CONSTRAINT TRIGGER m4_result_fault AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='download.submission_observed') EXECUTE FUNCTION m4_reject_result()`
	}
	if sql != "" {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *m4R3Fixture) assertResults(t *testing.T, ctx context.Context, expected int) {
	t.Helper()
	for _, query := range []string{"SELECT count(*) FROM outbox_event WHERE event_type='download.submission_observed' AND aggregate_id=$1", "SELECT count(*) FROM audit_log WHERE action='download.submission_observed' AND payload->>'operation_id'=$1"} {
		var n int
		if err := f.pool.QueryRow(ctx, query, f.operation.String()).Scan(&n); err != nil || n != expected {
			t.Fatalf("atomic result count=%d expected=%d error=%v", n, expected, err)
		}
	}
	if expected == 0 {
		var state string
		err := f.pool.QueryRow(ctx, "SELECT state FROM job WHERE id=$1", f.job.ID).Scan(&state)
		if err != nil || state != "running" {
			t.Fatal("failed or stale callback completed original job", err)
		}
	}
}
func (f *m4R3Fixture) dropFault(t *testing.T, ctx context.Context) {
	t.Helper()
	table := ""
	switch f.stage {
	case "validated-before-writeback":
		table = "download_task"
	case "result-before-commit":
		table = "outbox_event"
	case "deferred-commit-failure":
		table = "audit_log"
	}
	if table != "" {
		if _, err := f.pool.Exec(ctx, "DROP TRIGGER m4_result_fault ON "+table+"; DROP FUNCTION m4_reject_result()"); err != nil {
			t.Fatal(err)
		}
	}
}
func m4R3Client(kind, endpoint string, client *http.Client) (ports.DownloadClient, error) {
	if kind == "transmission" {
		return transmission.NewClient(endpoint, "", "", client)
	}
	return qbittorrent.NewClient(endpoint, "", "", client)
}

type m4R3Transport struct{ ignoreCancel bool }

func (tr m4R3Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr.ignoreCancel && r.Method == http.MethodPost {
		r = r.Clone(context.WithoutCancel(r.Context()))
	}
	return http.DefaultTransport.RoundTrip(r)
}

type m4R3Gate struct {
	t                  *testing.T
	pool               *pgxpool.Pool
	operation          uuid.UUID
	target, stage      string
	transport          *http.Transport
	attempts, accepted atomic.Int32
	reached, release   chan struct{}
	once               sync.Once
}

func (g *m4R3Gate) unblock() { g.once.Do(func() { close(g.release) }) }
func (g *m4R3Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "request body unavailable", http.StatusBadRequest)
		return
	}
	isAdd := r.URL.Path == "/api/v2/torrents/add"
	if r.URL.Path == "/transmission/rpc" {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		isAdd = request.Method == "torrent-add"
	}
	if isAdd {
		g.attempts.Add(1)
		var state string
		if err := g.pool.QueryRow(r.Context(), "SELECT state FROM download_task WHERE operation_id=$1", g.operation).Scan(&state); err != nil || state != "submitting" {
			g.t.Error("Add was not preceded by persisted submitting state", err)
			http.Error(w, "missing durable start", http.StatusInternalServerError)
			return
		}
		if g.stage == "started-before-action" {
			http.Error(w, "intentional pre-action interruption", http.StatusServiceUnavailable)
			return
		}
	}
	target, _ := url.Parse(g.target)
	request := r.Clone(r.Context())
	request.URL.Scheme, request.URL.Host, request.Host = target.Scheme, target.Host, target.Host
	request.RequestURI = ""
	request.Body = io.NopCloser(bytes.NewReader(body))
	response, err := g.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "downstream unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		http.Error(w, "downstream reply unavailable", http.StatusBadGateway)
		return
	}
	if isAdd && response.StatusCode >= 200 && response.StatusCode < 300 {
		g.accepted.Add(1)
		if g.stage == "action-before-reply" {
			http.Error(w, "intentional lost acknowledgement", http.StatusBadGateway)
			return
		}
		if g.stage == "expired-callback-blocked" {
			close(g.reached)
			<-g.release
		}
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}
