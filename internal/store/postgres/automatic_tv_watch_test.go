package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type automaticTVWatchFixture struct {
	*automaticTVObservationFixture
	watch   *postgres.AutomaticTVWatchStore
	source  ports.DownloadWatchSource
	hint    ports.RootChangeHint
	capture ports.AutomaticTVObservationHintCapture
}

func newAutomaticTVWatchFixture(t *testing.T, ack bool) *automaticTVWatchFixture {
	t.Helper()
	return automaticTVWatchFromObservation(t, newAutomaticTVObservationFixture(t, ack))
}

func automaticTVWatchFromObservation(t *testing.T, f *automaticTVObservationFixture) *automaticTVWatchFixture {
	t.Helper()
	if v, e := postgres.Migrate(t.Context(), f.pool.Config().ConnString(), "../../../migrations"); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatalf("current schema: %d %v", v, e)
	}
	scan := uuid.New()
	execIntent(t, f.pool, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	execIntent(t, f.pool, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	f.claim(t)
	snapshot := f.load(t)
	receipt, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, false))
	if e != nil || receipt.Complete || receipt.ReviewRequired {
		t.Fatalf("partial: %+v %v", receipt, e)
	}
	f.finishPermit(t)
	s := postgres.NewAutomaticTVWatchStore(f.pool, idgen.New(), hintClock{})
	page, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 256})
	if e != nil || !page.Complete || len(page.Sources) < 1 {
		t.Fatalf("sources: %+v %v", page, e)
	}
	source := page.Sources[0]
	h := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: snapshot.Video.Path, Metadata: ports.RootChangeMetadata{EventID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, ReceivedAt: time.Now(), ManifestAt: source.ManifestAt, SourceIdentity: source.Binding.RootIdentity}}
	targets, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || !targets.Complete || len(targets.Targets) != 1 {
		t.Fatalf("targets: %+v %v", targets, e)
	}
	target := targets.Targets[0]
	if target.Kind != "automatic_tv_observation_hint" || target.TaskID != f.req.TaskID || target.OperationID != f.req.OperationID || target.ClientID != source.Binding.ClientID || target.ClientStamp != source.Binding.ClientStamp || !target.ClientVersion.Equal(source.Binding.ClientVersion) {
		t.Fatal("target binding")
	}
	return &automaticTVWatchFixture{automaticTVObservationFixture: f, watch: s, source: source, hint: h, capture: target.AutomaticTVHintCapture}
}
func (f *automaticTVWatchFixture) due(t *testing.T) {
	t.Helper()
	var delay float64
	if e := f.pool.QueryRow(t.Context(), `SELECT GREATEST(0,EXTRACT(EPOCH FROM next_poll_at-clock_timestamp())) FROM automatic_tv_observation_result WHERE job_id=$1`, f.req.Lease.ID).Scan(&delay); e != nil {
		t.Fatal(e)
	}
	timer := time.NewTimer(time.Duration(delay*float64(time.Second)) + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}
func (f *automaticTVWatchFixture) snapshot(t *testing.T) string {
	t.Helper()
	return publisherFrozen(t, f.pool, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_task x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM automatic_tv_observation_result x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM automatic_tv_observation_job x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM automatic_tv_observation_permit x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM automatic_tv_physical_attempt x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM automatic_tv_execution_authority x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM automatic_tv_submission_preparation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x))::text`)
}
func (f *automaticTVWatchFixture) successors(t *testing.T) int {
	return intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_observation_job WHERE operation_id=$1 AND job_id<>$2`, f.req.OperationID, f.req.Lease.ID)
}

func TestAutomaticTVWatchDueOnlyPG(t *testing.T) {
	for _, ack := range []bool{true, false} {
		name := "ack"
		if !ack {
			name = "unknown_add_confirmed_partial"
		}
		t.Run(name, func(t *testing.T) {
			f := newAutomaticTVWatchFixture(t, ack)
			before := f.snapshot(t)
			r := f.capture.AutomaticTVObservationHintRequest()
			if r.PriorJobID != f.req.Lease.ID || r.SendAttemptID != f.command.PhysicalAttemptID || r.EventID != f.hint.Metadata.EventID || r.Hint.Metadata != (ports.RootChangeMetadata{}) {
				t.Fatal("capture identity")
			}
			if _, e := f.watch.ResolveAutomaticTVObservationHint(t.Context(), r); e != nil {
				t.Fatal(e)
			}
			result, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture)
			if e != nil || result.Queued || result.JobID != uuid.Nil || result.RetryAfter <= 0 || result.RetryAfter > 2*time.Second {
				t.Fatalf("early: %+v %v", result, e)
			}
			if f.snapshot(t) != before {
				t.Fatal("early scheduling/read modified persistent state")
			}
			prior := publisherFrozen(t, f.pool, `SELECT to_jsonb(r)::text FROM automatic_tv_observation_result r WHERE job_id=$1`, f.req.Lease.ID)
			f.due(t)
			result, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture)
			if e != nil || !result.Queued || result.JobID == uuid.Nil || result.RetryAfter != 0 {
				t.Fatalf("due: %+v %v", result, e)
			}
			job, e := db.New(f.pool).GetJobByID(t.Context(), result.JobID)
			if e != nil {
				t.Fatal(e)
			}
			var payload map[string]uuid.UUID
			if json.Unmarshal(job.Payload, &payload) != nil || len(payload) != 3 || payload["operation_id"] != r.OperationID || payload["task_id"] != r.TaskID || payload["automatic_tv_authority"] != r.OperationID || job.MaxAttempts != 3 || job.Kind != ports.AutomaticTVObservationJobKind {
				t.Fatal("non-original job format")
			}
			if got := publisherFrozen(t, f.pool, `SELECT to_jsonb(r)::text FROM automatic_tv_observation_result r WHERE job_id=$1`, f.req.Lease.ID); got != prior {
				t.Fatal("rewrote prior")
			}
			if _, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil || f.successors(t) != 1 {
				t.Fatal("capture replay created work", e)
			}
		})
	}
}

func TestAutomaticTVWatchPeriodicRacePG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	r := f.capture.AutomaticTVObservationHintRequest()
	r.EventID = uuid.New()
	r.Sequence++
	second, e := f.watch.ResolveAutomaticTVObservationHint(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	f.due(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i == 0 {
				_, err := f.observer.ScheduleAutomaticTVObservations(t.Context(), 100)
				if err != nil {
					t.Error(err)
				}
				return
			}
			c := f.capture
			if i%2 == 0 {
				c = second
			}
			_, err := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), c)
			if err != nil && !errors.Is(err, ports.ErrAutomaticTVObservationStale) {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if f.successors(t) != 1 {
		t.Fatal("multiple successors")
	}
}

func TestAutomaticTVWatchTerminalReplayPG(t *testing.T) {
	for _, state := range []string{"failed", "dead", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			f := newAutomaticTVWatchFixture(t, true)
			f.due(t)
			result, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture)
			if e != nil {
				t.Fatal(e)
			}
			execIntent(t, f.pool, `UPDATE job SET state=$2 WHERE id=$1`, result.JobID, state)
			if _, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
				t.Fatal("terminal successor rebased")
			}
			if f.successors(t) != 1 {
				t.Fatal("extra successor")
			}
		})
	}
}

func TestAutomaticTVWatchInvalidationPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"root", "UPDATE library_root SET generation=generation+1"},
		{"baseline", "UPDATE scan_finalization SET root_inode=root_inode+1"},
		{"client", "UPDATE download_client SET username='changed',updated_at=clock_timestamp()"},
		{"mapping", `UPDATE download_client SET remote_path_map='{"/remote":"relative"}'`},
		{"owner", "UPDATE app_user SET is_active=false"},
		{"subscription", "UPDATE subscription SET state='paused'"},
		{"policy", "UPDATE subscription SET quality_policy_revision=quality_policy_revision+1"},
		{"source", "UPDATE indexer_lifecycle SET enabled=false"},
		{"wanted", "UPDATE wanted_item SET user_skipped=true"},
		{"demand", "UPDATE download_requirement SET released_at=clock_timestamp()"},
		{"protection", "UPDATE acquisition_reservation SET released_at=clock_timestamp()"},
		{"isolation", "UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAutomaticTVWatchFixture(t, true)
			early, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture)
			if e != nil || early.RetryAfter <= 0 {
				t.Fatalf("early %+v %v", early, e)
			}
			execIntent(t, f.pool, c.sql)
			if _, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
				t.Fatal("changed authorization before due")
			}
			f.due(t)
			if _, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
				t.Fatal("changed authorization after due")
			}
			if f.successors(t) != 0 {
				t.Fatal("unauthorized job")
			}
		})
	}
}

func TestAutomaticTVWatchHintEnvelopePG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	before := f.snapshot(t)
	for _, name := range []string{"rescan", "no_metadata", "stale", "future", "manifest", "inode", "sibling", "missing_prior"} {
		t.Run(name, func(t *testing.T) {
			h := f.hint
			switch name {
			case "rescan":
				h.Kind = ports.RootChangeRescan
				h.RelativePath = "."
			case "no_metadata":
				h.Metadata = ports.RootChangeMetadata{}
			case "stale":
				h.Metadata.ReceivedAt = time.Now().Add(-6 * time.Second)
			case "future":
				h.Metadata.ReceivedAt = time.Now().Add(time.Minute)
			case "manifest":
				h.Metadata.ManifestAt = h.Metadata.ManifestAt.Add(-time.Minute)
			case "inode":
				h.Metadata.SourceIdentity.Inode++
			case "sibling":
				h.RelativePath = "Other.S02E03.mkv"
			case "missing_prior":
				r := f.capture.AutomaticTVObservationHintRequest()
				r.PriorJobID = uuid.New()
				if _, e := f.watch.ResolveAutomaticTVObservationHint(t.Context(), r); e == nil {
					t.Fatal("missing prior")
				}
				return
			}
			page, e := f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, h, ports.DownloadWatchTargetQuery{})
			if e == nil && (len(page.Targets) != 0 || !page.Complete) {
				t.Fatal("invalid envelope accepted", name)
			}
		})
	}
	if before != f.snapshot(t) {
		t.Fatal("read changed rows")
	}
}

func TestAutomaticTVWatchActivePhysicalPG(t *testing.T) {
	for _, kind := range []string{"observation", "send", "send_job"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVWatchFixture(t, true)
			switch kind {
			case "observation":
				f.due(t)
				f.claim(t)
			case "send":
				_, e := f.pool.Exec(t.Context(), `INSERT INTO automatic_tv_physical_attempt(id,operation_id,job_id,lease_owner,lease_token,backend_pid,advisory_key) SELECT $1,operation_id,job_id,lease_owner,lease_token,backend_pid,advisory_key FROM automatic_tv_physical_attempt WHERE id=$2`, uuid.New(), f.command.PhysicalAttemptID)
				if e == nil {
					t.Fatal("database allowed original send replay after partial")
				}
				return
			case "send_job":
				execIntent(t, f.pool, `INSERT INTO job(id,kind,state,payload,dedupe_key) SELECT $1,kind,'queued',payload,'active-send-test' FROM job WHERE id=$2`, uuid.New(), f.request.Lease.ID)
			}
			before := f.snapshot(t)
			if _, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
				t.Fatal("active physical/send accepted")
			}
			if f.snapshot(t) != before {
				t.Fatal("new job")
			}
		})
	}
}

func TestAutomaticTVWatchTailRollbackPG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	f.due(t)
	before := f.snapshot(t)
	execIntent(t, f.pool, `CREATE FUNCTION automatic_tv_watch_tail_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE download_client SET username='tail mutation'; RETURN NEW; END $$`)
	execIntent(t, f.pool, `CREATE CONSTRAINT TRIGGER automatic_tv_watch_tail_test AFTER INSERT ON automatic_tv_observation_job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION automatic_tv_watch_tail_test()`)
	if _, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
		t.Fatal("deferred mutation accepted")
	}
	if f.successors(t) != 0 || before != f.snapshot(t) {
		t.Fatal("tail left side effects")
	}
}

type automaticTVForgedCapture struct {
	r ports.AutomaticTVObservationHintRequest
}

func (c automaticTVForgedCapture) AutomaticTVObservationHintRequest() ports.AutomaticTVObservationHintRequest {
	return c.r
}
func TestAutomaticTVWatchOpaqueCapturePG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	forged := automaticTVForgedCapture{f.capture.AutomaticTVObservationHintRequest()}
	if _, e := f.watch.ScheduleAutomaticTVObservationHint(context.Background(), forged); e == nil {
		t.Fatal("forged capture")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := f.watch.ScheduleAutomaticTVObservationHint(ctx, f.capture); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
