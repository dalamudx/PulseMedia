//go:build linux

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each runtime owns a separate pool. Stopping App must not close the assertion
// pool or the authentic Cookie services used to queue work while workers are off.
type m8UntrackedOrganizationFixture struct {
	*m8ScopeConfigFixture
	runtime              *App
	cancel               context.CancelFunc
	done                 chan error
	endpoint             string
	offline              *httptest.Server
	httpClient           *http.Client
	metadataClient       *http.Client
	metadataCalls        atomic.Int32
	base                 string
	fileID               uuid.UUID
	confirmation         ports.UntrackedConfirmationReceipt
	confirmationRaw      []byte
	confirmationBody     string
	reviewRaw            []byte
	sourceRelative       string
	sourceBytes          []byte
	sourceIdentity       ports.FileIdentity
	protected            string
	baselineFiles        int
	configureRuntimePool func(*pgxpool.Config) // Test-owned transport fault injection only.
}

func m8UntrackedOrganizationRequired(t *testing.T) {
	t.Helper()
	if os.Getenv("PULSE_M8_UNTRACKED_ORGANIZATION_ACCEPTANCE") != "1" {
		t.Skip("explicit owned organization acceptance required")
	}
}

type m8OrganizationDirectories struct{ source, target, remoteSource string }

func m8UntrackedOrganizationNew(t *testing.T, action string, mediaTypes ...string) *m8UntrackedOrganizationFixture {
	t.Helper()
	return m8UntrackedOrganizationWithDirectories(t, action, nil, mediaTypes...)
}

func m8UntrackedOrganizationWithDirectories(t *testing.T, action string, directories *m8OrganizationDirectories, mediaTypes ...string) *m8UntrackedOrganizationFixture {
	t.Helper()
	mediaType, fileName, targetRelative, namespace := "movie", "Movie.2026.mkv", "Movie (2026)/Movie.mkv", "tmdb.movie"
	unitCount, metadataCount := 1, int32(2)
	if len(mediaTypes) == 1 {
		if mediaTypes[0] != "tv" {
			t.Fatal("unsupported organization fixture type")
		}
		mediaType, fileName, targetRelative, namespace = "tv", "Series.2026.S01E02E07.mkv", "Series (2026)/Season 01/Series.S01E02E07.mkv", "tmdb.tv"
		unitCount, metadataCount = 2, 3
	}
	t.Setenv("TMDB_API_KEY", "owned-untracked-organization")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	base := m7NewScrapeFixture(t)
	sourceDirectory, targetDirectory := t.TempDir(), t.TempDir()
	if directories != nil {
		sourceDirectory, targetDirectory = directories.source, directories.target
	}
	f := &m8UntrackedOrganizationFixture{
		m8ScopeConfigFixture: &m8ScopeConfigFixture{
			m7ScrapeFixture: base, scope: uuid.New(), client: uuid.New(), root: uuid.New(),
			source: sourceDirectory, target: targetDirectory,
		},
		sourceRelative: fileName,
		sourceBytes:    []byte("user source remains"),
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(remote.Close)
	secret := crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32))
	cipher, err := secret.EncryptSecret([]byte("owned-organization-password"))
	if err != nil {
		t.Fatal("encrypt owned downloader fixture")
	}
	mapping := m8UntrackedOrganizationJSON(t, map[string]string{"/remote": f.source})
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'organization-owned','qbittorrent',$2,'owned-user',$3,$4)`, f.client, remote.URL, cipher, []byte(mapping))
	// No SQL scan baseline or media facts: App's native scanner establishes them.
	f.exec(t, `INSERT INTO library_root(id,path,name,media_type,marker_filename) VALUES($1,$2,'organization-target',$3,'.scope-marker')`, f.root, f.target, mediaType)
	for name, body := range map[string]string{".scope-marker": "owned", "user.nfo": "user sidecar remains"} {
		if err = os.WriteFile(filepath.Join(f.target, name), []byte(body), 0600); err != nil {
			t.Fatal("write owned target marker/sidecar")
		}
	}
	f.input = ports.UntrackedScopeInput{Enabled: true, ClientID: f.client, RemotePrefix: "/remote", TargetRootID: f.root}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.metadataCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Query().Get("api_key") != "owned-untracked-organization" || r.URL.Query().Get("language") != "zh-CN" {
			t.Error("native owned metadata request lost method, key or locale")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		movie := `{"id":551,"media_type":"movie","title":"Movie","original_title":"Movie","release_date":"2026-01-01","overview":"Owned organization fixture","vote_average":8}`
		body := movie
		switch r.URL.Path {
		case "/3/search/movie":
			if r.URL.Query().Get("query") != "Movie" || r.URL.Query().Get("year") != "2026" {
				t.Error("native review search lost parsed movie identity")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body = `{"page":1,"total_pages":1,"total_results":1,"results":[` + movie + `]}`
		case "/3/movie/551":
		case "/3/search/tv", "/3/tv/551", "/3/tv/551/season/1":
			if mediaType != "tv" {
				t.Error("TV endpoint in movie fixture")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		default:
			t.Error("unexpected owned organization metadata endpoint")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if mediaType == "tv" {
			series := `{"id":551,"media_type":"tv","name":"Series","original_name":"Series","first_air_date":"2026-01-01","overview":"Owned sparse TV fixture","number_of_episodes":2,"number_of_seasons":1,"seasons":[{"season_number":1,"episode_count":2}],"vote_average":8}`
			switch r.URL.Path {
			case "/3/search/tv":
				if r.URL.Query().Get("query") != "Series" || r.URL.Query().Get("first_air_date_year") != "2026" {
					t.Error("native TV search lost parsed identity")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body = `{"page":1,"total_pages":1,"total_results":1,"results":[` + series + `]}`
			case "/3/tv/551":
				body = series
			case "/3/tv/551/season/1":
				body = `{"id":5501,"season_number":1,"episodes":[{"id":55102,"episode_number":2,"season_number":1,"name":"Second","air_date":"2026-01-02"},{"id":55107,"episode_number":7,"season_number":1,"name":"Seventh","air_date":null}]}`
			default:
				t.Error("unexpected owned sparse TV endpoint")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if _, e := io.WriteString(w, body); e != nil && r.Context().Err() == nil {
			t.Error("owned metadata response failed")
		}
	}))
	t.Cleanup(metadata.Close)
	target, err := url.Parse(metadata.URL)
	if err != nil {
		t.Fatal("parse owned metadata destination")
	}
	metadataTransport := &http.Transport{Proxy: nil}
	t.Cleanup(metadataTransport.CloseIdleConnections)
	f.metadataClient = &http.Client{Transport: m8WishRoute{target: target, transport: metadataTransport}, Timeout: 10 * time.Second}
	httpTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second}
	t.Cleanup(httpTransport.CloseIdleConnections)
	f.httpClient = &http.Client{Transport: httpTransport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// These are production services and the production Cookie router. This server
	// stays available after App has cancelled and joined all physical workers.
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetUntrackedScopeService(library.NewUntrackedScopeService(postgres.NewUntrackedScopeStore(f.pool, f.app.IDGen(), f.app.Clock()), rootfs.NewFilesystem()))
	h.SetUntrackedDiscoveryService(library.NewUntrackedDiscoveryReader(postgres.NewUntrackedDiscoveryStore(f.pool, f.app.IDGen(), f.app.Clock())))
	h.SetUntrackedReviewService(library.NewUntrackedReviewService(postgres.NewUntrackedReviewStore(f.pool, f.app.IDGen(), f.app.Clock()), rootfs.NewFilesystem()))
	h.SetUntrackedOrganizationService(library.NewUntrackedOrganizationService(postgres.NewUntrackedOrganizationStore(f.pool, f.app.IDGen(), f.app.Clock()), rootfs.NewFilesystem()))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	f.offline = httptest.NewServer(f.router)
	t.Cleanup(f.offline.Close)
	t.Cleanup(func() { f.stop(t) })
	f.start(t)
	f.wait(t, "native target root baseline and settled startup scans", func() bool {
		return f.predicate(t, `SELECT EXISTS(SELECT 1 FROM scan_run run JOIN scan_finalization fin ON fin.scan_run_id=run.id WHERE run.root_id=$1 AND run.state='succeeded' AND fin.exhausted AND fin.completed_at IS NOT NULL) AND NOT EXISTS(SELECT 1 FROM library_root WHERE NOT baseline_completed OR health<>'online') AND NOT EXISTS(SELECT 1 FROM job WHERE (kind LIKE 'library_%' OR kind='movie_recognition') AND state IN ('queued','running')) AND NOT EXISTS(SELECT 1 FROM library_scan_schedule WHERE next_run_at<=clock_timestamp())`, f.root)
	})
	f.protected = f.protectedGraph(t)
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM library_file WHERE root_id=$1 AND retired_at IS NULL`, f.root).Scan(&f.baselineFiles); err != nil {
		t.Fatal("read settled root inventory baseline")
	}
	if err = f.pool.QueryRow(t.Context(), `SELECT c.updated_at,r.generation FROM download_client c CROSS JOIN library_root r WHERE c.id=$1 AND r.id=$2`, f.client, f.root).Scan(&f.input.ExpectedClientVersion, &f.input.ExpectedRootGeneration); err != nil {
		t.Fatal("read real scope config versions")
	}
	raw := f.expect(t, http.MethodPut, f.path(), f.body(t, 0, true), "organization-scope", http.StatusOK)
	var scope ports.UntrackedScopeReceipt
	if json.Unmarshal(raw, &scope) != nil || scope.ExecutionAllowed || scope.Scope.Version != 1 || scope.Scope.SourceIdentity.Inode == scope.Scope.TargetIdentity.Inode && scope.Scope.SourceIdentity.Device == scope.Scope.TargetIdentity.Device {
		t.Fatal("scope failed to freeze independent native roots")
	}
	var sweep time.Time
	f.wait(t, "native source watcher and settled empty initial sweep", func() bool {
		if directories == nil && !m8DiscoveryKernelReady(t, f.source) {
			return false
		}
		var ready bool
		e := f.pool.QueryRow(t.Context(), `SELECT active_job_id IS NULL AND completed_sequence=requested_sequence AND completed_sequence>0 AND next_sweep_at>clock_timestamp()+interval '40 seconds',next_sweep_at FROM untracked_discovery_state WHERE scope_id=$1`, f.scope).Scan(&ready, &sweep)
		return e == nil && ready
	})
	writePath := filepath.Join(f.source, f.sourceRelative)
	if directories != nil {
		writePath = filepath.Join(directories.remoteSource, f.sourceRelative)
	}
	if err = os.WriteFile(writePath, f.sourceBytes, 0600); err != nil {
		t.Fatal("close actual owned source video")
	}
	var revision int64
	waitForDiscovery := f.wait
	discoveryLabel := "close-write discovery before original periodic sweep"
	if directories != nil {
		discoveryLabel = "NFS remote write discovered by original periodic sweep"
		waitForDiscovery = func(t *testing.T, label string, probe func() bool) { f.waitWithin(t, label, 75*time.Second, probe) }
	}
	waitForDiscovery(t, discoveryLabel, func() bool {
		var idle bool
		var created time.Time
		e := f.pool.QueryRow(t.Context(), `SELECT f.id,f.revision,j.created_at,d.active_job_id IS NULL AND d.completed_sequence=d.requested_sequence FROM untracked_discovered_file f JOIN job j ON j.id=f.last_job_id JOIN untracked_discovery_state d ON d.scope_id=f.scope_id WHERE f.scope_id=$1 AND f.relative_path=$2`, f.scope, f.sourceRelative).Scan(&f.fileID, &revision, &created, &idle)
		if directories != nil {
			return e == nil && idle && !created.Before(sweep)
		}
		return e == nil && idle && created.Before(sweep)
	})
	f.sourceIdentity = m8UntrackedOrganizationIdentity(t, f.source, f.sourceRelative)
	f.base = f.path() + "/discoveries/" + f.fileID.String()
	reviewBody := m8UntrackedOrganizationJSON(t, ports.UntrackedReviewInput{ScopeVersion: scope.Scope.Version, FileRevision: revision, MediaType: mediaType})
	f.reviewRaw = f.expect(t, http.MethodPost, f.base+"/reviews", reviewBody, "organization-review", http.StatusAccepted)
	var request ports.UntrackedReviewRequestReceipt
	if json.Unmarshal(f.reviewRaw, &request) != nil || request.ExecutionAllowed || request.Request.ID == uuid.Nil {
		t.Fatal("invalid explicit native review request")
	}
	var view ports.UntrackedReview
	f.wait(t, "complete native movie candidate and succeeded review job", func() bool {
		code, body, e := f.call(t, http.MethodGet, f.base+"/reviews/"+request.Request.ID.String(), "", "", f.cookie, "")
		return e == nil && code == http.StatusOK && json.Unmarshal(body, &view) == nil && view.Evidence != nil && view.Evidence.Candidate != nil && f.predicate(t, `SELECT state='succeeded' FROM job WHERE id=$1`, request.Request.JobID)
	})
	candidate := view.Evidence.Candidate
	if view.Stale || view.ExecutionAllowed || !view.Evidence.Complete || view.Evidence.Reason != "ready" || candidate.Namespace != namespace || candidate.TMDBID != 551 || len(candidate.Units) != unitCount || len(view.MaterialHash) != 64 || f.metadataCalls.Load() != metadataCount {
		t.Fatal("explicit review lacks exact native movie identity/coverage")
	}
	if mediaType == "movie" && candidate.Units[0].UnitKey != "movie" {
		t.Fatal("movie unit changed")
	}
	if mediaType == "tv" {
		if candidate.Units[0].UnitKey != "S01E02" || candidate.Units[0].TMDBEpisodeID != 55102 || candidate.Units[0].Season != 1 || candidate.Units[0].Episode != 2 || candidate.Units[0].AirDate == nil || candidate.Units[1].UnitKey != "S01E07" || candidate.Units[1].TMDBEpisodeID != 55107 || candidate.Units[1].Season != 1 || candidate.Units[1].Episode != 7 || candidate.Units[1].AirDate != nil {
			t.Fatal("native sparse TV identity/date/coverage changed")
		}
	}
	confirmation := ports.UntrackedConfirmationInput{ReviewID: request.Request.ID, MaterialHash: view.MaterialHash, ScopeVersion: scope.Scope.Version, FileRevision: revision, CandidateKey: candidate.Key, Action: action, TargetRelativePath: targetRelative}
	f.confirmationBody = m8UntrackedOrganizationJSON(t, confirmation)
	f.confirmationRaw = f.expect(t, http.MethodPost, f.base+"/confirmations", f.confirmationBody, "organization-confirmation", http.StatusOK)
	if json.Unmarshal(f.confirmationRaw, &f.confirmation) != nil || f.confirmation.State != "confirmed_pending_organization" || f.confirmation.ExecutionAllowed || f.confirmation.Material.ID == uuid.Nil || f.confirmation.Material.Input != confirmation {
		t.Fatal("schema104 confirmation changed frozen intent or granted execution")
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
	f.count(t, `SELECT count(*) FROM unit_fact WHERE stage='imported'`, 0)
	if f.protected != f.protectedGraph(t) || f.calls.Load() != 0 {
		t.Fatal("pre-organization flow changed acquisition graph or called downloader")
	}
	f.stop(t)
	return f
}

func m8UntrackedOrganizationJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("encode owned organization command")
	}
	return string(raw)
}

func (f *m8UntrackedOrganizationFixture) start(t *testing.T) {
	t.Helper()
	config := f.pool.Config().Copy()
	if f.configureRuntimePool != nil {
		f.configureRuntimePool(config)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("open owned runtime pool")
	}
	a := f.app
	if f.runtime == nil {
		a.dbPool = pool
		a.metadataHTTPClient = f.metadataClient
	} else {
		a, err = New(f.app.cfg, WithDBPool(pool), WithMetadataHTTPClient(f.metadataClient), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err != nil {
			pool.Close()
			t.Fatal("rebuild owned production App")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = a.Close()
		t.Fatal("reserve owned App listen address")
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		_ = a.Close()
		t.Fatal("release owned listen reservation")
	}
	a.cfg.Server.HTTPAddr = address
	f.runtime = a
	f.endpoint = "http://" + address
	ctx, cancel := context.WithCancel(t.Context())
	f.cancel, f.done = cancel, make(chan error, 1)
	done := f.done
	go func() { done <- a.Run(ctx) }()
	f.wait(t, "live production organization routes", func() bool {
		code, _, e := f.call(t, http.MethodGet, f.path(), "", "", f.cookie, "")
		return e == nil && code == http.StatusOK
	})
}

func (f *m8UntrackedOrganizationFixture) stop(t *testing.T) {
	t.Helper()
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case err := <-f.done:
		if err != nil {
			t.Error("owned organization App returned an error")
		}
	case <-time.After(45 * time.Second):
		t.Fatal("owned organization App did not drain actual I/O")
	}
	if err := f.runtime.Close(); err != nil {
		t.Error("owned organization App resource release failed")
	}
	f.cancel = nil
	f.endpoint = f.offline.URL
}

func (f *m8UntrackedOrganizationFixture) wait(t *testing.T, label string, probe func() bool) {
	t.Helper()
	f.waitWithin(t, label, 20*time.Second, probe)
}

func (f *m8UntrackedOrganizationFixture) waitWithin(t *testing.T, label string, duration time.Duration, probe func() bool) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if probe() {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("organization acceptance wait: %s", label)
		case err := <-f.done:
			f.done <- err
			t.Fatalf("App stopped before %s", label)
		case <-t.Context().Done():
			t.Fatalf("organization acceptance cancelled before %s", label)
		case <-ticker.C:
		}
	}
}

func (f *m8UntrackedOrganizationFixture) call(t *testing.T, method, path, body, key string, cookie *http.Cookie, bearer string) (int, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.endpoint+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("construct owned HTTP request")
	}
	req.Header.Set("Origin", f.endpoint)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := f.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("owned HTTP request unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil || response.Header.Get("Cache-Control") != "private, no-store" {
		return response.StatusCode, nil, fmt.Errorf("owned HTTP body/cache contract")
	}
	return response.StatusCode, raw, nil
}

func (f *m8UntrackedOrganizationFixture) expect(t *testing.T, method, path, body, key string, want int) []byte {
	t.Helper()
	code, raw, err := f.call(t, method, path, body, key, f.cookie, "")
	if err != nil || code != want {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &problem)
		t.Fatalf("organization HTTP method=%s path=%s status=%d want=%d code=%s", method, strings.Split(path, "?")[0], code, want, problem.Code)
	}
	return raw
}

func (f *m8UntrackedOrganizationFixture) predicate(t *testing.T, query string, args ...any) bool {
	t.Helper()
	var yes bool
	return f.pool.QueryRow(t.Context(), query, args...).Scan(&yes) == nil && yes
}

func (f *m8UntrackedOrganizationFixture) count(t *testing.T, query string, want int, args ...any) {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil || count != want {
		t.Fatalf("organization SQL count=%d want=%d query_succeeded=%t query_prefix=%q", count, want, err == nil, query[:min(len(query), 100)])
	}
}

func (f *m8UntrackedOrganizationFixture) protectedGraph(t *testing.T) string {
	t.Helper()
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, "download_client", "download_task", "download_requirement", "release_candidate", "unit_reservation", "acquisition_reservation", "wanted_item", "subscription")
}

func (f *m8UntrackedOrganizationFixture) allTables(t *testing.T) string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'app_session' ORDER BY tablename`)
	if err != nil {
		t.Fatal("list full protected public tables")
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatal("read protected table name")
		}
		tables = append(tables, table)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal("finish protected table list")
	}
	// Authentication may Touch its session. Every business, lease, history,
	// delivery, audit and outbox row remains in this comparison.
	return m8ConfigSnapshot(t, f.m7ScrapeFixture, tables...)
}

func m8UntrackedOrganizationIdentity(t *testing.T, rootPath, relative string) ports.FileIdentity {
	t.Helper()
	root, err := rootfs.NewFilesystem().OpenRoot(t.Context(), rootPath)
	if err != nil {
		t.Fatal("open owned native proof root")
	}
	defer root.Close()
	identity, err := root.Inspect(t.Context(), relative)
	if err != nil {
		t.Fatal("inspect owned native file proof")
	}
	return identity
}

func m8UntrackedOrganizationTree(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if entry.IsDir() {
			fmt.Fprintf(&out, "%s:directory\n", relative)
			return nil
		}
		body, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		identity := m8UntrackedOrganizationIdentity(t, root, relative)
		fmt.Fprintf(&out, "%s:%v:%x\n", relative, identity, sha256.Sum256(body))
		return nil
	})
	if err != nil {
		t.Fatal("snapshot owned native tree")
	}
	return out.String()
}
