package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

// Route only the real TMDB adapter's outbound HTTP to an owned protocol server.
// No public/internal application handler or metadata provider is substituted.
type m5AcquisitionMetadataRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r m5AcquisitionMetadataRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.themoviedb.org" || req.URL.Path != "/3/movie/550" {
		return nil, errors.New("unexpected outbound metadata request")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &u, r.target.Host
	return r.transport.RoundTrip(clone)
}

func newM5AcquisitionAcceptance(t *testing.T) (*m5IndexerAcceptance, *atomic.Int32, func()) {
	t.Helper()
	// This check precedes all PG cluster/database creation.
	if os.Getenv("PULSE_M5_INDEXER_ACCEPTANCE") != "1" {
		t.Skip("set PULSE_M5_INDEXER_ACCEPTANCE=1 with an owned PG18.6 fixture")
	}
	if !strings.HasPrefix(os.Getenv("TMPDIR"), "/data/temp/") || os.Getenv("TMP") != os.Getenv("TMPDIR") || os.Getenv("TEMP") != os.Getenv("TMPDIR") {
		t.Fatal("owned TMPDIR/TMP/TEMP under /data/temp required")
	}
	// Force the dynamic provider to read the encrypted setting, never host credentials.
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(t.Context(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/3/movie/550" || r.URL.Query().Get("api_key") != "m5-local-metadata-credential" || r.URL.Query().Get("language") != "zh-CN" {
			t.Error("unexpected native TMDB request or settings credential")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":550,"media_type":"movie","title":"Fight Club","original_title":"Fight Club","release_date":"1999-10-15","imdb_id":"tt0137523","overview":"Owned local metadata protocol fixture"}`)
	}))
	t.Cleanup(metadata.Close)
	target, err := url.Parse(metadata.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: m5AcquisitionMetadataRoute{target, transport}, Timeout: 5 * time.Second}
	cfg := config.NewDefault()
	cfg.Database.URL, cfg.DataDir = dsn, filepath.Join(t.TempDir(), "data")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, app.WithMetadataHTTPClient(client), app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	f := &m5IndexerAcceptance{app: a, base: "http://" + cfg.Server.HTTPAddr}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("App.Run shutdown: %v", err)
				}
			case <-time.After(50 * time.Second):
				t.Error("App cancellation/join exceeded shutdown budget")
			}
			if err := a.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, e := (&http.Client{Timeout: time.Second}).Get(f.base + "/api/v1/auth/status")
		if e == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case e := <-done:
			done <- e
			t.Fatalf("App startup: %v", e)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("App HTTP readiness deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var version string
	if err = a.DBPool().QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("PostgreSQL 18.6 required", err)
	}
	f.key, err = crypto.EnsureMasterKey(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	setup := f.request(t, http.MethodPost, "/api/v1/auth/setup", "", map[string]string{"username": "m5-acquisition-admin", "password": "SyntheticM5Acquisition123!"}, "")
	m5IndexerStatus(t, setup, http.StatusOK)
	for _, cookie := range (&http.Response{Header: setup.header}).Cookies() {
		if cookie.Name == "pulsemedia_session" {
			f.session = cookie.Value
		}
	}
	if f.session == "" {
		t.Fatal("public setup did not issue session Cookie")
	}
	setting := f.request(t, http.MethodPut, "/api/v1/settings/tmdb.api_key", "m5-local-metadata", map[string]any{"expected_version": 0, "value": map[string]any{"api_key": "m5-local-metadata-credential"}}, f.session)
	m5IndexerStatus(t, setting, http.StatusOK)
	var encoded string
	if err = a.DBPool().QueryRow(t.Context(), "SELECT value::text FROM setting WHERE key='tmdb.api_key'").Scan(&encoded); err != nil || !strings.Contains(encoded, "ciphertext") || strings.Contains(encoded, "m5-local-metadata-credential") {
		t.Fatal("TMDB setting must use the actual encrypted settings codec", err)
	}
	return f, calls, stop
}

// Run with the private PG wrapper and -race -timeout 4m. All candidates and query
// observations below are produced by App.Run and native Torznab HTTP, never SQL.
// The controlled qBittorrent stub receives a native Add; it transfers no content.
func TestM5InteractiveSearchSelectedMovieAcquisitionNativeHTTPPG(t *testing.T) {
	f, metadataCalls, stop := newM5AcquisitionAcceptance(t)
	const acquisitionPath = "/api/v1/acquisitions/selected-movie"
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const magnet = "magnet:?xt=urn:btih:" + hash
	var sourceSearches, adds, logins atomic.Int32
	var sourceOffline atomic.Bool
	allowAdd := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowAdd) }) }
	var qbMu sync.Mutex
	var savedPath, savedTags string
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.FormValue("username") != "m5-qbit-edited" || r.FormValue("password") != "m5-local-downloader-password" {
				t.Error("native qBittorrent login lost selected current configuration")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			logins.Add(1)
			select {
			case <-allowAdd:
			case <-r.Context().Done():
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned-protocol-session", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			adds.Add(1)
			if r.Method != http.MethodPost || r.ParseMultipartForm(1<<20) != nil {
				t.Error("expected native multipart Add")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.MultipartForm != nil {
				defer func() {
					if err := r.MultipartForm.RemoveAll(); err != nil {
						t.Error(err)
					}
				}()
			}
			cookie, err := r.Cookie("SID")
			if err != nil || cookie.Value != "owned-protocol-session" || r.FormValue("urls") != magnet || r.FormValue("savepath") == "" || r.FormValue("tags") == "" {
				t.Error("native Add lost authenticated magnet, root or operation tag")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			qbMu.Lock()
			savedPath, savedTags = r.FormValue("savepath"), r.FormValue("tags")
			qbMu.Unlock()
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			qbMu.Lock()
			path, tags := savedPath, savedTags
			qbMu.Unlock()
			if path == "" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "name": "Fight.Club.1999.1080p.BluRay.x264", "save_path": path, "content_path": filepath.Join(path, "Fight.Club.1999.mkv"), "progress": 0.1, "size": 123456789, "downloaded": 12345678, "state": "downloading", "tags": tags}})
		default:
			t.Errorf("unexpected native downloader operation %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() { release(); qb.Close() })
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api" || q.Get("apikey") != "m5-local-source-credential" {
			t.Error("unexpected native Torznab configuration")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if sourceOffline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch q.Get("t") {
		case "caps":
			_, _ = io.WriteString(w, `<caps><limits default="50" max="100"/><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
		case "movie":
			if q.Get("q") != "Fight Club" {
				_, _ = io.WriteString(w, `<rss><channel/></rss>`)
				return
			}
			if q.Get("limit") != "50" || (q.Get("offset") != "" && q.Get("offset") != "0") {
				t.Error("interactive source lost native paging")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sourceSearches.Add(1)
			_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="1"/><item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>m5-interactive-selected-movie</guid><enclosure url="%s" length="123456789" type="application/x-bittorrent"/><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="42"/><torznab:attr name="peers" value="2"/></item></channel></rss>`, magnet, hash)
		default:
			t.Error("unexpected native Torznab operation")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(source.Close)
	// Last registered: cancel and join App before closing its protocol servers.
	t.Cleanup(stop)

	created := f.request(t, http.MethodPost, "/api/v1/indexers", "m5-acquisition-source", map[string]any{"name": "M5 selected native source", "protocol": "torznab", "url": source.URL, "api_key": "m5-local-source-credential", "categories": []int{2000}}, f.session)
	m5IndexerStatus(t, created, http.StatusCreated)
	sourceID := uuid.MustParse(m5InteractiveString(t, m5IndexerObject(t, created), "id"))
	probe := func(online bool) {
		t.Helper()
		// Observe the real shared endpoint cooldown; never rewrite its budget.
		budgetDeadline := time.Now().Add(45 * time.Second)
		for m5InteractiveInt(t, f, `SELECT CASE WHEN next_query_at<=clock_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=clock_timestamp()) THEN 1 ELSE 0 END FROM indexer WHERE id=$1`, sourceID) != 1 {
			if time.Now().After(budgetDeadline) {
				t.Fatal("native source probe budget deadline")
			}
			time.Sleep(25 * time.Millisecond)
		}
		sourceOffline.Store(!online)
		r := f.request(t, http.MethodPost, "/api/v1/indexers/"+sourceID.String()+"/test", "", nil, f.session)
		m5IndexerStatus(t, r, http.StatusOK)
		if m5IndexerObject(t, r)["alive"] != online {
			t.Fatal("native source health probe mismatch")
		}
	}
	clientBody := map[string]any{"name": "M5 chosen downloader", "client_type": "qbittorrent", "url": qb.URL, "username": "m5-qbit-original", "password": "m5-local-downloader-password", "remote_path_map": map[string]string{}}
	createdClient := f.request(t, http.MethodPost, "/api/v1/download-clients", "", clientBody, f.session)
	m5IndexerStatus(t, createdClient, http.StatusCreated)
	clientView := m5IndexerObject(t, createdClient)
	clientID := uuid.MustParse(m5InteractiveString(t, clientView, "id"))

	// Default roots have real on-disk markers. Wait for the owned native scanner,
	// rather than fabricating baseline_completed, health or filesystem identities.
	var rootID uuid.UUID
	var rootPath string
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := f.app.DBPool().QueryRow(t.Context(), `SELECT id,path FROM library_root WHERE media_type='movie' AND health='online' AND baseline_completed ORDER BY id LIMIT 1`).Scan(&rootID, &rootPath)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native root baseline deadline", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(rootPath, ".pulsemedia_root")); err != nil {
		t.Fatal("selected root has no real identity marker", err)
	}

	search := m5InteractiveCreate(t, f, "m5-acquisition-search", map[string]any{"query": "Fight Club"})
	searchID := m5InteractiveString(t, m5IndexerObject(t, search), "id")
	snapshot := m5InteractiveWaitSnapshot(t, f, searchID, m5InteractiveTerminal)
	rows := m5InteractiveRows(t, snapshot, "results")
	if snapshot["state"] != "succeeded" || len(rows) != 1 || sourceSearches.Load() != 1 {
		t.Fatal("expected one actual native interactive candidate", snapshot)
	}
	candidateID := uuid.MustParse(m5InteractiveString(t, rows[0].(map[string]any), "id"))
	if n := m5InteractiveInt(t, f, `SELECT count(*) FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id JOIN interactive_search_query iq ON iq.query_id=q.id WHERE iq.search_id=$1 AND q.indexer_id=$2 AND qc.candidate_id=$3 AND q.query_type='interactive' AND q.state='succeeded' AND q.finished_at IS NOT NULL`, uuid.MustParse(searchID), sourceID, candidateID); n != 1 {
		t.Fatal("candidate lacks its actual successful interactive query", n)
	}
	if n := m5InteractiveInt(t, f, `SELECT count(*) FROM indexer_query`); n != 1 {
		t.Fatal("unexpected legacy search proof", n)
	}
	m5InteractiveQuiesce(t, f)
	assertWork := func(want int) {
		t.Helper()
		for _, query := range []string{
			`SELECT count(*) FROM job WHERE kind='download_submission'`,
			`SELECT count(*) FROM unit_reservation`,
			`SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL`,
			`SELECT count(*) FROM download_requirement`,
			`SELECT count(*) FROM audit_log WHERE action='movie.selection.committed'`,
			`SELECT count(*) FROM idempotency_record WHERE idempotency_key='m5-interactive-acquire'`,
		} {
			if n := m5InteractiveInt(t, f, query); n != want {
				t.Fatalf("acquisition count %d, want %d: %s", n, want, query)
			}
		}
	}
	assertWork(0)
	before := m5InteractiveReadFingerprint(t, f)
	for range 3 {
		m5InteractiveGet(t, f, searchID)
	}
	legacy := f.request(t, http.MethodGet, "/api/v1/releases/search?q=Fight%20Club", "", nil, f.session)
	m5IndexerStatus(t, legacy, http.StatusBadRequest)
	if before != m5InteractiveReadFingerprint(t, f) || sourceSearches.Load() != 1 || adds.Load() != 0 || metadataCalls.Load() != 0 {
		t.Fatal("GET search mutated durable search or caused metadata/acquisition/native Add")
	}
	assertWork(0)

	selection := ports.MovieSelection{TMDBID: 550, RootID: rootID, CandidateID: candidateID, DownloaderID: clientID, PolicyID: "default"}
	preview := func() ports.MovieSelectionPreview {
		t.Helper()
		r := f.request(t, http.MethodPost, acquisitionPath+"/preview", "", selection, f.session)
		m5IndexerStatus(t, r, http.StatusOK)
		var p ports.MovieSelectionPreview
		if err := json.Unmarshal(r.body, &p); err != nil || p.Selection != selection || len(p.InputStamp) != 64 || p.Title != "Fight Club" || p.Year != 1999 {
			t.Fatal("invalid public selected-movie preview", err, string(r.body))
		}
		return p
	}
	p := preview()
	if p.Decision != "accept" || p.Score <= 0 || metadataCalls.Load() != 1 {
		t.Fatalf("interactive candidate must preview accept using native TMDB: %+v calls=%d", p, metadataCalls.Load())
	}
	command := ports.MovieSelectionCommand{Selection: selection, ExpectedInputStamp: p.InputStamp, Confirmed: true}
	m5IndexerStatus(t, f.request(t, http.MethodPost, acquisitionPath, "anonymous-acquire", command, ""), http.StatusUnauthorized)
	unconfirmed := command
	unconfirmed.Confirmed = false
	m5IndexerStatus(t, f.request(t, http.MethodPost, acquisitionPath, "unconfirmed-acquire", unconfirmed, f.session), http.StatusBadRequest)

	// A visible offline observation is insufficient acquisition authority.
	probe(false)
	offline := preview()
	if offline.Decision != "hold" || offline.Reason != "source_unproven" {
		t.Fatal("offline source retained acquisition authority", offline)
	}
	stale := f.request(t, http.MethodPost, acquisitionPath, "offline-stale-stamp", command, f.session)
	m5IndexerStatus(t, stale, http.StatusConflict)
	if !bytes.Contains(stale.body, []byte("VERSION_CONFLICT")) {
		t.Fatal("offline source did not invalidate accepted preview", string(stale.body))
	}
	offlineCommand := command
	offlineCommand.ExpectedInputStamp = offline.InputStamp
	rejected := f.request(t, http.MethodPost, acquisitionPath, "offline-current-stamp", offlineCommand, f.session)
	m5IndexerStatus(t, rejected, http.StatusConflict)
	if !bytes.Contains(rejected.body, []byte("UNSUPPORTED_COVERAGE")) {
		t.Fatal("offline current preview unexpectedly authorized acquisition", string(rejected.body))
	}
	probe(true)
	p = preview()
	if p.Decision != "accept" {
		t.Fatal("actual successful health probe failed to restore eligibility", p)
	}
	command.ExpectedInputStamp = p.InputStamp
	clientBody["username"] = "m5-qbit-edited"
	clientBody["password"] = ""
	clientBody["expected_updated_at"] = clientView["updated_at"]
	edited := f.request(t, http.MethodPut, "/api/v1/download-clients/"+clientID.String(), "", clientBody, f.session)
	m5IndexerStatus(t, edited, http.StatusOK)
	stale = f.request(t, http.MethodPost, acquisitionPath, "client-stale-stamp", command, f.session)
	m5IndexerStatus(t, stale, http.StatusConflict)
	if !bytes.Contains(stale.body, []byte("VERSION_CONFLICT")) {
		t.Fatal("changed downloader configuration kept old preview authority", string(stale.body))
	}
	assertWork(0)
	p = preview()
	if p.Decision != "accept" || p.InputStamp == command.ExpectedInputStamp {
		t.Fatal("new selected downloader configuration needs a fresh accepted preview", p)
	}
	command.ExpectedInputStamp = p.InputStamp
	first := f.request(t, http.MethodPost, acquisitionPath, "m5-interactive-acquire", command, f.session)
	m5IndexerStatus(t, first, http.StatusAccepted)
	if first.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("acquisition receipt must not be cacheable")
	}
	var receipt ports.MovieSelectionReceipt
	if err := json.Unmarshal(first.body, &receipt); err != nil || receipt.OperationID == uuid.Nil || receipt.MediaID == uuid.Nil || receipt.UnitID == uuid.Nil || receipt.RootID != rootID || receipt.CandidateID != candidateID || receipt.Status != "dispatched" {
		t.Fatal("public acquisition receipt did not bind explicit selection", err, string(first.body))
	}
	assertWork(1)
	callsBeforeRecovery := metadataCalls.Load()
	recoverReceipt := func() {
		t.Helper()
		r := f.request(t, http.MethodPost, acquisitionPath, "m5-interactive-acquire", command, f.session)
		m5IndexerStatus(t, r, http.StatusAccepted)
		if !bytes.Equal(r.body, first.body) || metadataCalls.Load() != callsBeforeRecovery {
			t.Fatal("same-key recovery changed original receipt or called metadata")
		}
		assertWork(1)
	}
	recoverReceipt()
	if adds.Load() != 0 {
		t.Fatal("native Add occurred before controlled protocol release")
	}
	t.Logf("queued receipt=202 queries=3 native_probe_queries=2 interactive_queries=1 candidates=1 submission_jobs=1 unit_placeholders=1 acquisition_protections=1 requirements=1 received_Add=0")
	release()
	deadline = time.Now().Add(25 * time.Second)
	for {
		n := m5InteractiveInt(t, f, `SELECT count(*) FROM download_task dt JOIN job j ON j.payload->>'operation_id'=dt.operation_id::text WHERE dt.operation_id=$1 AND dt.candidate_id=$2 AND dt.client_id=$3 AND dt.info_hash=$4 AND dt.state='tracking' AND j.kind='download_submission' AND j.state='succeeded'`, receipt.OperationID, candidateID, clientID, hash)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			var diagnostic string
			if err := f.app.DBPool().QueryRow(t.Context(), `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(jsonb_build_object('state',state,'attempts',attempts,'error',error)) FROM job WHERE kind='download_submission'),'tasks',(SELECT jsonb_agg(jsonb_build_object('state',state)) FROM download_task))::text`).Scan(&diagnostic); err != nil {
				t.Fatal("native submission deadline; diagnostic unavailable", err)
			}
			t.Fatalf("native submission deadline: Add=%d login=%d %s", adds.Load(), logins.Load(), diagnostic)
		}
		time.Sleep(25 * time.Millisecond)
	}
	recoverReceipt()
	if adds.Load() != 1 || logins.Load() != 1 || sourceSearches.Load() != 1 || m5InteractiveInt(t, f, "SELECT count(*) FROM download_task") != 1 {
		t.Fatalf("duplicate native work: Add=%d login=%d search=%d", adds.Load(), logins.Load(), sourceSearches.Load())
	}
	qbMu.Lock()
	actualPath, actualTags := savedPath, savedTags
	qbMu.Unlock()
	if actualPath != rootPath || !strings.Contains(actualTags, receipt.OperationID.String()) {
		t.Fatal("native Add did not use explicitly selected root and durable operation")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3`, receipt.OperationID, receipt.UnitID, rootID) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND candidate_id=$2 AND root_id=$3`, receipt.OperationID, candidateID, rootID) != 1 {
		t.Fatal("durable protection/demand graph does not bind selected receipt")
	}
	// The two real health probes use legacy search rows with empty keywords.
	// Neither has candidate links: acquisition evidence comes only from interactive.
	for query, want := range map[string]int{
		`SELECT count(*) FROM indexer_query`:                                                                                         3,
		`SELECT count(*) FROM indexer_query WHERE query_type='interactive' AND state='succeeded'`:                                    1,
		`SELECT count(*) FROM indexer_query WHERE query_type='search' AND keyword=''`:                                                2,
		`SELECT count(*) FROM indexer_query_candidate`:                                                                               1,
		`SELECT count(*) FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id WHERE q.query_type<>'interactive'`: 0,
		`SELECT count(*) FROM release_candidate`:                                                                                     1,
		`SELECT count(*) FROM job WHERE kind='interactive_search_source' AND state='succeeded'`:                                      1,
	} {
		if n := m5InteractiveInt(t, f, query); n != want {
			t.Fatalf("unexpected final provenance count %d, want %d: %s", n, want, query)
		}
	}
	// History cannot restore authority to a currently disabled account.
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE username='m5-acquisition-admin'`)
	denied := f.request(t, http.MethodPost, acquisitionPath, "m5-interactive-acquire", command, f.session)
	m5IndexerStatus(t, denied, http.StatusUnauthorized)
	assertWork(1)
	if adds.Load() != 1 || metadataCalls.Load() != callsBeforeRecovery {
		t.Fatal("disabled-account recovery performed work")
	}
	t.Logf("accepted receipt=202 recovered_original=2 queries=3 native_probe_queries=2 interactive_queries=1 candidates=1 submission_jobs=1 succeeded_jobs=1 download_tasks=1 tracking_tasks=1 unit_placeholders=1 active_acquisition_protections=1 requirements=1 success_audits=1 acquisition_receipts=1 received_native_Add=1")
	stop()
}
