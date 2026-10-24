package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type movieUpgradeWatchFixture struct {
	*trackingJobFixture
	watch   *postgres.MovieUpgradeWatchStore
	source  ports.DownloadWatchSource
	hint    ports.RootChangeHint
	capture ports.MovieUpgradeTrackingHintCapture
}

// Real native Add and real native partial tracking; never manufacture observation rows.
func newMovieUpgradeWatchFixture(t *testing.T, beforeFreeze ...func(*frozenUpgradeFixture)) *movieUpgradeWatchFixture {
	t.Helper()
	f := nativeTrackingJobFixture(t, beforeFreeze...)
	if v, e := postgres.Migrate(t.Context(), f.dsn, "../../../migrations"); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatalf("current schema: %d %v", v, e)
	}
	scan := uuid.New()
	execIntent(t, f.p, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	execIntent(t, f.p, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.root)
	f.complete.Store(false)
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(t.Context()); e != nil || n != 1 {
		t.Fatal("periodic first job", n, e)
	}
	req := f.claim(t, 2*time.Minute)
	result, e := f.jobRepo.TrackMovieUpgradeJob(t.Context(), f.actor, req)
	if e != nil || result == nil || result.Outcome != "tracking" {
		t.Fatal("native partial", result, e)
	}
	watch, e := postgres.NewMovieUpgradeWatchStore(f.runtime, hintClock{})
	if e != nil {
		t.Fatal(e)
	}
	sources, e := watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 256})
	if e != nil || !sources.Complete || len(sources.Sources) == 0 {
		t.Fatalf("sources: %+v %v", sources, e)
	}
	var save string
	if e = f.p.QueryRow(t.Context(), `SELECT evidence->>'save_path' FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&save); e != nil {
		t.Fatal(e)
	}
	for _, source := range sources.Sources {
		remote := path.Join(save, f.proof.Files()[0].Path)
		b := source.Binding
		local := remote
		if b.SourceKind == "path_mapping" {
			if remote != b.RemotePrefix && !strings.HasPrefix(remote, b.RemotePrefix+"/") {
				continue
			}
			local = path.Join(b.LocalRoot, strings.TrimPrefix(remote, b.RemotePrefix))
		}
		if !strings.HasPrefix(local, b.LocalRoot+"/") {
			continue
		}
		h := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: strings.TrimPrefix(local, b.LocalRoot+"/"), Metadata: ports.RootChangeMetadata{EventID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, ReceivedAt: time.Now(), ManifestAt: source.ManifestAt, SourceIdentity: b.RootIdentity}}
		targets, e := watch.ResolveDownloadWatchTargets(t.Context(), b, h, ports.DownloadWatchTargetQuery{})
		if e != nil {
			t.Fatal(e)
		}
		if len(targets.Targets) == 0 {
			continue
		}
		if len(targets.Targets) != 1 || targets.Targets[0].Kind != "movie_upgrade_tracking_hint" {
			t.Fatalf("targets: %+v", targets)
		}
		c := targets.Targets[0].MovieUpgradeHintCapture
		if c.MovieUpgradeTrackingHintRequest().PriorObservationID != result.ObservationID {
			t.Fatal("wrong predecessor")
		}
		return &movieUpgradeWatchFixture{trackingJobFixture: f, watch: watch, source: source, hint: h, capture: c}
	}
	t.Fatal("prepared exact file had no target")
	return nil
}

func (f *movieUpgradeWatchFixture) snapshot(t *testing.T) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM library_root x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM library_file x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY observation_id) FROM movie_upgrade_tracking_observation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY observation_id) FROM movie_upgrade_tracking_result x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY observation_id) FROM movie_upgrade_tracking_permit x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM movie_upgrade_tracking_job x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM movie_upgrade_tracking_job_claim x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM idempotency_record x))::text`)
}

func (f *movieUpgradeWatchFixture) successors(t *testing.T) int {
	t.Helper()
	return intentCount(t, f.p, `SELECT count(*) FROM movie_upgrade_tracking_job WHERE operation_id=$1 AND prior_observation_id=$2`, f.operation, f.capture.MovieUpgradeTrackingHintRequest().PriorObservationID)
}

// One real 30s wait covers tail rollback, concurrent hints, and periodic competition.
func TestMovieUpgradeWatchDueAndAtomicQueuePG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	before := f.snapshot(t)
	protected := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	priorID := f.capture.MovieUpgradeTrackingHintRequest().PriorObservationID
	prior := publisherFrozen(t, f.p, `SELECT jsonb_build_array(to_jsonb(o),to_jsonb(r),to_jsonb(p))::text FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) JOIN movie_upgrade_tracking_permit p USING(observation_id) WHERE o.observation_id=$1`, priorID)
	if e := f.watch.ValidateMovieUpgradeTrackingHint(t.Context(), f.capture); e != nil {
		t.Fatal(e)
	}
	if _, e := f.watch.ResolveMovieUpgradeTrackingHint(t.Context(), f.capture.MovieUpgradeTrackingHintRequest()); e != nil {
		t.Fatal(e)
	}
	early, e := f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture)
	if e != nil || early.Queued || early.JobID != uuid.Nil || early.RetryAfter <= 0 || early.RetryAfter > 30*time.Second {
		t.Fatalf("early: %+v %v", early, e)
	}
	if f.snapshot(t) != before || f.successors(t) != 0 {
		t.Fatal("read-only path changed persistent state")
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(t.Context()); e != nil || n != 0 {
		t.Fatal("periodic gate", n, e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = f.watch.ScheduleMovieUpgradeTrackingHint(ctx, f.capture); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel", e)
	}
	// Wait on the real deadline. No UPDATE of immutable history or clock backdating.
	timer := time.NewTimer(early.RetryAfter + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	for _, mode := range []string{"tail_mutation", "deferred_failure"} {
		body := fmt.Sprintf("UPDATE library_root SET updated_at=clock_timestamp() WHERE id='%s'; RETURN NEW;", f.root)
		trigger := "CREATE TRIGGER movie_watch_test_tail AFTER INSERT ON movie_upgrade_tracking_job FOR EACH ROW EXECUTE FUNCTION movie_watch_test_tail()"
		if mode == "deferred_failure" {
			body = "RAISE EXCEPTION 'watch commit boundary failure';"
			trigger = "CREATE CONSTRAINT TRIGGER movie_watch_test_tail AFTER INSERT ON movie_upgrade_tracking_job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION movie_watch_test_tail()"
		}
		execIntent(t, f.p, "CREATE FUNCTION movie_watch_test_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN "+body+" END $$;"+trigger)
		got, err := f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture)
		execIntent(t, f.p, "DROP TRIGGER movie_watch_test_tail ON movie_upgrade_tracking_job; DROP FUNCTION movie_watch_test_tail()")
		if err == nil || got != nil || f.successors(t) != 0 || nativeProtectedGraph(t, f.preparedUpgradeFixture) != protected || f.snapshot(t) != before {
			t.Fatalf("%s did not roll back: %+v %v", mode, got, err)
		}
	}
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 0 {
				_, err := f.runtime.ScheduleMovieUpgradeTracking(t.Context())
				if err != nil {
					t.Errorf("periodic race: %v", err)
				}
				return
			}
			result, err := f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture)
			if err != nil && !errors.Is(err, ports.ErrDownloadWatchStale) {
				t.Errorf("hint race: %v", err)
			}
			if err == nil && (result == nil || !result.Queued) {
				t.Errorf("due result: %+v", result)
			}
		}(i)
	}
	wg.Wait()
	if f.successors(t) != 1 {
		t.Fatal("successor cardinality", f.successors(t))
	}
	var valid bool
	if e = f.p.QueryRow(t.Context(), `SELECT j.created_at>=r.recorded_at+interval '30 seconds' AND j.payload=jsonb_build_object('tracking_job_id',j.id) AND j.dedupe_key='movie-upgrade-tracking:'||b.operation_id::text||':'||b.prior_observation_id::text FROM movie_upgrade_tracking_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_tracking_result r ON r.observation_id=b.prior_observation_id WHERE b.operation_id=$1 AND b.prior_observation_id=$2`, f.operation, priorID).Scan(&valid); e != nil || !valid {
		t.Fatal("original gate/payload/dedupe", valid, e)
	}
	if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array(to_jsonb(o),to_jsonb(r),to_jsonb(p))::text FROM movie_upgrade_tracking_observation o JOIN movie_upgrade_tracking_result r USING(observation_id) JOIN movie_upgrade_tracking_permit p USING(observation_id) WHERE o.observation_id=$1`, priorID); after != prior {
		t.Fatal("prior bytes changed")
	}
	f.complete.Store(true)
	req := f.claim(t, 2*time.Minute)
	result, e := f.jobRepo.TrackMovieUpgradeJob(t.Context(), f.actor, req)
	if e != nil || result == nil || result.Outcome != "completed" {
		t.Fatal("original native consumer", result, e)
	}
	if _, e = f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
		t.Fatal("terminal capture replay")
	}
	if f.adds.Load() != 1 || nativeProtectedGraph(t, f.preparedUpgradeFixture) != protected {
		t.Fatal("original Add/protection changed")
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(t.Context()); e != nil || n != 0 {
		t.Fatal("terminal periodic", n, e)
	}
}

type publicMovieUpgradeCapture struct {
	request ports.MovieUpgradeTrackingHintRequest
}

func (c publicMovieUpgradeCapture) MovieUpgradeTrackingHintRequest() ports.MovieUpgradeTrackingHintRequest {
	return c.request
}

func TestMovieUpgradeWatchPrivateEnvelopePG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	before := f.snapshot(t)
	request := f.capture.MovieUpgradeTrackingHintRequest()
	other, e := postgres.NewMovieUpgradeWatchStore(f.runtime, hintClock{})
	if e != nil {
		t.Fatal(e)
	}
	for _, capture := range []ports.MovieUpgradeTrackingHintCapture{nil, publicMovieUpgradeCapture{request}} {
		if e = f.watch.ValidateMovieUpgradeTrackingHint(t.Context(), capture); e == nil {
			t.Fatal("forged validation")
		}
		if _, e = f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), capture); e == nil {
			t.Fatal("forged schedule")
		}
	}
	if e = other.ValidateMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
		t.Fatal("cross-instance capture")
	}
	for _, name := range []string{"expired", "future", "manifest_future", "manifest_old", "inode", "event", "instance", "sequence", "prior", "task", "operation", "path", "metadata", "rescan"} {
		t.Run(name, func(t *testing.T) {
			r := request
			switch name {
			case "expired":
				r.ReceivedAt = time.Now().Add(-40 * time.Second)
			case "future":
				r.ReceivedAt = time.Now().Add(time.Minute)
			case "manifest_future":
				r.ManifestAt = time.Now().Add(time.Minute)
			case "manifest_old":
				r.ManifestAt = r.ManifestAt.Add(-time.Hour)
			case "inode":
				r.SourceIdentity.Inode = 0
			case "event":
				r.EventID = uuid.Nil
			case "instance":
				r.InstanceID = uuid.Nil
			case "sequence":
				r.Sequence = 0
			case "prior":
				r.PriorObservationID = uuid.New()
			case "task":
				r.TaskID = uuid.New()
			case "operation":
				r.OperationID = uuid.New()
			case "path":
				r.Hint.RelativePath = "sibling.mkv"
			case "metadata":
				r.Hint.Metadata = f.hint.Metadata
			case "rescan":
				r.Hint.Kind = ports.RootChangeRescan
			}
			if _, err := f.watch.ResolveMovieUpgradeTrackingHint(t.Context(), r); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
	h := f.hint
	h.Kind = ports.RootChangeRescan
	h.RelativePath = "."
	targets, e := f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || !targets.Complete || len(targets.Targets) != 0 {
		t.Fatal("rescan", targets, e)
	}
	if f.snapshot(t) != before {
		t.Fatal("validation wrote rows")
	}
}

func TestMovieUpgradeWatchCurrentChangesPG(t *testing.T) {
	for _, c := range []struct{ name, sql string }{
		{"client", `UPDATE download_client SET username='changed',updated_at=clock_timestamp()`},
		{"mapping", `UPDATE download_client SET remote_path_map='{"/remote":"relative"}'`},
		{"root_generation", `UPDATE library_root SET generation=generation+1`},
		{"root_health", `UPDATE library_root SET health_revision=health_revision+1`},
		{"baseline", `UPDATE scan_finalization SET root_inode=root_inode+1`},
		{"account", `UPDATE app_user SET is_active=false`},
		{"subscription", `UPDATE subscription SET state='paused'`},
		{"file", `UPDATE library_file SET size_bytes=size_bytes+1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newMovieUpgradeWatchFixture(t)
			execIntent(t, f.p, c.sql)
			if e := f.watch.ValidateMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
				t.Fatal("changed capture validated")
			}
			if _, e := f.watch.ResolveMovieUpgradeTrackingHint(t.Context(), f.capture.MovieUpgradeTrackingHintRequest()); e == nil {
				t.Fatal("changed graph recaptured")
			}
			if _, e := f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
				t.Fatal("changed graph scheduled")
			}
			if f.successors(t) != 0 {
				t.Fatal("unexpected successor")
			}
		})
	}
}
