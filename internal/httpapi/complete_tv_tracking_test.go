package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type trackingFixture struct {
	*observationFixture
	manager                    *download.Manager
	store                      *postgres.CompleteTVTrackingStore
	info                       func(int32) string
	files                      func() string
	export                     func() string
	hook                       func(string)
	lookups, inventories, adds atomic.Int32
}

const trackingFilesJSON = `[{"index":0,"name":"Show/Show.S01E01.mkv","size":512,"progress":1},{"index":1,"name":"Show/Show.S01E02.mkv","size":512,"progress":1}]`

func newTrackingFixture(t *testing.T, attachments ...bool) *trackingFixture {
	return newTrackingFixtureWithRoots(t, nil, attachments...)
}
func newTrackingFixtureWithRoots(t *testing.T, before func(*completeTVFixture), attachments ...bool) *trackingFixture {
	t.Helper()
	n := &trackingFixture{}
	rawInput, originalHash := completeTVRaw, "d497dcf93e2dbd4b917ba68788ba7d42590cf594"
	if len(attachments) > 0 && attachments[0] {
		rawInput = "d4:infod5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveed6:lengthi0e4:pathl8:note.txteee4:name4:Show12:piece lengthi1024e6:pieces20:abcdefghijklmnopqrstee"
		originalHash = "2b9fc2d07ed413dec72dcf377b3ffde554d65184"
	}
	n.export = func() string { return rawInput }
	key := bytes.Repeat([]byte{42}, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.hook != nil {
			n.hook(r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			if r.Method != "GET" || r.URL.Query().Get("tag") != "pulsemedia:op:"+n.request.OperationID.String() || len(r.URL.Query()) != 1 {
				w.WriteHeader(400)
				return
			}
			_, _ = io.WriteString(w, n.info(n.lookups.Add(1)))
		case "/api/v2/torrents/export":
			if r.Method != "GET" || r.URL.Query().Get("hash") != originalHash || len(r.URL.Query()) != 1 {
				w.WriteHeader(400)
				return
			}
			_, _ = io.WriteString(w, n.export())
		case "/api/v2/torrents/files":
			n.inventories.Add(1)
			if r.Method != "GET" || r.URL.Query().Get("hash") != originalHash || len(r.URL.Query()) != 1 {
				w.WriteHeader(400)
				return
			}
			_, _ = io.WriteString(w, n.files())
		case "/api/v2/torrents/add":
			n.adds.Add(1)
			w.WriteHeader(500)
		default:
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	p := newPrepareTVFixture(t, func(f *completeTVFixture) {
		f.exec(t, `UPDATE release_candidate SET info_hash=$1`, originalHash)
		encrypted, e := crypto.Encrypt(key, []byte("tracking-password"))
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE download_client SET url=$1,username='tracking',password_encrypted=$2`, server.URL, encrypted)
		if before != nil {
			before(f)
		}
	})
	sealed, e := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, []byte(rawInput))
	if e != nil {
		t.Fatal(e)
	}
	p.c.Metainfo = sealed
	if _, e = p.prepare(); e != nil {
		t.Fatal(e)
	}
	f := &tvResultFixture{p, ports.CompleteTVSubmissionResultCommand{Lease: p.c.Lease, TaskID: p.c.TaskID, OperationID: p.c.OperationID, Metainfo: sealed, Acknowledged: true}}
	f.exec(t, `UPDATE download_task SET state='tracking'`)
	f.exec(t, `UPDATE job SET lease_owner='retired-sender',lease_token=999,lease_until=clock_timestamp()-interval '1 hour' WHERE id=$1`, p.c.Lease.ID)
	req := ports.CompleteTVTrackingRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "tracking-observer", Token: 7}, TaskID: p.c.TaskID, OperationID: p.c.OperationID}
	f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, req.Lease.ID, ports.CompleteTVTrackingJobKind, req.TaskID.String(), req.OperationID.String(), req.Lease.Owner, req.Lease.Token)
	n.observationFixture = &observationFixture{f, postgres.NewCompleteTVObservationStore(f.f.a.DBPool(), idgen.New()), req}
	var save string
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT save_path FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&save); e != nil {
		t.Fatal(e)
	}
	n.info = func(_ int32) string {
		b, _ := json.Marshal([]any{map[string]any{"hash": originalHash, "save_path": save, "size": 1024, "downloaded": 1024, "progress": 1, "state": "uploading", "tags": "pulsemedia:op:" + n.request.OperationID.String()}})
		return string(b)
	}
	n.files = func() string { return trackingFilesJSON }
	n.store = postgres.NewCompleteTVTrackingStore(n.f.a.DBPool(), idgen.New())
	n.manager = download.NewManager(n.f.a.DBPool(), idgen.New(), key)
	n.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(url, user, password, nil)
	})
	return n
}
func (n *trackingFixture) observe(ctx context.Context) (*ports.CompleteTVTrackingReceipt, error) {
	return n.manager.ObserveCompleteTVTracking(ctx, n.store, torrent.Inspector{}, n.request)
}
func (n *trackingFixture) assert(t *testing.T, state string) {
	t.Helper()
	var got, org, job string
	var lanes, reservations, demands, plans, facts, permits, audits int
	var progress float32
	var total, downloaded int64
	e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT t.state,t.organization_state,j.state,t.progress,t.total_bytes,t.downloaded_bytes,
 (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE state='dispatched'),(SELECT count(*) FROM download_requirement WHERE state='pending'),
 (SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM library_file_unit)+(SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM complete_tv_physical_permit),
 (SELECT count(*) FROM audit_log WHERE action='complete_tv.tracking_observed')
 FROM download_task t JOIN job j ON j.id=$1 WHERE t.id=$2`, n.request.Lease.ID, n.request.TaskID).Scan(&got, &org, &job, &progress, &total, &downloaded, &lanes, &reservations, &demands, &plans, &facts, &permits, &audits)
	if e != nil || got != state || job != "succeeded" || lanes != 2 || reservations != 2 || demands != 2 || plans != 0 || facts != 0 || permits != 0 || audits != 1 || n.adds.Load() != 0 {
		t.Fatalf("invariant: %v %s %s lanes=%d res=%d demands=%d plans=%d facts=%d permits=%d audits=%d", e, got, job, lanes, reservations, demands, plans, facts, permits, audits)
	}
	if state == "completed" && (org != "review_required" || progress != 1 || total != 1024 || downloaded != 1024) {
		t.Fatal("completion fields", org, progress, total, downloaded)
	}
	var coherent bool
	e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT j.result=a.payload AND j.result->>'state'=t.state AND length(j.result->>'evidence_stamp')=64 AND e.payload->>'state'=t.state FROM job j JOIN download_task t ON t.id=$2 JOIN audit_log a ON a.action='complete_tv.tracking_observed' JOIN outbox_event e ON e.event_type='complete_tv.tracking_observed' WHERE j.id=$1`, n.request.Lease.ID, n.request.TaskID).Scan(&coherent)
	if e != nil || !coherent {
		t.Fatal("receipt coherence", e)
	}
}
func TestCompleteTVTrackingNativePG(t *testing.T) {
	for _, kind := range []string{"complete", "revoked", "partial", "near-one", "after-partial", "missing", "duplicate", "path", "bytes", "index", "truncated", "null", "duplicate-json", "info-unicode-progress", "file-unicode-progress", "file-partial", "file-near-one", "info-hash", "info-tag", "info-path", "info-bytes", "info-missing", "info-ambiguous", "rechecking", "after-hash"} {
		t.Run(kind, func(t *testing.T) {
			n := newTrackingFixture(t)
			orig := n.info
			want := "unknown"
			unavailable := false
			switch kind {
			case "info-hash":
				n.info = func(i int32) string {
					return strings.Replace(orig(i), "d497dcf93e2dbd4b917ba68788ba7d42590cf594", "ffffffffffffffffffffffffffffffffffffffff", 1)
				}
			case "info-tag":
				n.info = func(i int32) string { return strings.Replace(orig(i), "pulsemedia:op:", "other:op:", 1) }
			case "info-path":
				n.info = func(i int32) string { return strings.Replace(orig(i), `"save_path":"`, `"save_path":"other/`, 1) }
			case "info-bytes":
				n.info = func(i int32) string { return strings.Replace(orig(i), `"size":1024`, `"size":1023`, 1) }
			case "info-missing":
				n.info = func(int32) string { return `[]` }
			case "info-ambiguous":
				n.info = func(i int32) string {
					s := orig(i)
					return strings.TrimSuffix(s, "]") + "," + strings.TrimPrefix(s, "[")
				}
			case "rechecking":
				want = "tracking"
				n.info = func(i int32) string { return strings.Replace(orig(i), "uploading", "checkingUP", 1) }
			case "after-hash":
				n.info = func(i int32) string {
					if i == 2 {
						return strings.Replace(orig(i), "d497dcf93e2dbd4b917ba68788ba7d42590cf594", "ffffffffffffffffffffffffffffffffffffffff", 1)
					}
					return orig(i)
				}
			case "complete":
				want = "completed"
			case "revoked":
				want = "completed"
				n.hook = func(p string) {
					if strings.HasSuffix(p, "/files") {
						n.exec(t, `UPDATE app_user SET is_active=false`)
						n.exec(t, `UPDATE indexer SET health='offline',config_version=config_version+1`)
					}
				}
			case "partial":
				want = "tracking"
				n.info = func(i int32) string { return strings.Replace(orig(i), `"progress":1`, `"progress":0.5`, 1) }
			case "near-one":
				want = "tracking"
				n.info = func(i int32) string {
					return strings.Replace(orig(i), `"progress":1`, `"progress":0.9999999999999999999`, 1)
				}
			case "after-partial":
				want = "tracking"
				n.info = func(i int32) string {
					if i == 2 {
						return strings.Replace(orig(i), `"progress":1`, `"progress":0.5`, 1)
					}
					return orig(i)
				}
			case "missing":
				n.files = func() string { return `[ {"index":0,"name":"Show/Show.S01E01.mkv","size":512,"progress":1} ]` }
			case "duplicate":
				unavailable = true
				n.files = func() string { return strings.Replace(trackingFilesJSON, "Show.S01E02", "Show.S01E01", 1) }
			case "path":
				n.files = func() string { return strings.Replace(trackingFilesJSON, "Show.S01E02", "Show.S01E03", 1) }
			case "bytes":
				n.files = func() string { return strings.Replace(trackingFilesJSON, "512", "511", 1) }
			case "index":
				n.files = func() string { return strings.Replace(trackingFilesJSON, `"index":1`, `"index":2`, 1) }
			case "truncated":
				unavailable = true
				n.files = func() string { return trackingFilesJSON[:len(trackingFilesJSON)-1] }
			case "null":
				unavailable = true
				n.files = func() string { return `null` }
			case "info-unicode-progress":
				unavailable = true
				n.info = func(i int32) string {
					return strings.Replace(orig(i), `"progress":1`, `"progress":0.5,"progre\u017fs":1`, 1)
				}
			case "file-unicode-progress":
				unavailable = true
				n.files = func() string {
					return strings.Replace(trackingFilesJSON, `"progress":1`, `"progress":0.5,"progre\u017fs":1`, 1)
				}
			case "duplicate-json":
				unavailable = true
				n.files = func() string { return strings.Replace(trackingFilesJSON, `"size":512`, `"size":1,"Size":512`, 1) }
			case "file-partial":
				n.files = func() string { return strings.Replace(trackingFilesJSON, `"progress":1`, `"progress":0.5`, 1) }
			case "file-near-one":
				n.files = func() string {
					return strings.Replace(trackingFilesJSON, `"progress":1`, `"progress":0.9999999999999999999`, 1)
				}
			}
			before := n.snapshot(t)
			r, e := n.observe(context.Background())
			if unavailable {
				if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
					t.Fatal("unavailable changed facts", r, e)
				}
				return
			}
			if e != nil || r == nil || r.State != want {
				t.Fatal(r, e, "want", want)
			}
			n.assert(t, want)
		})
	}
}
func TestCompleteTVTrackingPGGuards(t *testing.T) {
	for _, kind := range []string{"owner", "token", "expired", "released-lane", "released-reservation", "deleted-unit", "tail-task", "tail-progress", "tail-protection", "tail-job", "database", "canceled", "unit-version", "unit-path", "unit-size", "tail-audit", "tail-event"} {
		t.Run(kind, func(t *testing.T) {
			n := newTrackingFixture(t)
			want := ports.ErrCompleteTVObservationStale
			ctx := context.Background()
			switch kind {
			case "unit-version", "unit-path", "unit-size":
				n.exec(t, `ALTER TABLE complete_tv_submission_unit DISABLE TRIGGER USER`)
				column, value := "unit_version", "unit_version+1"
				if kind == "unit-path" {
					column, value = "video_path", "video_path||'.other'"
				}
				if kind == "unit-size" {
					column, value = "video_size", "video_size+1"
				}
				n.exec(t, `UPDATE complete_tv_submission_unit SET `+column+`=`+value)
			case "tail-audit", "tail-event":
				stmt := `UPDATE audit_log SET payload='{}'::jsonb WHERE action='complete_tv.tracking_observed';`
				if kind == "tail-event" {
					stmt = `UPDATE outbox_event SET payload='{}'::jsonb WHERE event_type='complete_tv.tracking_observed';`
				}
				n.exec(t, `CREATE FUNCTION tracking_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='complete_tv_coverage_tracking' AND NEW.state='succeeded' THEN `+stmt+` END IF; RETURN NEW; END $$`)
				n.exec(t, `CREATE TRIGGER tracking_tail BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION tracking_tail()`)
			case "owner":
				n.exec(t, `UPDATE job SET lease_owner='other' WHERE id=$1`, n.request.Lease.ID)
			case "token":
				n.exec(t, `UPDATE job SET lease_token=8 WHERE id=$1`, n.request.Lease.ID)
			case "expired":
				n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, n.request.Lease.ID)
			case "released-lane":
				n.exec(t, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp,released_at) SELECT $1,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp,clock_timestamp() FROM acquisition_reservation LIMIT 1`, uuid.New())
			case "released-reservation":
				n.exec(t, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state) SELECT $1,unit_id,root_id,purpose,candidate_id,operation_id,'released' FROM unit_reservation LIMIT 1`, uuid.New())
			case "deleted-unit":
				n.exec(t, `ALTER TABLE complete_tv_submission_unit DISABLE TRIGGER USER`)
				n.exec(t, `DELETE FROM complete_tv_submission_unit WHERE unit_id=(SELECT unit_id FROM complete_tv_submission_unit LIMIT 1)`)
			case "tail-task", "tail-progress", "tail-protection", "tail-job":
				stmt := `UPDATE download_task SET state='unknown';`
				if kind == "tail-progress" {
					stmt = `UPDATE download_task SET progress=0.5;`
				}
				if kind == "tail-protection" {
					stmt = `UPDATE unit_reservation SET state='released';`
				}
				if kind == "tail-job" {
					stmt = `NEW.result='{}'::jsonb;`
				}
				n.exec(t, `CREATE FUNCTION tracking_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='complete_tv_coverage_tracking' AND NEW.state='succeeded' THEN `+stmt+` END IF; RETURN NEW; END $$`)
				n.exec(t, `CREATE TRIGGER tracking_tail BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION tracking_tail()`)
			case "database":
				want = ports.ErrUnavailable
				n.exec(t, `ALTER TABLE complete_tv_submission_preparation RENAME TO offline_preparation`)
				defer n.exec(t, `ALTER TABLE offline_preparation RENAME TO complete_tv_submission_preparation`)
			case "canceled":
				want = context.Canceled
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			// Schema rename makes the generic fixture snapshot query invalid; capture after restore instead.
			before := ""
			if kind != "database" {
				before = n.snapshot(t)
			}
			r, e := n.observe(ctx)
			if r != nil || !errors.Is(e, want) {
				t.Fatal(r, e, "want", want)
			}
			if kind != "database" && n.snapshot(t) != before {
				t.Fatal("rejection mutated facts")
			}
		})
	}
}
func TestCompleteTVTrackingNativePGPhysicalExclusion(t *testing.T) {
	n := newTrackingFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	n.hook = func(p string) {
		if strings.HasSuffix(p, "/files") {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, e := n.observe(context.Background()); done <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no inventory")
	}
	r, e := n.observe(context.Background())
	if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("overlap", r, e)
	}
	var permits int
	if e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&permits); e != nil || permits != 1 {
		t.Fatal("permit not retained", e, permits)
	}
	// Unblock synchronously before fixture cleanup.
	release <- struct{}{}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	n.assert(t, "completed")
}
