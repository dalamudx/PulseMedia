package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	download "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type upgradeWorkerFixture struct {
	*nativeUpgradeFixture
	cmdRepo ports.MovieUpgradeSendCommandRepository
	native  ports.MovieUpgradeNativeJobSendRepository
	cmd     *ports.MovieUpgradeSendJobReceipt
	fetches atomic.Int32
	body    atomic.Value
}

func upgradeWorkerNative(t *testing.T, ack string) *upgradeWorkerFixture {
	t.Helper()
	f := &upgradeWorkerFixture{}
	f.body.Store([]byte{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrent" {
			http.NotFound(w, r)
			return
		}
		f.fetches.Add(1)
		if r.URL.Query().Get("apikey") != "worker-source-key" {
			t.Error("missing source authorization")
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		if _, e := w.Write(f.body.Load().([]byte)); e != nil {
			t.Error("torrent response write")
		}
	}))
	t.Cleanup(server.Close)
	key := bytes.Repeat([]byte{37}, 32)
	link, e := crypto.Encrypt(key, []byte(server.URL+"/torrent?apikey=worker-source-key"))
	if e != nil {
		t.Fatal(e)
	}
	credential, e := crypto.Encrypt(key, []byte("worker-source-key"))
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	n := nativeUpgrade(t, ack, false, func(seed *frozenUpgradeFixture) {
		seed.exec(t, `UPDATE library_root SET path=$2 WHERE id=$1`, seed.root, root)
		seed.exec(t, `UPDATE library_root SET baseline_completed=true,health='online' WHERE id=$1`, seed.root)
		// This seeded baseline represents an already scanned root, not a root due on first App.Run.
		seed.exec(t, `INSERT INTO library_scan_schedule(root_id,next_run_at) VALUES($1,clock_timestamp()+interval '1 hour') ON CONFLICT(root_id) DO UPDATE SET next_run_at=EXCLUDED.next_run_at`, seed.root)
		seed.exec(t, `UPDATE indexer SET url=$2,api_key_encrypted=$3,health='online',health_next_check_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, seed.source, server.URL, credential)
		seed.exec(t, `UPDATE release_candidate SET link_encrypted=$2,source_config_version=(SELECT config_version FROM indexer WHERE id=$3) WHERE id=$1`, seed.candidate, link, seed.source)
		query := uuid.New()
		seed.exec(t, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) SELECT $1,id,'search','Fight Club',config_version,'succeeded',clock_timestamp() FROM indexer WHERE id=$2`, query, seed.source)
		seed.exec(t, `INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)`, query, seed.candidate)
		seed.project(t)
	})
	f.nativeUpgradeFixture = n
	f.body.Store(append([]byte(nil), n.proof.Bytes()...))
	var command ports.QueueMovieUpgradeSend
	_, f.cmdRepo, f.native, command = sendJobFixtureFromNative(t, n)
	f.cmd, e = f.cmdRepo.QueueMovieUpgradeSend(context.Background(), n.actor, command, "worker-send")
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *upgradeWorkerFixture) executor(t *testing.T, fetch ports.CompleteTVTorrentFetcher) ports.MovieUpgradeSendJobExecutor {
	t.Helper()
	work, e := postgres.NewMovieUpgradeSendWorkStore(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	out, e := download.NewMovieUpgradeSendJobExecutor(f.manager, work, fetch, torrent.Inspector{}, f.native)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func (f *upgradeWorkerFixture) assertNoDispatch(t *testing.T) {
	t.Helper()
	var count int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_job_claim WHERE job_id=$1)+(SELECT count(*) FROM movie_upgrade_send_authorization WHERE operation_id=$2)+(SELECT count(*) FROM movie_upgrade_send_dispatch WHERE operation_id=$2)`, f.cmd.JobID, f.operation).Scan(&count); e != nil || count != 0 || f.adds.Load() != 0 {
		t.Fatalf("unexpected eligibility count=%d add=%d err=%v", count, f.adds.Load(), e)
	}
}
func TestMovieUpgradeSendWorkerFetchAndExactBytesPG(t *testing.T) {
	for _, ack := range []string{"Ok.", "ambiguous"} {
		t.Run(ack, func(t *testing.T) {
			f := upgradeWorkerNative(t, ack)
			req := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			old := f.oldState(t)
			if e := f.executor(t, torznab.CompleteTVFetcher{}).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.MustParse(f.actor.ID)); e != nil {
				t.Fatal(e)
			}
			upload := <-f.received
			if !bytes.Equal(upload.raw, f.proof.Bytes()) || f.fetches.Load() != 1 || f.adds.Load() != 1 {
				t.Fatal("expected one original-byte fetch/Add")
			}
			var status, state string
			if e := f.p.QueryRow(context.Background(), `SELECT r.status,j.state FROM movie_upgrade_send_result r JOIN movie_upgrade_send_job b USING(operation_id) JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1`, f.operation).Scan(&status, &state); e != nil || state != "succeeded" {
				t.Fatal(status, state, e)
			}
			want := "confirmed"
			if ack != "Ok." {
				want = "unknown"
			}
			if status != want {
				t.Fatal(status)
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
				t.Fatal("protected graph changed")
			}
			if e := f.executor(t, torznab.CompleteTVFetcher{}).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.MustParse(f.actor.ID)); e == nil || f.fetches.Load() != 1 || f.adds.Load() != 1 {
				t.Fatal("consumed job fetched or sent again", e)
			}
		})
	}
}
func TestMovieUpgradeSendWorkerDifferentOuterBytesPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	req := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
	work, _ := postgres.NewMovieUpgradeSendWorkStore(f.preparation)
	loaded, e := work.LoadMovieUpgradeSendWork(context.Background(), req, uuid.MustParse(f.actor.ID))
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := json.Marshal(loaded)
	if e != nil || string(encoded) != "{}" {
		t.Fatal("network work leaked through JSON", e)
	}
	original := f.proof.Bytes()
	at := bytes.Index(original, []byte("4:info"))
	if at < 0 {
		t.Fatal("missing fixture info")
	}
	raw := append(append(append([]byte(nil), original[:at]...), []byte("7:comment7:changed")...), original[at:]...)
	proof, e := torrent.InspectMovieUpgradeMetainfo(raw, loaded.Target)
	if e != nil || proof.InfoHash() != f.proof.InfoHash() {
		t.Fatal("invalid same-info counterexample", e)
	}
	f.body.Store(raw)
	if e = f.executor(t, torznab.CompleteTVFetcher{}).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.MustParse(f.actor.ID)); e == nil {
		t.Fatal("changed torrent bytes accepted")
	}
	f.assertNoDispatch(t)
}

type upgradeWorkerFetchFunc func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error)

func (f upgradeWorkerFetchFunc) FetchCompleteTVTorrent(c context.Context, s ports.CompleteTVTorrentSource) ([]byte, error) {
	return f(c, s)
}
func TestMovieUpgradeSendWorkerChangesDuringFetchPG(t *testing.T) {
	for _, change := range []string{"claim", "account", "source"} {
		t.Run(change, func(t *testing.T) {
			f := upgradeWorkerNative(t, "Ok.")
			req := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
			fetch := upgradeWorkerFetchFunc(func(ctx context.Context, s ports.CompleteTVTorrentSource) ([]byte, error) {
				raw, e := (torznab.CompleteTVFetcher{}).FetchCompleteTVTorrent(ctx, s)
				if e != nil {
					return nil, e
				}
				switch change {
				case "claim":
					f.exec(t, `ALTER TABLE job DISABLE TRIGGER movie_upgrade_send_job_terminal`)
					f.exec(t, `UPDATE job SET lease_until=lease_until+interval '1 second' WHERE id=$1`, req.Lease.ID)
					f.exec(t, `ALTER TABLE job ENABLE TRIGGER movie_upgrade_send_job_terminal`)
				case "account":
					f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
				case "source":
					f.exec(t, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, f.source)
				}
				return raw, nil
			})
			if e := f.executor(t, fetch).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.MustParse(f.actor.ID)); e == nil {
				t.Fatal("changed work accepted")
			}
			if f.fetches.Load() != 1 {
				t.Fatal("fixture did not fetch")
			}
			f.assertNoDispatch(t)
		})
	}
}
func TestMovieUpgradeSendWorkerWrongActorDoesNotFetchPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	req := claimSendJob(t, f.nativeUpgradeFixture, f.cmd)
	if e := f.executor(t, torznab.CompleteTVFetcher{}).ExecuteMovieUpgradeSendJob(context.Background(), req, uuid.New()); e == nil || f.fetches.Load() != 0 {
		t.Fatal("untrusted actor fetched", e)
	}
	f.assertNoDispatch(t)
}
func TestMovieUpgradeSendAppRunRestartPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	metadataBefore := sendWorkerProtectedMetadata(t, f)
	old := f.oldState(t)
	data := t.TempDir()
	stop := startObservationRuntimeApp(t, f.nativeUpgradeFixture, data)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	fail := func(cause error) {
		var state, reason string
		var protected bool
		var arms, dispatches int
		diagnostic := f.p.QueryRow(context.Background(), `SELECT j.state,COALESCE(j.error,''),b.protected_sha256=pulse_upgrade_send_job_protected(b.operation_id),(SELECT count(*) FROM movie_upgrade_send_authorization a WHERE a.operation_id=b.operation_id),(SELECT count(*) FROM movie_upgrade_send_dispatch d WHERE d.operation_id=b.operation_id) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1`, f.operation).Scan(&state, &reason, &protected, &arms, &dispatches)
		var oldRows, newRows map[string]map[string]json.RawMessage
		if e := json.Unmarshal([]byte(metadataBefore), &oldRows); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal([]byte(sendWorkerProtectedMetadata(t, f)), &newRows); e != nil {
			t.Fatal(e)
		}
		var changed []string
		for table, row := range oldRows {
			for field, value := range row {
				if !bytes.Equal(value, newRows[table][field]) {
					changed = append(changed, table+"."+field)
				}
			}
		}
		t.Fatalf("App wait: %v state=%s reason=%s protected=%v changed=%v arm=%d dispatch=%d fetch=%d add=%d diagnostic=%v", cause, state, reason, protected, changed, arms, dispatches, f.fetches.Load(), f.adds.Load(), diagnostic)
	}
	for {
		var done bool
		if e := f.p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_result r JOIN movie_upgrade_send_job b USING(operation_id) JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1 AND r.status='confirmed' AND j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_send_job_terminal(j.id))`, f.operation).Scan(&done); e != nil {
			fail(e)
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			fail(ctx.Err())
		case <-ticker.C:
		}
	}
	stop()
	stopAgain := startObservationRuntimeApp(t, f.nativeUpgradeFixture, data)
	timer := time.NewTimer(200 * time.Millisecond)
	<-timer.C
	stopAgain()
	receipt, e := f.cmdRepo.QueueMovieUpgradeSend(context.Background(), f.actor, ports.QueueMovieUpgradeSend{OperationID: f.operation, TaskID: f.cmd.TaskID, Confirmed: true}, "worker-send")
	if e != nil || receipt == nil || *receipt != *f.cmd {
		t.Fatal("original receipt changed", e)
	}
	if f.fetches.Load() != 1 || f.adds.Load() != 1 || f.observationQueries.Load() != 0 || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || old != f.oldState(t) {
		t.Fatal("App replayed I/O or changed protected graph")
	}
}
func TestMovieUpgradeSendAppDoesNotAdoptExpiredPG(t *testing.T) {
	f := upgradeWorkerNative(t, "Ok.")
	q := jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	job, err := q.Claim(context.Background(), []string{ports.MovieUpgradeSendJobKind}, "expired-send-worker", 200*time.Millisecond)
	if err != nil || job == nil {
		t.Fatal("short real claim", err)
	}
	<-time.After(time.Until(job.LeaseUntil.Time) + 20*time.Millisecond)
	var before, after string
	if err := f.p.QueryRow(context.Background(), `SELECT to_jsonb(j)::text FROM job j WHERE id=$1`, job.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	stop := startObservationRuntimeApp(t, f.nativeUpgradeFixture, t.TempDir())
	<-time.After(1100 * time.Millisecond)
	stop()
	if err := f.p.QueryRow(context.Background(), `SELECT to_jsonb(j)::text FROM job j WHERE id=$1`, job.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || f.fetches.Load() != 0 {
		t.Fatal("expired job adopted or mutated")
	}
	f.assertNoDispatch(t)
}

// Compare metadata omitted from the historical task/protection comparator.
func sendWorkerProtectedMetadata(t *testing.T, f *upgradeWorkerFixture) string {
	t.Helper()
	var value string
	err := f.p.QueryRow(context.Background(), `SELECT jsonb_build_object('wanted',(SELECT coalesce(jsonb_object_agg(w.id,to_jsonb(w)),'{}'::jsonb) FROM wanted_item w WHERE w.subscription_id=a.subscription_id),'indexer',(SELECT to_jsonb(i) FROM indexer i WHERE i.id=(a.snapshot->>'source_id')::uuid),'root',to_jsonb(r),'client',to_jsonb(c),'subscription',to_jsonb(s),'unit',to_jsonb(u))::text FROM movie_upgrade_authority a JOIN movie_upgrade_preparation p USING(operation_id) JOIN library_root r ON r.id=a.root_id JOIN download_client c ON c.id=(p.task_snapshot->>'client_id')::uuid JOIN subscription s ON s.id=a.subscription_id JOIN media_unit u ON u.id=a.unit_id WHERE a.operation_id=$1`, f.operation).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
