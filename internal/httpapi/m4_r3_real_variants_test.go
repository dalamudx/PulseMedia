package httpapi_test

// Explicit real-service evidence for the two dedicated qB byte capabilities.
// The proxy forwards native requests unchanged; it only withholds acknowledgements.
import (
	"bytes"
	"context"
	"crypto/rand"
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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func m4VariantRoot(t *testing.T) string {
	t.Helper()
	// Both gates precede DB construction, filesystem probes, and service access.
	if os.Getenv("PULSE_M4_REAL_VARIANT_PERMITS") != "1" || os.Getenv("PULSE_M4_SERVICES_DIR") == "" {
		t.Skip("requires PULSE_M4_REAL_VARIANT_PERMITS=1 and explicit owned M4 services")
	}
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != root || filepath.Dir(root) != "/data/temp" || !strings.HasPrefix(filepath.Base(root), "pulse-m4-services.") {
		t.Fatal("invalid owned service directory")
	}
	cmd := exec.Command("podman", "inspect", "--format", "{{index .Config.Labels \"io.pulsemedia.state\"}}|{{range .Mounts}}{{.Source}}:{{.Destination}}|{{end}}", "pulsemedia-m4-qbittorrent")
	out, err := cmd.Output()
	if err != nil || !strings.HasPrefix(string(out), root+"|") || !strings.Contains(string(out), root+"/downloads:/downloads|") {
		t.Fatal("qB owner or downloads bind mismatch")
	}
	return root
}

type m4Variant struct {
	t                     *testing.T
	raw                   []byte
	hash, local, save     string
	names                 []string
	sizes                 []int64
	direct                *qbittorrent.Client
	server                *httptest.Server
	transport             *http.Transport
	adds, accepted, reads atomic.Int32
	entered, release      chan struct{}
	once                  sync.Once
	held                  bool
	beforeAdd             func() error
	beforeInfo            func() error
	observationPermits    atomic.Int32
}

func newM4Variant(t *testing.T, root string, tv, held bool) *m4Variant {
	t.Helper()
	g := &m4Variant{t: t, entered: make(chan struct{}), release: make(chan struct{}), held: held, transport: http.DefaultTransport.(*http.Transport).Clone()}
	var err error
	g.local, err = os.MkdirTemp(filepath.Join(root, "downloads"), "m4-variant.")
	if err != nil {
		t.Fatal(err)
	}
	g.save = "/downloads/" + filepath.Base(g.local)
	// Register immediately; task removal precedes deletion of only our validated directory.
	t.Cleanup(func() {
		g.unblock()
		if g.server != nil {
			g.server.Close()
		}
		g.transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if g.direct != nil && g.hash != "" {
			_, e := g.direct.GetTorrent(ctx, g.hash)
			if e == nil {
				if e = g.direct.RemoveTorrent(ctx, g.hash, false); e != nil {
					t.Error("owned qB removal failed", e)
					return
				}
				m4VariantWait(t, 10*time.Second, func() bool { _, e = g.direct.GetTorrent(ctx, g.hash); return errors.Is(e, ports.ErrNotFound) })
			} else if !errors.Is(e, ports.ErrNotFound) {
				t.Error("owned qB cleanup observation failed", e)
				return
			}
		}
		if filepath.Dir(g.local) != filepath.Join(root, "downloads") || !strings.HasPrefix(filepath.Base(g.local), "m4-variant.") {
			t.Error("cleanup path ownership mismatch")
			return
		}
		if err := os.RemoveAll(g.local); err != nil {
			t.Error(err)
			return
		}
		if _, err := os.Lstat(g.local); !os.IsNotExist(err) {
			t.Error("owned payload cleanup unconfirmed")
			return
		}
		t.Log("M4_VARIANT_CLEANUP_VERIFIED owned_task_absent=true owned_payload_absent=true proxy_joined=true")
	})
	if err = os.Chmod(g.local, 0755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err = os.Chown(g.local, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	g.names = []string{"Fight.Club.1999.2160p.Bluray.x265.mkv"}
	g.sizes = []int64{120}
	if tv {
		g.names = []string{"Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv"}
		g.sizes = []int64{512, 512}
	}
	var content []byte
	for i, name := range g.names {
		b := make([]byte, g.sizes[i])
		if _, err = rand.Read(b); err != nil {
			t.Fatal(err)
		}
		content = append(content, b...)
		p := filepath.Join(g.local, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, b, 0644); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(p, 0644); err != nil {
			t.Fatal(err)
		}
	}
	piece := sha1.Sum(content)
	var info []byte
	if tv {
		info = []byte("d5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveee4:name4:Show12:piece lengthi16384e6:pieces20:")
	} else {
		name := g.names[0]
		info = []byte(fmt.Sprintf("d6:lengthi120e4:name%d:%s12:piece lengthi16384e6:pieces20:", len(name), name))
	}
	info = append(info, piece[:]...)
	info = append(info, []byte("7:privatei1ee")...)
	hash := sha1.Sum(info)
	g.hash = hex.EncodeToString(hash[:])
	g.raw = append([]byte("d4:info"), info...)
	g.raw = append(g.raw, 'e')
	// Owned qB has local authentication bypass. Native client confirms its 204 login
	// through a protected endpoint; no protected config or secret file is read.
	g.direct, err = qbittorrent.NewClient("http://127.0.0.1:18085", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = g.direct.Test(ctx); err != nil {
		t.Fatal("real qB protected API probe failed", err)
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}
func (g *m4Variant) unblock() { g.once.Do(func() { close(g.release) }) }
func (g *m4Variant) serve(w http.ResponseWriter, r *http.Request) {
	add := r.URL.Path == "/api/v2/torrents/add"
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "body unavailable", 400)
		return
	}
	if add {
		if g.adds.Add(1) != 1 {
			g.t.Error("second native Add attempted")
			http.Error(w, "duplicate blocked", http.StatusConflict)
			return
		}
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(bytes.NewReader(raw))
		if err = clone.ParseMultipartForm(2 << 20); err != nil {
			g.t.Error("invalid native form")
			http.Error(w, "bad form", 400)
			return
		}
		defer func() { _ = clone.MultipartForm.RemoveAll() }()
		file, _, e := clone.FormFile("torrents")
		if e != nil {
			g.t.Error("missing native bytes")
			http.Error(w, "bad form", 400)
			return
		}
		b, e := io.ReadAll(file)
		_ = file.Close()
		if e != nil || !bytes.Equal(b, g.raw) || clone.FormValue("urls") != "" || clone.FormValue("savepath") != g.save || !strings.Contains(clone.FormValue("tags"), "pulsemedia:op:") {
			g.t.Error("native Add changed frozen bytes/path/tag")
			http.Error(w, "invalid binding", 400)
			return
		}
		if g.beforeAdd != nil {
			if err = g.beforeAdd(); err != nil {
				g.t.Error(err)
				http.Error(w, "missing persisted start", 500)
				return
			}
		}
	} else if strings.HasPrefix(r.URL.Path, "/api/v2/torrents/") {
		g.reads.Add(1)
		if r.URL.Path == "/api/v2/torrents/info" && g.beforeInfo != nil {
			if e := g.beforeInfo(); e != nil {
				g.t.Error(e)
				http.Error(w, "missing observation permit", 500)
				return
			}
		}
	}
	upstream := r.Clone(r.Context())
	target, _ := url.Parse("http://127.0.0.1:18085")
	upstream.URL.Scheme, upstream.URL.Host, upstream.Host = target.Scheme, target.Host, target.Host
	upstream.RequestURI = ""
	upstream.Body = io.NopCloser(bytes.NewReader(raw))
	upstream.GetBody = nil
	response, err := g.transport.RoundTrip(upstream)
	if err != nil {
		http.Error(w, "downstream unavailable", http.StatusBadGateway)
		return
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	_ = response.Body.Close()
	if err != nil {
		http.Error(w, "reply unavailable", http.StatusBadGateway)
		return
	}
	var ack struct {
		IDs     []string `json:"added_torrent_ids"`
		Failed  int      `json:"failure_count"`
		Pending int      `json:"pending_count"`
		Success int      `json:"success_count"`
	}
	_ = json.Unmarshal(b, &ack)
	confirmed := strings.TrimSpace(string(b)) == "Ok." || (ack.Success == 1 && ack.Failed == 0 && ack.Pending == 0 && len(ack.IDs) == 1 && strings.EqualFold(ack.IDs[0], g.hash))
	if add && response.StatusCode == 200 && confirmed {
		g.accepted.Add(1)
		close(g.entered)
		if g.held {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-g.release:
			case <-timer.C:
				g.t.Error("held real Add exceeded watchdog")
			}
		}
		conn, _, e := w.(http.Hijacker).Hijack()
		if e == nil {
			_ = conn.Close()
		}
		return
	}
	for k, v := range response.Header {
		for _, s := range v {
			w.Header().Add(k, s)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(b)
}
func m4VariantWait(t *testing.T, budget time.Duration, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("real variant condition timed out")
		case <-tick.C:
		}
	}
}
func (g *m4Variant) verify(t *testing.T, op uuid.UUID, targets ...media.MovieUpgradeTarget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var row *ports.TorrentInfo
	m4VariantWait(t, 8*time.Second, func() bool {
		var e error
		row, e = g.direct.GetTorrent(ctx, g.hash)
		return e == nil && row.Progress == 1
	})
	if row.InfoHash != g.hash || strings.TrimRight(row.SavePath, "/") != g.save || !strings.Contains(row.Tags, "pulsemedia:op:"+op.String()) {
		t.Fatal("real task hash/path/op mismatch")
	}
	files, err := g.direct.GetTorrentFiles(ctx, g.hash)
	if err != nil || len(files) != len(g.names) {
		t.Fatal("real complete file list mismatch", err)
	}
	for i, f := range files {
		if f.Name != g.names[i] || f.SizeBytes != g.sizes[i] || f.Progress != 1 {
			t.Fatalf("native file evidence mismatch at %d", i)
		}
	}
	exported, err := g.direct.CompleteTVTrackingObserver().GetTorrentMetainfo(ctx, g.hash)
	if err != nil {
		t.Fatal("real export failed", err)
	}
	// qB may add outer metadata. Validate original info identity using the real
	// inspector through a complete-TV proof (TV) or native task hash (movie).
	if len(g.names) == 2 {
		proof, e := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, exported)
		if e != nil || proof.InfoHash() != g.hash {
			t.Fatal("native exported info bytes mismatch", e)
		}
	}
	if len(targets) > 0 {
		p, e := torrent.Inspector{}.InspectMovieUpgradeMetainfo(ctx, exported, targets[0])
		if e != nil || p.InfoHash() != g.hash {
			t.Fatal("native movie export identity mismatch", e)
		}
	}
	if g.adds.Load() != 1 || g.accepted.Load() != 1 {
		t.Fatal("real Add counts", g.adds.Load(), g.accepted.Load())
	}
	t.Logf("M4_VARIANT_REAL_TASK infohash=%s files=%d complete=true op_tag=true exact_bytes=true forwarded_adds=1 accepted_adds=1", g.hash, len(files))
}

// This transport preserves the native client and its no-GetBody anti-replay
// contract while modeling an admitted synchronous HTTP call ignoring cancellation.
type m4VariantTransport struct{ base *http.Transport }

func (tr m4VariantTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/api/v2/torrents/add" {
		if r.GetBody != nil {
			return nil, errors.New("native Add unexpectedly replayable")
		}
		r = r.Clone(context.WithoutCancel(r.Context()))
	}
	return tr.base.RoundTrip(r)
}

func TestM4RealCompleteTVR3Permit(t *testing.T) {
	root := m4VariantRoot(t)
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost_ack_observe", true: "held_cancel_lock_loss"}[held], func(t *testing.T) {
			g := newM4Variant(t, root, true, held)
			key := bytes.Repeat([]byte{23}, 32)
			enc := func(s string) []byte {
				b, e := crypto.Encrypt(key, []byte(s))
				if e != nil {
					t.Fatal(e)
				}
				return b
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(g.raw) }))
			t.Cleanup(source.Close)
			f := newPrepareTVFixture(t, func(f *completeTVFixture) {
				f.exec(t, `UPDATE download_client SET url=$1,username='',password_encrypted=$2`, g.server.URL, enc(""))
				f.exec(t, `UPDATE library_root SET path=$1 WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`, g.save, f.f.sub.Id)
				f.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, f.f.sub.Id)
				f.exec(t, `UPDATE indexer SET url=$1,api_key_encrypted=$2`, source.URL, enc("variant-source"))
				f.exec(t, `UPDATE release_candidate SET info_hash=$1,link_encrypted=$2,source_config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`, g.hash, enc(source.URL+"/torrent"))
				f.exec(t, `UPDATE indexer_query SET config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`)
			})
			makeManager := func() *downloadstore.Manager {
				m := downloadstore.NewManager(f.f.a.DBPool(), idgen.New(), key)
				m.SetClientFactory(func(kind, endpoint, user, password string) (ports.DownloadClient, error) {
					return qbittorrent.NewClient(endpoint, user, password, &http.Client{Transport: m4VariantTransport{g.transport}, Timeout: 12 * time.Second})
				})
				repo := postgres.NewCompleteTVSubmissionStore(f.f.a.DBPool(), idgen.New())
				m.SetCompleteTVServices(repo, repo, torznab.CompleteTVFetcher{}, torrent.Inspector{})
				return m
			}
			observer := postgres.NewCompleteTVObservationStore(f.f.a.DBPool(), idgen.New())
			req := ports.CompleteTVObservationRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "real-variant-observer", Token: 7}, OperationID: f.c.OperationID}
			g.beforeAdd = func() error {
				var ok bool
				e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM complete_tv_physical_permit p JOIN download_task d USING(operation_id) JOIN job j ON j.id=$2 WHERE p.operation_id=$1 AND d.state='submitting' AND j.lease_owner=$3 AND j.lease_token=$4 AND j.state='running')`, f.c.OperationID, f.c.Lease.ID, f.c.Lease.Owner, f.c.Lease.Token).Scan(&ok)
				if e != nil || !ok {
					return errors.New("complete TV started/permit/job evidence missing")
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				r *ports.CompleteTVSubmissionResultReceipt
				e error
			}
			done := make(chan result, 1)
			joined := false
			go func() { r, e := makeManager().SendCompleteTV(ctx, f.c.Lease, f.c.OperationID); done <- result{r, e} }()
			defer func() {
				cancel()
				g.unblock()
				if !joined {
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						t.Error("complete TV owner not joined")
					}
				}
			}()
			select {
			case <-g.entered:
			case got := <-done:
				joined = true
				t.Fatal("complete TV returned before Add", got.r, got.e)
			case <-time.After(8 * time.Second):
				t.Fatal("no real complete TV acceptance")
			}
			if held {
				cancel()
				var killed bool
				if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT pg_terminate_backend(backend_pid) FROM complete_tv_physical_permit WHERE operation_id=$1`, f.c.OperationID).Scan(&killed); e != nil || !killed {
					t.Fatal("permit session not terminated", e)
				}
				reads := g.reads.Load()
				other := makeManager()
				if r, e := other.SendCompleteTV(context.Background(), f.c.Lease, f.c.OperationID); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
					t.Fatal("second sender admitted", r, e)
				}
				req.TaskID = uuid.New()
				if r, e := other.ObserveCompleteTV(context.Background(), observer, req); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
					t.Fatal("second observer admitted", r, e)
				}
				if g.reads.Load() != reads || g.adds.Load() != 1 {
					t.Fatal("second manager performed HTTP")
				}
				select {
				case <-done:
					joined = true
					t.Fatal("owner returned before physical reply")
				default:
				}
				var permits int
				if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit WHERE operation_id=$1`, f.c.OperationID).Scan(&permits); e != nil || permits != 1 {
					t.Fatal("old physical permit released", e)
				}
				g.unblock()
			}
			select {
			case got := <-done:
				joined = true
				if got.e != nil || got.r == nil || got.r.State != "unknown" {
					t.Fatal("unknown return", got.r, got.e)
				}
				req.TaskID = got.r.TaskID
			case <-time.After(15 * time.Second):
				t.Fatal("complete TV return timeout")
			}
			var permits int
			if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit WHERE operation_id=$1`, f.c.OperationID).Scan(&permits); e != nil || permits != 0 {
				t.Fatal("owner did not retire returned permit", e)
			}
			f.exec(t, `UPDATE job SET lease_owner='retired-real-sender',lease_token=999,lease_until=clock_timestamp()-interval '1 hour' WHERE id=$1`, f.c.Lease.ID)
			f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, req.Lease.ID, ports.CompleteTVObservationJobKind, req.TaskID.String(), req.OperationID.String(), req.Lease.Owner, req.Lease.Token)
			r, e := makeManager().ObserveCompleteTV(context.Background(), observer, req)
			if e != nil || r == nil || r.State != "tracking" {
				t.Fatal("real independent recovery", r, e)
			}
			var ok bool
			e = f.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)=2 AND (SELECT count(*) FROM unit_reservation WHERE state='dispatched')=2 AND (SELECT count(*) FROM download_requirement WHERE state='pending')=2 AND (SELECT count(*) FROM complete_tv_submission_unit)=2 AND (SELECT count(*) FROM complete_tv_physical_permit)=0 AND (SELECT count(*) FROM complete_tv_submission_preparation)=1 AND (SELECT count(*) FROM download_task)=1 AND (SELECT state='succeeded' AND lease_token=7 FROM job WHERE id=$1)`, req.Lease.ID).Scan(&ok)
			if e != nil || !ok {
				t.Fatal("complete TV protection/history/commit invariant", e)
			}
			g.verify(t, f.c.OperationID)
			t.Logf("M4_COMPLETE_TV_R3_PERMIT_PASS held_cancel_lock_loss=%t started=true external_action=true unknown_validated=true commit=true independent_observation=true two_units_protected=true", held)
		})
	}
}

func TestM4RealUpgradeR3Permit(t *testing.T) {
	root := m4VariantRoot(t)
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost_ack_app_observe", true: "held_lock_loss_fenced"}[held], func(t *testing.T) {
			g := newM4Variant(t, root, false, held)
			var stop func()
			f := newPublicPreparationFixture(t, func(f *publicPreparationFixture) {
				f.raw = append([]byte(nil), g.raw...)
				master, e := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
				if e != nil {
					t.Fatal(e)
				}
				encrypted, e := crypto.Encrypt(master, []byte(""))
				if e != nil {
					t.Fatal(e)
				}
				f.exec(t, `UPDATE download_client SET url=$2,username='',password_encrypted=$3 WHERE id=$1`, f.client, g.server.URL, encrypted)
				f.exec(t, `UPDATE library_root SET path=$2 WHERE id=$1`, f.root, g.save)
				f.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.root)
				f.project(t)
				f.exec(t, `UPDATE release_candidate SET info_hash=$2 WHERE id=$1`, f.request.CandidateID, g.hash)
				stop = publicUpgradeAppStarter(t, f.publicUpgradeFixture)()
			})
			defer func() { g.unblock(); stop() }()
			body := publicSendBody(t, f)
			original := f.unchangedFiles(t)
			var protectedBefore []byte
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT pulse_upgrade_observation_protected_digest($1)`, f.operation).Scan(&protectedBefore); e != nil {
				t.Fatal(e)
			}
			var task uuid.UUID
			var targetRaw []byte
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT task_id,evidence->'proof'->'target' FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&task, &targetRaw); e != nil {
				t.Fatal(e)
			}
			var target media.MovieUpgradeTarget
			if e := json.Unmarshal(targetRaw, &target); e != nil {
				t.Fatal(e)
			}
			proof, e := torrent.Inspector{}.InspectMovieUpgradeMetainfo(context.Background(), g.raw, target)
			if e != nil {
				t.Fatal(e)
			}
			g.beforeAdd = func() error {
				var ok bool
				e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization a JOIN movie_upgrade_send_dispatch d USING(operation_id) JOIN movie_upgrade_send_job b USING(operation_id) JOIN job j ON j.id=b.job_id WHERE a.operation_id=$1 AND a.owner=d.owner AND a.token=d.token AND a.backend_pid=d.backend_pid AND a.status='armed_not_sent' AND j.state='running' AND j.lease_owner IS NOT NULL AND j.lease_token>0) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=$1)`, f.operation).Scan(&ok)
				if e != nil || !ok {
					return errors.New("upgrade arm/dispatch/job evidence missing")
				}
				return nil
			}
			g.beforeInfo = func() error {
				var ok bool
				e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit p USING(observation_id,operation_id) JOIN movie_upgrade_send_result s USING(operation_id) JOIN pg_stat_activity a ON a.pid=o.backend_pid AND a.backend_start=o.backend_start WHERE o.operation_id=$1 AND o.owner<>'00000000-0000-0000-0000-000000000000'::uuid AND o.token<>'00000000-0000-0000-0000-000000000000'::uuid AND o.lease_until>clock_timestamp() AND s.status='unknown' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_result r WHERE r.observation_id=o.observation_id))`, f.operation).Scan(&ok)
				if e != nil || !ok {
					return errors.New("upgrade native observation bypassed durable permit/owner/token/return")
				}
				g.observationPermits.Add(1)
				return nil
			}
			first := f.command("upgrade-sends", body, "real-variant-send", f.admin, nil)
			if first.Code != 202 {
				t.Fatalf("App send status %d", first.Code)
			}
			select {
			case <-g.entered:
			case <-time.After(8 * time.Second):
				t.Fatal("no real upgrade acceptance")
			}
			if held {
				var killed bool
				if e := f.a.DBPool().QueryRow(context.Background(), `SELECT pg_terminate_backend(backend_pid) FROM movie_upgrade_send_authorization WHERE operation_id=$1`, f.operation).Scan(&killed); e != nil || !killed {
					t.Fatal("upgrade original session not terminated", e)
				}
				master, e := crypto.EnsureMasterKey(f.a.Config().DataDir, f.a.Config().SecretKey)
				if e != nil {
					t.Fatal(e)
				}
				m := downloadstore.NewManager(f.a.DBPool(), idgen.New(), master)
				m.SetClientFactory(func(kind, endpoint, user, password string) (ports.DownloadClient, error) {
					return qbittorrent.NewClient(endpoint, user, password, nil)
				})
				store, e := postgres.NewUpgradePreparationStore(f.a.DBPool(), idgen.New(), torrent.Inspector{})
				if e != nil {
					t.Fatal(e)
				}
				issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
				driver, e := downloadstore.NewMovieUpgradeNativeSendDriver(m, issuer, clock.New())
				if e != nil {
					t.Fatal(e)
				}
				repo, e := postgres.NewMovieUpgradeNativeSendRepository(store, driver, issuer)
				if e != nil {
					t.Fatal(e)
				}
				reads := g.reads.Load()
				if r, e := repo.SendMovieUpgrade(context.Background(), f.actor, f.operation, uuid.New(), proof, "second-real-send"); r != nil || !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
					t.Fatal("second native upgrade admitted", r, e)
				}
				qi := postgres.NewMovieUpgradeNativeQueryVerifier()
				qd, e := downloadstore.NewMovieUpgradeNativeObservationDriver(m, qi, clock.New())
				if e != nil {
					t.Fatal(e)
				}
				qr, e := postgres.NewMovieUpgradeNativeObservationRepository(store, qd, qi)
				if e != nil {
					t.Fatal(e)
				}
				if r, e := qr.ObserveMovieUpgrade(context.Background(), f.actor, f.operation, uuid.New(), "second-real-admit", "second-real-result"); r != nil || e == nil {
					t.Fatal("unreturned upgrade observed", r, e)
				}
				if g.reads.Load() != reads || g.adds.Load() != 1 {
					t.Fatal("second upgrade execution performed HTTP")
				}
				var count int
				if e := f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_send_result WHERE operation_id=$1`, f.operation).Scan(&count); e != nil || count != 0 {
					t.Fatal("old upgrade returned early", e)
				}
				g.unblock()
				// The trusted return cannot be persisted after original session loss. Keep
				// authorization/dispatch and every old-file protection for manual review.
				m4VariantWait(t, 12*time.Second, func() bool {
					var yes bool
					_ = f.a.DBPool().QueryRow(context.Background(), `SELECT NOT EXISTS(SELECT 1 FROM job j JOIN movie_upgrade_send_job b ON b.job_id=j.id WHERE b.operation_id=$1 AND j.state='running')`, f.operation).Scan(&yes)
					return yes
				})
			} else {
				waitPublicUpgradeObservation(t, func() bool {
					var yes bool
					e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result s JOIN movie_upgrade_observation o USING(operation_id) JOIN movie_upgrade_observation_result r USING(observation_id) WHERE s.operation_id=$1 AND s.status='unknown' AND r.outcome='exact_match')`, f.operation).Scan(&yes)
					if e != nil {
						t.Fatal(e)
					}
					return yes
				})
			}
			replay := f.command("upgrade-sends", body, "real-variant-send", f.admin, nil)
			if replay.Code != 202 || replay.Body.String() != first.Body.String() {
				t.Fatal("App receipt replay changed")
			}
			stop()
			if f.unchangedFiles(t) != original {
				t.Fatal("old file/facts/link history changed")
			}
			var protectedAfter []byte
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT pulse_upgrade_observation_protected_digest($1)`, f.operation).Scan(&protectedAfter); e != nil || !bytes.Equal(protectedBefore, protectedAfter) {
				t.Fatal("old root/file/facts/reservations digest changed", e)
			}
			if !held {
				var terminal bool
				e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result r JOIN movie_upgrade_send_authorization a USING(operation_id) JOIN movie_upgrade_send_job b USING(operation_id) JOIN job j ON j.id=b.job_id WHERE r.operation_id=$1 AND r.owner=a.owner AND r.token=a.token AND r.status='unknown' AND r.arm_snapshot=to_jsonb(a) AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_send_job_terminal(j.id)) AND EXISTS(SELECT 1 FROM movie_upgrade_observation o JOIN movie_upgrade_observation_permit p USING(observation_id) JOIN movie_upgrade_observation_result r USING(observation_id) JOIN movie_upgrade_observation_job b USING(observation_id) JOIN job j ON j.id=b.job_id WHERE o.operation_id=$1 AND r.outcome='exact_match' AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_observation_terminal_job(o.observation_id))`, f.operation).Scan(&terminal)
				if e != nil || !terminal || g.observationPermits.Load() != 1 {
					t.Fatal("trusted return/observation terminal ledger mismatch", e)
				}
			} else if g.observationPermits.Load() != 0 {
				t.Fatal("missing trusted return admitted observation")
			}
			var auth, dispatch, results int
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_authorization WHERE operation_id=$1),(SELECT count(*) FROM movie_upgrade_send_dispatch WHERE operation_id=$1),(SELECT count(*) FROM movie_upgrade_send_result WHERE operation_id=$1)`, f.operation).Scan(&auth, &dispatch, &results); e != nil || auth != 1 || dispatch != 1 || results != map[bool]int{false: 1, true: 0}[held] {
				t.Fatal("upgrade immutable history invariant", auth, dispatch, results, e)
			}
			var protected bool
			if e := f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL) AND EXISTS(SELECT 1 FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched')) AND EXISTS(SELECT 1 FROM download_task WHERE id=$2 AND operation_id=$1 AND info_hash=$3 AND save_path=$4)`, f.operation, task, g.hash, g.save).Scan(&protected); e != nil || !protected {
				t.Fatal("upgrade root/unit/task protection missing", e)
			}
			g.verify(t, f.operation, target)
			t.Logf("M4_UPGRADE_R3_PERMIT_PASS held_lock_loss=%t arm=true dispatch=true physical_return_joined=true trusted_return_persisted=%t old_file_protected=true one_add=true", held, !held)
		})
	}
}
