package app

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/adapter/tmdbartwork"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/mediasidecar"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only this first journey is production ingestion. The subsequent SQL-prepared
// imported facts exercise repository + native filesystem + native HTTP adapters.
func TestM7SidecarAutomaticTVAppBrowser(t *testing.T) {
	runM6Journey(t, func(h *m6Journey) {
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
		defer cancel()
		before, err := h.snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pool := h.application.DBPool()
		var jobID, eventID, fileID, unitID uuid.UUID
		var relative, state string
		var payload, raw []byte
		for {
			err = pool.QueryRow(ctx, `SELECT j.id,e.id,f.id,u.id,f.rel_path,j.state,j.payload,j.result
FROM outbox_event e
JOIN library_file f ON f.id=(e.payload->>'file_id')::uuid
JOIN media_unit u ON u.id=(e.payload->>'unit_id')::uuid
JOIN job j ON j.kind='media_sidecar' AND j.payload->>'source_event_id'=e.id::text
WHERE e.event_type='media.imported' AND f.root_id=$1`, h.root).Scan(&jobID, &eventID, &fileID, &unitID, &relative, &state, &payload, &raw)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("sidecar terminal SELECT: %v; %s", err, m7SidecarDiagnostic(t, h))
			}
			if err == nil && (state == "succeeded" || state == "dead" || state == "failed") {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("sidecar deadline state=%q: %s", state, m7SidecarDiagnostic(t, h))
			case <-time.After(50 * time.Millisecond):
			}
		}
		if state != "succeeded" {
			t.Fatalf("App sidecar state=%s: %s", state, m7SidecarDiagnostic(t, h))
		}
		var exact, oneJob, noRecipients bool
		err = pool.QueryRow(ctx, `SELECT
$2::jsonb=jsonb_build_object('version',1,'source_event_id',$3::uuid,'file_id',$4::uuid,'unit_id',$5::uuid),
(SELECT count(*)=1 FROM job WHERE kind='media_sidecar'),
NOT EXISTS(SELECT 1 FROM media_server) AND NOT EXISTS(SELECT 1 FROM notification_channel)
FROM job WHERE id=$1 AND max_attempts=1 AND attempts=1 AND dedupe_key='media-sidecar:'||$3::uuid::text
AND lease_owner IS NULL AND lease_until IS NULL AND finished_at IS NOT NULL`, jobID, payload, eventID, fileID, unitID).Scan(&exact, &oneJob, &noRecipients)
		if err != nil || !exact || !oneJob || !noRecipients {
			t.Fatalf("exact importing event/job with zero recipients: exact=%t one=%t zero=%t err=%v", exact, oneJob, noRecipients, err)
		}
		show := naming.FormatMovieDir(m6JourneyTitle, 2020)
		if filepath.Dir(relative) != filepath.Join(show, "Season 01") {
			t.Fatalf("production TV layout=%q want canonical show/Season 01; %s", relative, m7SidecarDiagnostic(t, h))
		}
		nfo := filepath.Join(h.rootPath, show, "tvshow.nfo")
		m7AssertNFO(t, nfo, "tvshow", m6JourneyTitle, 2020, 550)
		m7AssertArtifacts(t, raw, h.rootPath, show, map[string]ports.SidecarDisposition{"tvshow.nfo": ports.SidecarCreated})
		if _, err = os.Lstat(filepath.Join(h.rootPath, show, "poster.jpg")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("NFO-only journey claimed poster: %v", err)
		}
		for _, dir := range []string{".", filepath.Join(show, "Season 01"), m6JourneyRelease} {
			if _, err = os.Lstat(filepath.Join(h.rootPath, dir, "tvshow.nfo")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("TV NFO outside canonical show: %s: %v", dir, err)
			}
		}
		after, err := h.snapshot(ctx)
		if err != nil || before != after {
			t.Fatalf("sidecar changed production import completion graph: %v", err)
		}
		h.assertImported(t)
		t.Log("M7_SIDECAR_PRODUCTION_AUTOTV_APP_NFO_PASS poster=unavailable source=production_automatic_tv_finalizer")
	})
}

func TestM7SidecarPreparedRuntimeHTTPPG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		for _, scenario := range []string{"jpeg", "preserve_user_bytes", "artwork_network_failure", "metadata_network_failure", "unsafe_existing", "commit_receipt_loss"} {
			t.Run("SQLPrepared/"+kind+"/"+scenario, func(t *testing.T) { m7PreparedSidecar(t, kind, scenario) })
		}
	}
	if !t.Failed() {
		t.Log("M7_SIDECAR_SQL_PREPARED_BOUNDARY_PASS production_ingestion=false")
	}
}

func m7SidecarDiagnostic(t *testing.T, h *m6Journey) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
	defer cancel()
	var out string
	err := h.application.DBPool().QueryRow(ctx, `SELECT jsonb_build_object(
 'root_generation',r.generation,'health',r.health,'baseline',r.baseline_completed,'marker',r.marker_filename,
 'baseline_identity',(SELECT jsonb_build_object('device',s.root_device,'inode',s.root_inode) FROM scan_finalization s JOIN scan_run sr ON sr.id=s.scan_run_id WHERE s.root_id=r.id AND sr.generation=r.generation AND sr.state='succeeded' AND s.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1),
 'files',(SELECT jsonb_agg(jsonb_build_object('path',f.rel_path,'fingerprint',f.file_fingerprint,'size',f.size_bytes,'mtime',f.mtime_ns)) FROM library_file f WHERE f.root_id=r.id AND f.retired_at IS NULL),
 'jobs',(SELECT jsonb_agg(jsonb_build_object('state',j.state,'error',j.error,'attempts',j.attempts,'result',j.result)) FROM job j WHERE kind='media_sidecar'))::text FROM library_root r WHERE r.id=$1`, h.root).Scan(&out)
	if err != nil {
		return fmt.Sprintf("diagnostic unavailable: %v", err)
	}
	return out
}

func m7AssertNFO(t *testing.T, path, root, title string, year int, id int64) {
	t.Helper()
	raw := m7ReadRegular(t, path, 0644)
	var got struct {
		XMLName xml.Name
		Title   string `xml:"title"`
		Year    int    `xml:"year"`
		IDs     []struct {
			Type    string `xml:"type,attr"`
			Default string `xml:"default,attr"`
			Value   string `xml:",chardata"`
		} `xml:"uniqueid"`
	}
	if err := xml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("NFO XML: %v", err)
	}
	if got.XMLName.Local != root || got.Title != title || got.Year != year || len(got.IDs) != 1 || got.IDs[0].Type != "tmdb" || got.IDs[0].Default != "true" || got.IDs[0].Value != fmt.Sprint(id) {
		t.Fatalf("canonical NFO identity: %+v", got)
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok {
			switch strings.ToLower(start.Name.Local) {
			case "episodedetails", "season", "episode", "showid", "episodeid":
				t.Fatalf("episode identity leaked into work NFO: %s", start.Name.Local)
			}
		}
	}
}

func m7ReadRegular(t *testing.T, path string, mode os.FileMode) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		t.Fatalf("sidecar mode=%v want regular %04o: %s", info.Mode(), mode, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func m7AssertArtifacts(t *testing.T, raw []byte, root, dir string, want map[string]ports.SidecarDisposition) {
	t.Helper()
	var got ports.MediaSidecarCompletion
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("sidecar receipt: %v", err)
	}
	if len(got.Artifacts) != len(want) {
		t.Fatalf("receipt artifacts=%d want=%d", len(got.Artifacts), len(want))
	}
	seen := map[string]bool{}
	fs := rootfs.NewFilesystem()
	cap, err := fs.OpenRoot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	for _, artifact := range got.Artifacts {
		disposition, ok := want[artifact.Name]
		if !ok || seen[artifact.Name] || artifact.Result.Disposition != disposition || artifact.Result.Published != (disposition == ports.SidecarCreated) {
			t.Fatalf("untruthful artifact receipt: %+v", artifact)
		}
		seen[artifact.Name] = true
		if disposition == ports.SidecarUnsafeExisting {
			if artifact.Result.Identity != (ports.FileIdentity{}) {
				t.Fatal("unsafe entry claimed identity")
			}
			continue
		}
		actual, err := cap.Inspect(t.Context(), filepath.Join(dir, artifact.Name))
		if err != nil || actual != artifact.Result.Identity {
			t.Fatalf("artifact receipt != actual inode: %s: %v", artifact.Name, err)
		}
	}
}

type m7SidecarRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r m7SidecarRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	metadata := req.URL.Host == "api.themoviedb.org" && strings.HasPrefix(req.URL.Path, "/3/")
	artwork := req.URL.Host == "image.tmdb.org" && req.URL.Path == "/t/p/w342/Sidecar.jpg"
	if req.URL.Scheme != "https" || (!metadata && !artwork) {
		return nil, errors.New("unexpected sidecar fixture destination")
	}
	if req.URL.Host == "image.tmdb.org" && (req.URL.RawQuery != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "") {
		return nil, errors.New("artwork credential leak")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &u, r.target.Host
	return r.transport.RoundTrip(clone)
}

type m7LostSidecarReceipt struct{ ports.MediaSidecarRepository }

func (r m7LostSidecarReceipt) Complete(context.Context, ports.JobLease, ports.MediaSidecarSnapshot, ports.MediaSidecarCompletion) error {
	return ports.ErrUnavailable
}

// No App ingestion is claimed here: SQL prepares the imported fact and baseline,
// while every file identity and every sidecar byte comes from the native runtime.
func m7PreparedSidecar(t *testing.T, kind, scenario string) {
	t.Helper()
	if !strings.HasPrefix(filepath.Clean(os.TempDir()), "/data/temp/") {
		t.Fatal("owned /data/temp required")
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(t.Context(), dsn, "../../migrations")
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatalf("schema93 required: %d %v", version, err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	title := "Sidecar & 原生"
	dir := naming.FormatMovieDir(title, 2024)
	relative := filepath.Join(dir, "source.mkv")
	nfo := "movie.nfo"
	key := "movie"
	var season, episode any
	if kind == "tv" {
		relative = filepath.Join(dir, "Season 01", "source.mkv")
		nfo = "tvshow.nfo"
		key = "S01E03"
		season = int32(1)
		episode = int32(3)
	}
	if err = os.MkdirAll(filepath.Join(root, filepath.Dir(relative)), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "download.mkv")
	original := []byte("owned immutable source and hardlink\n")
	if err = os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(source, filepath.Join(root, relative)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("owned marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fs := rootfs.NewFilesystem()
	cap, err := fs.OpenRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	rootID, err := cap.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fileID, err := cap.Inspect(ctx, relative)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, unitID, rootUUID, fileUUID, eventID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	lease := ports.JobLease{ID: uuid.New(), Owner: "m7-owned-runtime", Token: 1}
	exec(`INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,$2,$2,2024,$3)`, mediaID, title, kind)
	exec(`INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,'6211')`, uuid.New(), mediaID, "tmdb."+kind)
	exec(`INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,$5)`, unitID, mediaID, key, season, episode)
	exec(`INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,$3,'M7 SQL-prepared runtime',true)`, rootUUID, root, kind)
	exec(`INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,$4,$5,$6)`, fileUUID, rootUUID, relative, fileID.Size, fileID.Mtime, fmt.Sprintf("stat.v1:%d:%d:%d", fileID.Inode, fileID.Size, fileID.Mtime))
	exec(`INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, fileUUID, unitID)
	exec(`INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), unitID, rootUUID, fileUUID)
	exec(`INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) VALUES($1,$2,1,1,0,'succeeded',clock_timestamp())`, runID, rootUUID)
	exec(`INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, runID, rootUUID, rootID.Device, rootID.Inode)
	payload, _ := json.Marshal(ports.MediaSidecarPayload{Version: 1, SourceEventID: eventID, FileID: fileUUID, UnitID: unitID})
	exec(`INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'media',$2,'media.imported',$3,1)`, eventID, unitID.String(), payload)
	exec(`INSERT INTO job(id,kind,state,payload,max_attempts,attempts,lease_owner,lease_token,lease_until) VALUES($1,'media_sidecar','running',$2,1,1,$3,$4,clock_timestamp()+interval '2 minutes')`, lease.ID, payload, lease.Owner, lease.Token)
	exec(`INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb','tmdb.'||media_type,'6211',metadata_version,'/Sidecar.jpg' FROM media WHERE id=$1`, mediaID)
	before := m7ProtectedSidecarGraph(t, pool)
	var jpegBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 220, G: 80, B: 30, A: 255})
	if err = jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	var metadataCalls, artCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("sidecar fixture attempted mutation")
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/t/p/w342/Sidecar.jpg" {
			artCalls.Add(1)
			if scenario == "artwork_network_failure" {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes.Bytes())
			return
		}
		metadataCalls.Add(1)
		if r.URL.Path != "/3/"+kind+"/6211" || r.URL.Query().Get("api_key") != "owned-sidecar-key" || r.URL.Query().Get("language") != "zh-CN" {
			t.Error("unexpected bound metadata request")
			w.WriteHeader(400)
			return
		}
		if scenario == "metadata_network_failure" {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":6211,"title":"Sidecar","name":"Sidecar","original_title":"Sidecar","original_name":"Sidecar","overview":"Owned metadata","release_date":"2024-01-01","first_air_date":"2024-01-01","poster_path":"/Sidecar.jpg","seasons":[]}`)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: m7SidecarRoute{target, transport}, Timeout: 2 * time.Second}
	sharedBudget := budget.NewMetadataBudget(nil)
	metadata := tmdb.NewClient("owned-sidecar-key", tmdb.WithHTTPClient(client), tmdb.WithRequestBudget(sharedBudget))
	artwork := tmdbartwork.New(client, sharedBudget)
	repo := mediasidecar.NewRepository(pool, idgen.New())
	var activeRepo ports.MediaSidecarRepository = repo
	if scenario == "commit_receipt_loss" {
		activeRepo = m7LostSidecarReceipt{repo}
	}
	nfoPath, posterPath := filepath.Join(root, dir, nfo), filepath.Join(root, dir, "poster.jpg")
	userBytes := []byte("user supplied bytes must never be claimed or replaced\n")
	if scenario == "preserve_user_bytes" {
		for _, path := range []string{nfoPath, posterPath} {
			if err = os.WriteFile(path, userBytes, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if scenario == "unsafe_existing" {
		if err = os.Symlink(source, nfoPath); err != nil {
			t.Fatal(err)
		}
	}
	svc := metadatasidecar.NewService(activeRepo, fs, metadata, artwork)
	err = svc.Execute(ctx, lease)
	want := map[string]ports.SidecarDisposition{nfo: ports.SidecarCreated, "poster.jpg": ports.SidecarCreated}
	switch scenario {
	case "unsafe_existing":
		if err == nil {
			t.Fatal("unsafe existing entry accepted")
		}
		link, e := os.Readlink(nfoPath)
		if e != nil || link != source {
			t.Fatalf("unsafe entry changed: %v", e)
		}
		want = map[string]ports.SidecarDisposition{nfo: ports.SidecarUnsafeExisting}
	case "commit_receipt_loss":
		if !errors.Is(err, ports.ErrUnavailable) {
			t.Fatalf("lost receipt: %v", err)
		}
		m7AssertNFO(t, nfoPath, map[string]string{"movie": "movie", "tv": "tvshow"}[kind], title, 2024, 6211)
		publishedNFO, publishedPoster := m7ReadRegular(t, nfoPath, 0644), m7ReadRegular(t, posterPath, 0644)
		var uncommitted bool
		if err = pool.QueryRow(ctx, `SELECT state='running' AND result IS NULL AND NOT EXISTS(SELECT 1 FROM audit_log WHERE action='media.sidecar.completed') FROM job WHERE id=$1`, lease.ID).Scan(&uncommitted); err != nil || !uncommitted {
			t.Fatalf("receipt loss falsely committed: %v", err)
		}
		exec(`UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
		n, e := db.New(pool).RecoverExpiredMediaSidecarJobs(ctx)
		if e != nil || n != 1 {
			t.Fatalf("recovery: n=%d err=%v", n, e)
		}
		m7AssertSidecarState(t, pool, lease.ID, "dead", true)
		if err = metadatasidecar.NewService(repo, fs, metadata, artwork).Execute(ctx, lease); !errors.Is(err, ports.ErrMediaSidecarStale) {
			t.Fatalf("dead job replay: %v", err)
		}
		// Explicit operator observation is a new job; it must preserve, not claim,
		// the files published before the lost database receipt.
		previous := lease.ID
		lease.ID = uuid.New()
		exec(`INSERT INTO job(id,kind,state,payload,max_attempts,attempts,lease_owner,lease_token,lease_until) SELECT $1,kind,'running',payload,1,1,$3,$4,clock_timestamp()+interval '2 minutes' FROM job WHERE id=$2`, lease.ID, previous, lease.Owner, lease.Token)
		if err = metadatasidecar.NewService(repo, fs, metadata, artwork).Execute(ctx, lease); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(publishedNFO, m7ReadRegular(t, nfoPath, 0644)) || !bytes.Equal(publishedPoster, m7ReadRegular(t, posterPath, 0644)) {
			t.Fatal("recovery changed published bytes")
		}
		want = map[string]ports.SidecarDisposition{nfo: ports.SidecarPreservedRegular, "poster.jpg": ports.SidecarPreservedRegular}
	default:
		if err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "unsafe_existing" {
		m7AssertSidecarState(t, pool, lease.ID, "dead", true)
	} else {
		m7AssertSidecarState(t, pool, lease.ID, "succeeded", false)
	}
	if scenario == "preserve_user_bytes" {
		for _, path := range []string{nfoPath, posterPath} {
			if !bytes.Equal(m7ReadRegular(t, path, 0600), userBytes) {
				t.Fatal("user sidecar overwritten")
			}
		}
		want = map[string]ports.SidecarDisposition{nfo: ports.SidecarPreservedRegular, "poster.jpg": ports.SidecarPreservedRegular}
	} else if scenario != "unsafe_existing" {
		m7AssertNFO(t, nfoPath, map[string]string{"movie": "movie", "tv": "tvshow"}[kind], title, 2024, 6211)
	}
	if scenario == "artwork_network_failure" || scenario == "metadata_network_failure" {
		want = map[string]ports.SidecarDisposition{nfo: ports.SidecarCreated}
		if _, err = os.Lstat(posterPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("network failure claimed poster: %v", err)
		}
	} else if scenario != "preserve_user_bytes" && scenario != "unsafe_existing" {
		poster := m7ReadRegular(t, posterPath, 0644)
		decoded, e := jpeg.Decode(bytes.NewReader(poster))
		if e != nil || decoded.Bounds() != image.Rect(0, 0, 3, 2) {
			t.Fatalf("actual JPEG dimensions: %v", e)
		}
	}
	if scenario == "unsafe_existing" {
		if _, err = os.Lstat(posterPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe NFO continued publication: %v", err)
		}
	}
	var receipt []byte
	if err = pool.QueryRow(ctx, `SELECT result FROM job WHERE id=$1`, lease.ID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	m7AssertArtifacts(t, receipt, root, dir, want)
	if metadataCalls.Load() < 1 || (scenario != "metadata_network_failure" && artCalls.Load() < 1) || (scenario == "metadata_network_failure" && artCalls.Load() != 0) {
		t.Fatal("network boundary not exercised")
	}
	for _, path := range []string{source, filepath.Join(root, relative)} {
		if !bytes.Equal(m7ReadRegular(t, path, 0600), original) {
			t.Fatal("sidecar changed video source")
		}
	}
	a, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(root, relative))
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("sidecar broke hardlink")
	}
	currentIdentity, err := cap.Inspect(ctx, relative)
	if err != nil || currentIdentity != fileID {
		t.Fatalf("sidecar changed source identity: %v", err)
	}
	if before != m7ProtectedSidecarGraph(t, pool) {
		t.Fatal("sidecar changed protected imported graph")
	}
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pulsemedia-sidecar-") {
			t.Fatal("staging file leaked")
		}
	}
}

func m7AssertSidecarState(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, want string, review bool) {
	t.Helper()
	var state string
	var gotReview, finished bool
	err := pool.QueryRow(t.Context(), `SELECT state,coalesce(error='review_required',false),lease_owner IS NULL AND lease_until IS NULL AND finished_at IS NOT NULL FROM job WHERE id=$1`, id).Scan(&state, &gotReview, &finished)
	if err != nil || state != want || gotReview != review || !finished {
		t.Fatalf("sidecar terminal state=%s review=%t finished=%t want=%s/%t: %v", state, gotReview, finished, want, review, err)
	}
}

func m7ProtectedSidecarGraph(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var graph strings.Builder
	for _, table := range []string{"media", "media_identity", "media_unit", "library_root", "library_file", "library_file_unit", "unit_fact", "scan_run", "scan_finalization", "media_artwork", "outbox_event", "outbox_delivery", "unit_reservation", "acquisition_reservation", "download_requirement", "automatic_tv_observation_result", "idempotency_record"} {
		var value string
		err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{table}.Sanitize()+` t) s`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		graph.WriteString(table)
		graph.WriteString(value)
	}
	return graph.String()
}
