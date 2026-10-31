package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

const transmissionTVRaw = "d4:infod5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveed6:lengthi0e4:pathl8:note.txteee4:name4:Show12:piece lengthi1024e6:pieces20:abcdefghijklmnopqrstee"

type transmissionTVFixture struct {
	*prepareTVFixture
	manager                                                    *download.Manager
	key                                                        []byte
	hash, save                                                 string
	adds, gets, inventories, fetches, rejected                 atomic.Int32
	present, lost, partial, wrongFiles, conflict, changedAfter atomic.Bool
	hook                                                       atomic.Pointer[func()]
}

func newTransmissionTVFixture(t *testing.T, configure ...func(*completeTVFixture)) *transmissionTVFixture {
	t.Helper()
	f := &transmissionTVFixture{key: bytes.Repeat([]byte{23}, 32)}
	proof, err := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, []byte(transmissionTVRaw))
	if err != nil {
		t.Fatal(err)
	}
	f.hash = proof.InfoHash()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		if r.URL.RawQuery != "passkey=private-link" {
			t.Error("wrong frozen source query")
		}
		_, _ = io.WriteString(w, transmissionTVRaw)
	}))
	t.Cleanup(source.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" || r.Method != "POST" {
			t.Error("non-native request")
			w.WriteHeader(400)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "native-user" || password != "native-password" {
			t.Error("wrong native credentials")
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			f.rejected.Add(1)
			w.Header().Set("X-Transmission-Session-Id", "tv-session")
			w.WriteHeader(409)
			return
		}
		var req struct {
			Method    string                     `json:"method"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		switch req.Method {
		case "torrent-add":
			f.adds.Add(1)
			var raw, save string
			var labels []string
			var paused bool
			_ = json.Unmarshal(req.Arguments["metainfo"], &raw)
			_ = json.Unmarshal(req.Arguments["download-dir"], &save)
			_ = json.Unmarshal(req.Arguments["labels"], &labels)
			_ = json.Unmarshal(req.Arguments["paused"], &paused)
			if len(req.Arguments) != 4 || raw != base64.StdEncoding.EncodeToString([]byte(transmissionTVRaw)) || save != f.save || strings.Join(labels, ",") != "pulsemedia,pulsemedia:op:"+f.c.OperationID.String() || paused {
				t.Error("original bytes/path/labels changed")
			}
			f.present.Store(true)
			if hook := f.hook.Load(); hook != nil {
				(*hook)()
			}
			if f.lost.Load() {
				c, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = c.Close()
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrent-added": map[string]any{"hashString": f.hash, "name": "Show", "id": 7}}, "tag": 1})
		case "torrent-get":
			listing := true
			if raw, ok := req.Arguments["ids"]; ok {
				listing = false
				f.inventories.Add(1)
				var ids []string
				_ = json.Unmarshal(raw, &ids)
				if len(ids) != 1 || ids[0] != f.hash {
					t.Error("wrong native file query")
				}
				if hook := f.hook.Load(); hook != nil {
					(*hook)()
				}
			} else {
				f.gets.Add(1)
			}
			rows := []any{}
			if f.present.Load() {
				hash := f.hash
				if f.changedAfter.Load() && listing && f.gets.Load()%2 == 0 {
					hash = strings.Repeat("f", 40)
				}
				done, status := int64(1024), 6
				if f.partial.Load() {
					done, status = 512, 4
				}
				row := map[string]any{"hashString": hash, "downloadDir": f.save, "labels": []string{"pulsemedia", "pulsemedia:op:" + f.c.OperationID.String()}, "status": status, "totalSize": 1024, "haveValid": done, "error": 0, "rateDownload": 123}
				if !listing {
					name := "Show/Show.S01E02.mkv"
					if f.wrongFiles.Load() {
						name = "Show/Show.S01E03.mkv"
					}
					row["files"] = []any{map[string]any{"name": "Show/Show.S01E01.mkv", "length": 512, "bytesCompleted": 512}, map[string]any{"name": name, "length": 512, "bytesCompleted": 512}, map[string]any{"name": "Show/note.txt", "length": 0, "bytesCompleted": 0}}
					row["fileStats"] = []any{map[string]any{"wanted": true, "bytesCompleted": 512}, map[string]any{"wanted": true, "bytesCompleted": 512}, map[string]any{"wanted": false, "bytesCompleted": 0}}
				}
				rows = append(rows, row)
				if f.conflict.Load() {
					other := map[string]any{}
					for k, v := range row {
						other[k] = v
					}
					other["hashString"] = strings.Repeat("a", 40)
					rows = append(rows, other)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": rows}, "tag": 1})
		default:
			t.Error("unexpected native method", req.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	enc := func(s string) []byte {
		b, e := crypto.Encrypt(f.key, []byte(s))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	f.prepareTVFixture = newTransmissionPublicPreparation(t, func(b *completeTVFixture) {
		b.exec(t, `UPDATE download_client SET client_type='transmission',url=$1,username='native-user',password_encrypted=$2`, server.URL, enc("native-password"))
		b.exec(t, `UPDATE indexer SET url=$1,api_key_encrypted=$2`, source.URL, enc("private-key"))
		b.exec(t, `UPDATE release_candidate SET info_hash=$1,link_encrypted=$2,source_config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`, f.hash, enc(source.URL+"/native?passkey=private-link"))
		b.exec(t, `UPDATE indexer_query SET config_version=(SELECT config_version FROM indexer WHERE id=indexer_id)`)
		for _, fn := range configure {
			fn(b)
		}
	})
	if e := f.f.a.DBPool().QueryRow(t.Context(), `SELECT path FROM library_root WHERE id=(SELECT root_id FROM subscription LIMIT 1)`).Scan(&f.save); e != nil {
		t.Fatal(e)
	}
	f.manager = download.NewManager(f.f.a.DBPool(), idgen.New(), f.key)
	f.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		if kind != "transmission" {
			return nil, ports.ErrUnavailable
		}
		return transmission.NewClient(url, user, password, nil)
	})
	repo := postgres.NewCompleteTVSubmissionStore(f.f.a.DBPool(), idgen.New())
	f.manager.SetCompleteTVServices(repo, repo, torznab.CompleteTVFetcher{}, torrent.Inspector{})
	return f
}
func newTransmissionPublicPreparation(t *testing.T, configure func(*completeTVFixture)) *prepareTVFixture {
	t.Helper()
	f := newCompleteTVFixture(t)
	configure(f)
	view := f.preview(t)
	review, err := f.svc.Review(t.Context(), f.actor, f.coverageFixture.command(view), "transmission-public-review")
	if err != nil {
		t.Fatal(err)
	}
	f.command = ports.AcquireCompleteTVCoverageCommand{SubscriptionID: f.f.sub.Id, ReviewID: review.ID, ExpectedInputStamp: view.InputStamp, ExpectedMaterialStamp: view.MaterialStamp, Confirmed: true}
	router := completePublicRouter(f.f.a, true)
	body := completePublicBody(f)
	completePublicProblem(t, completePublicCall(router, f, body, "transmission-anonymous", "", nil), http.StatusUnauthorized)
	completePublicProblem(t, completePublicCall(router, f, body, "transmission-member", f.f.member, nil), http.StatusForbidden)
	acquired := completePublicReceipt(t, completePublicCall(router, f, body, "transmission-public-acquire", f.f.admin, nil))
	recovered := completePublicReceipt(t, completePublicCall(router, f, body, "transmission-public-acquire", f.f.admin, nil))
	if recovered.OperationID != acquired.OperationID || recovered.JobID != acquired.JobID {
		t.Fatal("public acquisition repeated authority")
	}
	f.exec(t, `UPDATE job SET state='running',lease_owner='private-prepare',lease_token=123,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, acquired.JobID)
	proof, err := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, []byte(transmissionTVRaw))
	if err != nil {
		t.Fatal(err)
	}
	return &prepareTVFixture{completeTVFixture: f, c: ports.PrepareCompleteTVSubmissionCommand{Lease: ports.JobLease{ID: acquired.JobID, Owner: "private-prepare", Token: 123}, TaskID: uuid.New(), OperationID: acquired.OperationID, Metainfo: proof}, store: postgres.NewCompleteTVSubmissionStore(f.f.a.DBPool(), idgen.New()), raw: []byte(transmissionTVRaw)}
}

func (f *transmissionTVFixture) send(t *testing.T) *ports.CompleteTVSubmissionResultReceipt {
	t.Helper()
	r, e := f.manager.SendCompleteTV(t.Context(), f.c.Lease, f.c.OperationID)
	if e != nil || r == nil {
		t.Fatal(r, e)
	}
	return r
}
func (f *transmissionTVFixture) request(t *testing.T, kind string) ports.CompleteTVTrackingRequest {
	t.Helper()
	r := ports.CompleteTVTrackingRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "transmission-observer", Token: 1}, OperationID: f.c.OperationID}
	if e := f.f.a.DBPool().QueryRow(t.Context(), `SELECT id FROM download_task WHERE operation_id=$1`, r.OperationID).Scan(&r.TaskID); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, r.Lease.ID, kind, r.TaskID.String(), r.OperationID.String(), r.Lease.Owner, r.Lease.Token)
	return r
}
func (f *transmissionTVFixture) assertProtected(t *testing.T) {
	t.Helper()
	var valid bool
	e := f.f.a.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)=2 AND (SELECT count(*) FROM unit_reservation WHERE state='dispatched')=2 AND (SELECT count(*) FROM download_requirement WHERE state='pending')=2 AND (SELECT count(*) FROM complete_tv_physical_permit)=0 AND (SELECT count(*) FROM download_task)=1 AND (SELECT jsonb_array_length(proof->'original_files') FROM complete_tv_submission_preparation)=3`).Scan(&valid)
	if e != nil || !valid || f.adds.Load() != 1 {
		t.Fatal("original send/protection/manifest changed", valid, e, f.adds.Load())
	}
}

func TestTransmissionCompleteTVNativePG(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "wrong-manifest", "changed-after", "conflicting-identity", "late-lease", "changed-config", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newTransmissionTVFixture(t)
			f.lost.Store(mode == "lost-ack")
			sent := f.send(t)
			wantSend := "tracking"
			if mode == "lost-ack" {
				wantSend = "unknown"
			}
			if sent.State != wantSend {
				t.Fatal(sent)
			}
			if mode == "lost-ack" {
				r := f.request(t, ports.CompleteTVObservationJobKind)
				out, e := f.manager.ObserveCompleteTV(t.Context(), postgres.NewCompleteTVObservationStore(f.f.a.DBPool(), idgen.New()), r)
				if e != nil || out.State != "tracking" {
					t.Fatal(out, e)
				}
			}
			r := f.request(t, ports.CompleteTVTrackingJobKind)
			f.partial.Store(mode == "partial")
			f.wrongFiles.Store(mode == "wrong-manifest")
			f.changedAfter.Store(mode == "changed-after")
			f.conflict.Store(mode == "conflicting-identity")
			if mode == "late-lease" || mode == "changed-config" {
				hook := func() {
					if mode == "late-lease" {
						f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.Lease.ID)
					} else {
						f.exec(t, `UPDATE download_client SET username='changed'`)
					}
				}
				f.hook.Store(&hook)
			}
			out, e := f.manager.ObserveCompleteTVTracking(t.Context(), postgres.NewCompleteTVTrackingStore(f.f.a.DBPool(), idgen.New()), torrent.Inspector{}, r)
			if mode == "late-lease" || mode == "changed-config" {
				if out != nil || !errors.Is(e, ports.ErrCompleteTVObservationStale) {
					t.Fatal(out, e)
				}
			} else {
				want := "completed"
				if mode == "partial" {
					want = "tracking"
				}
				if mode == "wrong-manifest" || mode == "changed-after" || mode == "conflicting-identity" {
					want = "unknown"
				}
				if e != nil || out == nil || out.State != want {
					t.Fatal(out, e, "want", want)
				}
				if want == "completed" && len(out.Files) != 3 {
					t.Fatal("unwanted zero-byte attachment omitted")
				}
			}
			if out, e := f.manager.SendCompleteTV(t.Context(), f.c.Lease, f.c.OperationID); out != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
				t.Fatal("Add replay", out, e)
			}
			f.assertProtected(t)
		})
	}
}

func TestTransmissionCompleteTVPhysicalFenceUntilNativeReturnPG(t *testing.T) {
	for _, stage := range []string{"send", "tracking"} {
		t.Run(stage, func(t *testing.T) {
			f := newTransmissionTVFixture(t)
			f.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
				return transmission.NewClient(url, user, password, &http.Client{Transport: trackingUncancelableTransport{}})
			})
			var request ports.CompleteTVTrackingRequest
			if stage == "tracking" {
				f.send(t)
				request = f.request(t, ports.CompleteTVTrackingJobKind)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			hook := func() { enterOnce.Do(func() { close(entered); <-release }) }
			f.hook.Store(&hook)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if stage == "send" {
					_, err = f.manager.SendCompleteTV(ctx, f.c.Lease, f.c.OperationID)
				} else {
					_, err = f.manager.ObserveCompleteTVTracking(ctx, postgres.NewCompleteTVTrackingStore(f.f.a.DBPool(), idgen.New()), torrent.Inspector{}, request)
				}
				done <- err
			}()
			drained := false
			defer func() {
				unblock()
				if !drained {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("native owner did not drain")
					}
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native request did not start")
			}
			cancel()
			select {
			case err := <-done:
				drained = true
				t.Fatal("physical call released before return", err)
			case <-time.After(50 * time.Millisecond):
			}
			if r, err := f.manager.SendCompleteTV(t.Context(), f.c.Lease, f.c.OperationID); r != nil || !errors.Is(err, ports.ErrCompleteTVNeedsObservation) {
				t.Fatal("send bypassed active native physical owner", r, err)
			}
			fresh := f.request(t, ports.CompleteTVTrackingJobKind)
			if r, err := f.manager.ObserveCompleteTVTracking(t.Context(), postgres.NewCompleteTVTrackingStore(f.f.a.DBPool(), idgen.New()), torrent.Inspector{}, fresh); r != nil || !errors.Is(err, ports.ErrCompleteTVNeedsObservation) {
				t.Fatal("tracking bypassed active native physical owner", r, err)
			}
			unblock()
			select {
			case err := <-done:
				drained = true
				if stage == "tracking" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if stage == "send" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("native request did not return")
			}
			f.assertProtected(t)
		})
	}
}

func TestTransmissionCompleteTVAppNativeSendTrackingAndExplicitOrganization(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack", true: "lost-ack"}[lost], func(t *testing.T) {
			source, target := filepath.Join(t.TempDir(), "downloads"), filepath.Join(t.TempDir(), "library")
			for _, p := range []string{filepath.Join(source, "Show"), target} {
				if e := os.MkdirAll(p, 0700); e != nil {
					t.Fatal(e)
				}
			}
			for _, name := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
				if e := os.WriteFile(filepath.Join(source, "Show", name), make([]byte, 512), 0600); e != nil {
					t.Fatal(e)
				}
			}
			_ = os.WriteFile(filepath.Join(source, "Show", "note.txt"), nil, 0600)
			_ = os.WriteFile(filepath.Join(target, ".pulsemedia_root"), []byte("fixture"), 0600)
			f := newTransmissionTVFixture(t, func(b *completeTVFixture) {
				b.exec(t, `UPDATE library_root SET path=$1 WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`, target, b.f.sub.Id)
				b.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, b.f.sub.Id)
				b.exec(t, `UPDATE download_client SET remote_path_map=jsonb_build_object($1::text,$2::text)`, target, source)
			})
			f.lost.Store(lost)
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.c.Lease.ID)
			runtime := &runtimeTVFixture{byteSenderFixture: &byteSenderFixture{prepareTVFixture: f.prepareTVFixture, key: f.key}}
			stop := runtime.start(t)
			runtimeTVWait(t, func() bool {
				var state string
				_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task WHERE operation_id=$1`, f.c.OperationID).Scan(&state)
				return state == "tracking" || state == "completed"
			})
			stop()
			f.exec(t, `UPDATE download_task SET updated_at=clock_timestamp()-interval '31 seconds' WHERE operation_id=$1`, f.c.OperationID)
			if e := postgres.NewCompleteTVTrackingRuntimeStore(f.f.a.DBPool(), idgen.New()).ScheduleCompleteTVTracking(t.Context()); e != nil {
				t.Fatal(e)
			}
			stop = runtime.start(t)
			runtimeTVWait(t, func() bool {
				var state string
				_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task WHERE operation_id=$1`, f.c.OperationID).Scan(&state)
				return state == "completed"
			})
			stop()
			f.assertProtected(t)
			var task, job uuid.UUID
			var result []byte
			if e := f.f.a.DBPool().QueryRow(t.Context(), `SELECT t.id,j.id,j.result FROM download_task t JOIN job j ON j.kind=$2 AND j.result->>'task_id'=t.id::text WHERE t.operation_id=$1`, f.c.OperationID, ports.CompleteTVTrackingJobKind).Scan(&task, &job, &result); e != nil {
				t.Fatal(e)
			}
			var receipt ports.CompleteTVTrackingReceipt
			if e := json.Unmarshal(result, &receipt); e != nil {
				t.Fatal(e)
			}
			// Explicit organization consumes the actual App worker's immutable tracking result.
			n := &trackingFixture{observationFixture: &observationFixture{tvResultFixture: &tvResultFixture{prepareTVFixture: f.prepareTVFixture}, request: ports.CompleteTVTrackingRequest{TaskID: task, OperationID: f.c.OperationID, Lease: ports.JobLease{ID: job}}}}
			ids := idgen.New()
			repo := transferstore.NewRepository(f.f.a.DBPool(), ids, outbox.New(f.f.a.DBPool(), ids, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
			org := &completeOrganizationFixture{n: n, source: source, target: target, service: transfercase.NewCompleteTVOrganizationService(repo), worker: transfercase.NewService(repo), command: ports.CompleteTVOrganizationCommand{TaskID: task, OperationID: f.c.OperationID, TrackingJobID: job, ExpectedEvidenceStamp: receipt.EvidenceStamp, Confirmed: true}}
			// The real generated router authenticates the Cookie and records the
			// explicit confirmation. App.Run alone owns the physical worker lease.
			router := organizationPublicRouter(org, true)
			body := organizationPublicBody(org)
			completePublicProblem(t, organizationPublicCall(router, org, body, "transmission-anonymous", "", nil), http.StatusUnauthorized)
			completePublicProblem(t, organizationPublicCall(router, org, body, "transmission-member", f.f.member, nil), http.StatusForbidden)
			confirmed := organizationPublicReceipt(t, organizationPublicCall(router, org, body, "transmission-explicit-organization", f.f.admin, nil))
			stop = runtime.start(t)
			runtimeTVWait(t, func() bool {
				var organized bool
				_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT organization_state='completed' FROM download_task WHERE id=$1`, task).Scan(&organized)
				return organized
			})
			stop()
			org.assertImported(t, &confirmed)
			recovered := organizationPublicReceipt(t, organizationPublicCall(router, org, body, "transmission-explicit-organization", f.f.admin, nil))
			if recovered != confirmed {
				t.Fatal("explicit public receipt changed across App restart")
			}
			if f.adds.Load() != 1 || f.fetches.Load() != 1 || f.rejected.Load() == 0 {
				t.Fatal("send/recovery replay", f.adds.Load(), f.fetches.Load(), f.rejected.Load())
			}
			// The native worker stays read-only across restart after the explicit import.
			stop = runtime.start(t)
			stop()
			if f.adds.Load() != 1 {
				t.Fatal("restart repeated Add")
			}
		})
	}
}
